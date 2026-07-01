package resource

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
)

// This file is the MinIO (S3) Provisioner: per-project bucket isolation on the
// shared MinIO, plus the object-lifecycle / versioning / policy / CORS verbs
// (spec 29 §object storage). It uses the PURE-GO aws-sdk-go-v2 S3 client in-process
// (CGO-free, so it stays inside the single static binary — unlike the mc/aws
// external tools) pointed at MinIO with PATH-STYLE addressing and the root creds.
//
// The SDK sits behind the small S3API seam so unit/race tests run without a live
// MinIO: inject MinIO.Factory with a fake. Bucket names are transparently
// PROJECT-PREFIXED for global uniqueness (the shared MinIO is one flat namespace);
// callers escape with --no-prefix. The same code path targets a LocalStack S3
// endpoint later — it is an endpoint swap, never a branch.

// S3API is the subset of the aws-sdk-go-v2 S3 client the MinIO provisioner uses.
// *s3.Client satisfies it; tests inject a fake. Every signature matches the SDK
// (including the variadic option funcs) so the real client is a drop-in.
type S3API interface {
	CreateBucket(context.Context, *s3.CreateBucketInput, ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	DeleteBucket(context.Context, *s3.DeleteBucketInput, ...func(*s3.Options)) (*s3.DeleteBucketOutput, error)
	ListBuckets(context.Context, *s3.ListBucketsInput, ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
	PutBucketVersioning(context.Context, *s3.PutBucketVersioningInput, ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error)
	GetBucketVersioning(context.Context, *s3.GetBucketVersioningInput, ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error)
	PutBucketLifecycleConfiguration(context.Context, *s3.PutBucketLifecycleConfigurationInput, ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error)
	GetBucketLifecycleConfiguration(context.Context, *s3.GetBucketLifecycleConfigurationInput, ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error)
	DeleteBucketLifecycle(context.Context, *s3.DeleteBucketLifecycleInput, ...func(*s3.Options)) (*s3.DeleteBucketLifecycleOutput, error)
	PutBucketPolicy(context.Context, *s3.PutBucketPolicyInput, ...func(*s3.Options)) (*s3.PutBucketPolicyOutput, error)
	GetBucketPolicy(context.Context, *s3.GetBucketPolicyInput, ...func(*s3.Options)) (*s3.GetBucketPolicyOutput, error)
	PutBucketCors(context.Context, *s3.PutBucketCorsInput, ...func(*s3.Options)) (*s3.PutBucketCorsOutput, error)
	GetBucketCors(context.Context, *s3.GetBucketCorsInput, ...func(*s3.Options)) (*s3.GetBucketCorsOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

// S3Factory builds an S3API for a resolved Target (the 127.0.0.1 overlay endpoint
// + the instance's root creds). Injectable so the provisioner is endpoint-free in
// tests; nil selects the real aws-sdk-go-v2 client.
type S3Factory func(ctx context.Context, t Target) (S3API, error)

// BucketController is the object-storage surface the s3 CLI depends on beyond the
// generic Provisioner (list/lifecycle/versioning/policy/cors). MinIO implements it;
// the orchestrate helpers type-assert the registered provisioner to it.
type BucketController interface {
	Provisioner
	ListBuckets(ctx context.Context, t Target, prefix string) ([]string, error)
	SetVersioning(ctx context.Context, t Target, bucket string, enabled bool) error
	GetVersioning(ctx context.Context, t Target, bucket string) (string, error)
	SetLifecycle(ctx context.Context, t Target, bucket string, rule LifecycleRule) error
	GetLifecycle(ctx context.Context, t Target, bucket string) ([]map[string]any, error)
	RemoveLifecycle(ctx context.Context, t Target, bucket string) error
	SetPolicy(ctx context.Context, t Target, bucket, policyJSON string) error
	GetPolicy(ctx context.Context, t Target, bucket string) (string, error)
	SetCORS(ctx context.Context, t Target, bucket string, rules []s3types.CORSRule) error
	GetCORS(ctx context.Context, t Target, bucket string) ([]s3types.CORSRule, error)
}

// MinIO is the minio/S3 Provisioner. Factory nil → the real path-style client.
type MinIO struct {
	Factory S3Factory
}

// Ensure MinIO satisfies both contracts at compile time.
var (
	_ Provisioner      = MinIO{}
	_ BucketController = MinIO{}
)

// Engine reports the shared-template capability this provisioner serves.
func (MinIO) Engine() string { return "minio" }

// Kinds are the resource kinds this provisioner can create.
func (MinIO) Kinds() []string { return []string{"bucket", "lifecycle", "access_key"} }

// LifecycleRule is the portable object-lifecycle intent (spec 29): a mandatory
// expiry in days plus an optional storage-class transition. `--expire-days` is
// portable across MinIO and S3; `--transition` is engine-conditional.
type LifecycleRule struct {
	ExpireDays     int    // objects expire after N days (0 → no expiry rule)
	Prefix         string // object-key prefix the rule applies to ("" → whole bucket)
	TransitionDays int    // transition to TransitionTier after N days (0 → none)
	TransitionTier string // e.g. STANDARD_IA, GLACIER (S3 storage class)
}

// s3Endpoint is the loopback admin endpoint for the overlay-published instance.
func s3Endpoint(t Target) string { return fmt.Sprintf("http://%s:%d", t.Host, t.Port) }

// client resolves the S3 client for a target (the injected factory or the real
// path-style aws-sdk-go-v2 client with the instance root creds).
func (m MinIO) client(ctx context.Context, t Target) (S3API, error) {
	if m.Factory != nil {
		return m.Factory(ctx, t)
	}
	return defaultS3Client(ctx, t)
}

// defaultS3Client builds a pure-Go path-style S3 client against the MinIO endpoint
// with the instance's root credentials (accessKey=rootUser, secret=rootPassword).
func defaultS3Client(_ context.Context, t Target) (S3API, error) {
	access := t.AdminEnv["user"]
	secret := t.AdminEnv["password"]
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(access, secret, ""),
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(s3Endpoint(t))
		o.UsePathStyle = true // MinIO does not do virtual-host buckets
	}), nil
}

// bucketExists reports whether the bucket already exists (HeadBucket 2xx).
func bucketExists(ctx context.Context, c S3API, bucket string) (bool, error) {
	_, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

// Ensure idempotently creates the bucket (existence-guarded — CreateBucket errors
// if it already exists) and applies any requested versioning / lifecycle from
// Params. Returns the connection facts (endpoint + bucket + creds).
func (m MinIO) Ensure(ctx context.Context, t Target, r Resource) (Attrs, error) {
	c, err := m.client(ctx, t)
	if err != nil {
		return nil, err
	}
	bucket := r.Name
	if bucket == "" {
		bucket = r.Owner
	}

	if r.Kind == "lifecycle" {
		rule := lifecycleFromParams(r.Params)
		if err := m.putLifecycle(ctx, c, bucket, rule); err != nil {
			return nil, err
		}
		return m.attrs(t, bucket), nil
	}

	exists, err := bucketExists(ctx, c, bucket)
	if err != nil {
		return nil, fmt.Errorf("head bucket %q: %w", bucket, err)
	}
	if !exists {
		if _, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
			if !isAlreadyOwned(err) {
				return nil, fmt.Errorf("create bucket %q: %w", bucket, err)
			}
		}
	}
	if boolParam(r.Params, "versioning") {
		if err := m.setVersioning(ctx, c, bucket, true); err != nil {
			return nil, err
		}
	}
	if rule := lifecycleFromParams(r.Params); rule.ExpireDays > 0 || rule.TransitionDays > 0 {
		if err := m.putLifecycle(ctx, c, bucket, rule); err != nil {
			return nil, err
		}
	}
	return m.attrs(t, bucket), nil
}

// Drop removes the bucket (or, for the lifecycle kind, just its lifecycle config).
// Idempotent: a missing bucket/config is not an error. It never touches the shared
// MinIO container — only the tenant object.
func (m MinIO) Drop(ctx context.Context, t Target, r Resource) error {
	c, err := m.client(ctx, t)
	if err != nil {
		return err
	}
	bucket := r.Name
	if bucket == "" {
		bucket = r.Owner
	}
	if r.Kind == "lifecycle" {
		_, err := c.DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String(bucket)})
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("delete lifecycle on %q: %w", bucket, err)
		}
		return nil
	}
	// `rb --force` (Params["force"]): recursively purge every object first so a
	// non-empty bucket can be removed (a plain DeleteBucket fails with BucketNotEmpty).
	if boolParam(r.Params, "force") {
		if err := m.emptyBucket(ctx, c, bucket); err != nil {
			return err
		}
	}
	if _, err := c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete bucket %q: %w", bucket, err)
	}
	return nil
}

// emptyBucket recursively deletes every object in the bucket via paginated
// ListObjectsV2 + batched DeleteObjects, so a `rb --force` can remove a non-empty
// bucket. Idempotent: an already-empty or missing bucket is a no-op.
func (MinIO) emptyBucket(ctx context.Context, c S3API, bucket string) error {
	var token *string
	for {
		out, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			ContinuationToken: token,
		})
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return fmt.Errorf("list objects in %q: %w", bucket, err)
		}
		if len(out.Contents) > 0 {
			ids := make([]s3types.ObjectIdentifier, 0, len(out.Contents))
			for _, o := range out.Contents {
				ids = append(ids, s3types.ObjectIdentifier{Key: o.Key})
			}
			if _, err := c.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucket),
				Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
			}); err != nil {
				return fmt.Errorf("delete objects in %q: %w", bucket, err)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			return nil
		}
		token = out.NextContinuationToken
	}
}

// Preflight verifies the endpoint is reachable and the creds are valid (a
// ListBuckets round-trip). Absence degrades only the s3 verbs, never `up`.
func (m MinIO) Preflight(ctx context.Context, t Target) error {
	c, err := m.client(ctx, t)
	if err != nil {
		return err
	}
	_, err = c.ListBuckets(ctx, &s3.ListBucketsInput{})
	return err
}

// --- BucketController extras (used by the s3 CLI beyond create/drop) ----------

// ListBuckets returns bucket names, optionally filtered to a project prefix (so a
// tenant sees only its own buckets). A lock-free read.
func (m MinIO) ListBuckets(ctx context.Context, t Target, prefix string) ([]string, error) {
	c, err := m.client(ctx, t)
	if err != nil {
		return nil, err
	}
	out, err := c.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, b := range out.Buckets {
		name := aws.ToString(b.Name)
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// SetVersioning enables or suspends bucket versioning.
func (m MinIO) SetVersioning(ctx context.Context, t Target, bucket string, enabled bool) error {
	c, err := m.client(ctx, t)
	if err != nil {
		return err
	}
	return m.setVersioning(ctx, c, bucket, enabled)
}

// GetVersioning reports the bucket's versioning status ("Enabled"/"Suspended"/"").
func (m MinIO) GetVersioning(ctx context.Context, t Target, bucket string) (string, error) {
	c, err := m.client(ctx, t)
	if err != nil {
		return "", err
	}
	out, err := c.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
	if err != nil {
		return "", err
	}
	return string(out.Status), nil
}

// SetLifecycle applies a single expiry (+optional transition) rule to the bucket.
func (m MinIO) SetLifecycle(ctx context.Context, t Target, bucket string, rule LifecycleRule) error {
	c, err := m.client(ctx, t)
	if err != nil {
		return err
	}
	return m.putLifecycle(ctx, c, bucket, rule)
}

// GetLifecycle returns the bucket's lifecycle rules as a human summary (id→days).
func (m MinIO) GetLifecycle(ctx context.Context, t Target, bucket string) ([]map[string]any, error) {
	c, err := m.client(ctx, t)
	if err != nil {
		return nil, err
	}
	out, err := c.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var rules []map[string]any
	for _, r := range out.Rules {
		row := map[string]any{"id": aws.ToString(r.ID), "status": string(r.Status)}
		if r.Expiration != nil && r.Expiration.Days != nil {
			row["expire_days"] = int(*r.Expiration.Days)
		}
		for _, tr := range r.Transitions {
			if tr.Days != nil {
				row["transition_days"] = int(*tr.Days)
				row["transition_tier"] = string(tr.StorageClass)
			}
		}
		rules = append(rules, row)
	}
	return rules, nil
}

// RemoveLifecycle deletes the bucket's lifecycle configuration (idempotent).
func (m MinIO) RemoveLifecycle(ctx context.Context, t Target, bucket string) error {
	return m.Drop(ctx, t, Resource{Engine: "minio", Kind: "lifecycle", Name: bucket})
}

// SetPolicy sets the bucket policy from a raw JSON document.
func (m MinIO) SetPolicy(ctx context.Context, t Target, bucket, policyJSON string) error {
	c, err := m.client(ctx, t)
	if err != nil {
		return err
	}
	_, err = c.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: aws.String(bucket), Policy: aws.String(policyJSON),
	})
	if err != nil {
		return fmt.Errorf("put policy on %q: %w", bucket, err)
	}
	return nil
}

// GetPolicy returns the bucket policy JSON ("" when none is set).
func (m MinIO) GetPolicy(ctx context.Context, t Target, bucket string) (string, error) {
	c, err := m.client(ctx, t)
	if err != nil {
		return "", err
	}
	out, err := c.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(bucket)})
	if err != nil {
		if isNotFound(err) || apiCode(err) == "NoSuchBucketPolicy" {
			return "", nil
		}
		return "", err
	}
	return aws.ToString(out.Policy), nil
}

// SetCORS sets the bucket CORS configuration from raw JSON (a []CORSRule).
func (m MinIO) SetCORS(ctx context.Context, t Target, bucket string, rules []s3types.CORSRule) error {
	c, err := m.client(ctx, t)
	if err != nil {
		return err
	}
	_, err = c.PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket:            aws.String(bucket),
		CORSConfiguration: &s3types.CORSConfiguration{CORSRules: rules},
	})
	if err != nil {
		return fmt.Errorf("put cors on %q: %w", bucket, err)
	}
	return nil
}

// GetCORS returns the bucket CORS rules (nil when none).
func (m MinIO) GetCORS(ctx context.Context, t Target, bucket string) ([]s3types.CORSRule, error) {
	c, err := m.client(ctx, t)
	if err != nil {
		return nil, err
	}
	out, err := c.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(bucket)})
	if err != nil {
		if isNotFound(err) || apiCode(err) == "NoSuchCORSConfiguration" {
			return nil, nil
		}
		return nil, err
	}
	return out.CORSRules, nil
}

// --- internals ----------------------------------------------------------------

func (MinIO) attrs(t Target, bucket string) Attrs {
	return Attrs{
		"endpoint":  s3Endpoint(t),
		"host":      sharedHost(t.Instance),
		"port":      "9000",
		"bucket":    bucket,
		"accessKey": t.AdminEnv["user"],
		"secretKey": t.AdminEnv["password"],
	}
}

func (MinIO) setVersioning(ctx context.Context, c S3API, bucket string, enabled bool) error {
	status := s3types.BucketVersioningStatusSuspended
	if enabled {
		status = s3types.BucketVersioningStatusEnabled
	}
	_, err := c.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  aws.String(bucket),
		VersioningConfiguration: &s3types.VersioningConfiguration{Status: status},
	})
	if err != nil {
		return fmt.Errorf("set versioning on %q: %w", bucket, err)
	}
	return nil
}

func (MinIO) putLifecycle(ctx context.Context, c S3API, bucket string, rule LifecycleRule) error {
	lr := s3types.LifecycleRule{
		ID:     aws.String("devstack-" + bucket),
		Status: s3types.ExpirationStatusEnabled,
		Filter: &s3types.LifecycleRuleFilter{Prefix: aws.String(rule.Prefix)},
	}
	if rule.ExpireDays > 0 {
		lr.Expiration = &s3types.LifecycleExpiration{Days: aws.Int32(int32(rule.ExpireDays))}
	}
	if rule.TransitionDays > 0 && rule.TransitionTier != "" {
		lr.Transitions = []s3types.Transition{{
			Days:         aws.Int32(int32(rule.TransitionDays)),
			StorageClass: s3types.TransitionStorageClass(rule.TransitionTier),
		}}
	}
	_, err := c.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                 aws.String(bucket),
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{Rules: []s3types.LifecycleRule{lr}},
	})
	if err != nil {
		return fmt.Errorf("put lifecycle on %q: %w", bucket, err)
	}
	return nil
}

func lifecycleFromParams(p map[string]any) LifecycleRule {
	return LifecycleRule{
		ExpireDays:     intParam(p, "expire_days"),
		Prefix:         paramStr(p, "prefix"),
		TransitionDays: intParam(p, "transition_days"),
		TransitionTier: paramStr(p, "transition_tier"),
	}
}

func boolParam(p map[string]any, key string) bool {
	switch v := p[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	}
	return false
}

func intParam(p map[string]any, key string) int {
	switch v := p[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// apiCode extracts a smithy API error code, or "" if err is not an API error.
func apiCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

// isNotFound reports the standard "no such bucket / 404 / not found" family.
func isNotFound(err error) bool {
	var nsb *s3types.NoSuchBucket
	if errors.As(err, &nsb) {
		return true
	}
	switch apiCode(err) {
	case "NoSuchBucket", "NotFound", "404", "NoSuchLifecycleConfiguration":
		return true
	}
	return false
}

// isAlreadyOwned reports the idempotent "bucket already exists and is yours" case.
func isAlreadyOwned(err error) bool {
	var owned *s3types.BucketAlreadyOwnedByYou
	var exists *s3types.BucketAlreadyExists
	if errors.As(err, &owned) || errors.As(err, &exists) {
		return true
	}
	switch apiCode(err) {
	case "BucketAlreadyOwnedByYou", "BucketAlreadyExists":
		return true
	}
	return false
}

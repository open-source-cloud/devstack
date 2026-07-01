package resource

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeS3 is an in-memory S3API for the MinIO provisioner tests (no live endpoint).
type fakeS3 struct {
	buckets    map[string]bool
	versioning map[string]string
	lifecycle  map[string][]s3types.LifecycleRule
	policy     map[string]string
	cors       map[string][]s3types.CORSRule
	calls      []string
}

func newFakeS3() *fakeS3 {
	return &fakeS3{
		buckets:    map[string]bool{},
		versioning: map[string]string{},
		lifecycle:  map[string][]s3types.LifecycleRule{},
		policy:     map[string]string{},
		cors:       map[string][]s3types.CORSRule{},
	}
}

func (f *fakeS3) CreateBucket(_ context.Context, in *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	f.calls = append(f.calls, "CreateBucket:"+aws.ToString(in.Bucket))
	if f.buckets[aws.ToString(in.Bucket)] {
		return nil, &s3types.BucketAlreadyOwnedByYou{}
	}
	f.buckets[aws.ToString(in.Bucket)] = true
	return &s3.CreateBucketOutput{}, nil
}

func (f *fakeS3) HeadBucket(_ context.Context, in *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	if f.buckets[aws.ToString(in.Bucket)] {
		return &s3.HeadBucketOutput{}, nil
	}
	return nil, &s3types.NotFound{}
}

func (f *fakeS3) DeleteBucket(_ context.Context, in *s3.DeleteBucketInput, _ ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	f.calls = append(f.calls, "DeleteBucket:"+aws.ToString(in.Bucket))
	if !f.buckets[aws.ToString(in.Bucket)] {
		return nil, &s3types.NoSuchBucket{}
	}
	delete(f.buckets, aws.ToString(in.Bucket))
	return &s3.DeleteBucketOutput{}, nil
}

func (f *fakeS3) ListBuckets(_ context.Context, _ *s3.ListBucketsInput, _ ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	out := &s3.ListBucketsOutput{}
	for name := range f.buckets {
		out.Buckets = append(out.Buckets, s3types.Bucket{Name: aws.String(name)})
	}
	return out, nil
}

func (f *fakeS3) PutBucketVersioning(_ context.Context, in *s3.PutBucketVersioningInput, _ ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error) {
	f.versioning[aws.ToString(in.Bucket)] = string(in.VersioningConfiguration.Status)
	return &s3.PutBucketVersioningOutput{}, nil
}

func (f *fakeS3) GetBucketVersioning(_ context.Context, in *s3.GetBucketVersioningInput, _ ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	return &s3.GetBucketVersioningOutput{Status: s3types.BucketVersioningStatus(f.versioning[aws.ToString(in.Bucket)])}, nil
}

func (f *fakeS3) PutBucketLifecycleConfiguration(_ context.Context, in *s3.PutBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error) {
	f.lifecycle[aws.ToString(in.Bucket)] = in.LifecycleConfiguration.Rules
	return &s3.PutBucketLifecycleConfigurationOutput{}, nil
}

func (f *fakeS3) GetBucketLifecycleConfiguration(_ context.Context, in *s3.GetBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error) {
	r, ok := f.lifecycle[aws.ToString(in.Bucket)]
	if !ok {
		return nil, &s3types.NoSuchBucket{}
	}
	return &s3.GetBucketLifecycleConfigurationOutput{Rules: r}, nil
}

func (f *fakeS3) DeleteBucketLifecycle(_ context.Context, in *s3.DeleteBucketLifecycleInput, _ ...func(*s3.Options)) (*s3.DeleteBucketLifecycleOutput, error) {
	delete(f.lifecycle, aws.ToString(in.Bucket))
	return &s3.DeleteBucketLifecycleOutput{}, nil
}

func (f *fakeS3) PutBucketPolicy(_ context.Context, in *s3.PutBucketPolicyInput, _ ...func(*s3.Options)) (*s3.PutBucketPolicyOutput, error) {
	f.policy[aws.ToString(in.Bucket)] = aws.ToString(in.Policy)
	return &s3.PutBucketPolicyOutput{}, nil
}

func (f *fakeS3) GetBucketPolicy(_ context.Context, in *s3.GetBucketPolicyInput, _ ...func(*s3.Options)) (*s3.GetBucketPolicyOutput, error) {
	p, ok := f.policy[aws.ToString(in.Bucket)]
	if !ok {
		return nil, &s3types.NoSuchBucket{}
	}
	return &s3.GetBucketPolicyOutput{Policy: aws.String(p)}, nil
}

func (f *fakeS3) PutBucketCors(_ context.Context, in *s3.PutBucketCorsInput, _ ...func(*s3.Options)) (*s3.PutBucketCorsOutput, error) {
	f.cors[aws.ToString(in.Bucket)] = in.CORSConfiguration.CORSRules
	return &s3.PutBucketCorsOutput{}, nil
}

func (f *fakeS3) GetBucketCors(_ context.Context, in *s3.GetBucketCorsInput, _ ...func(*s3.Options)) (*s3.GetBucketCorsOutput, error) {
	c, ok := f.cors[aws.ToString(in.Bucket)]
	if !ok {
		return nil, &s3types.NoSuchBucket{}
	}
	return &s3.GetBucketCorsOutput{CORSRules: c}, nil
}

func minioTarget() Target {
	return Target{
		Instance: "minio", Host: "127.0.0.1", Port: 49000,
		AdminEnv: map[string]string{"user": "devstackadmin", "password": "devstackadmin"},
	}
}

func fakeMinIO(f *fakeS3) MinIO {
	return MinIO{Factory: func(context.Context, Target) (S3API, error) { return f, nil }}
}

func TestMinIOEngineAndKinds(t *testing.T) {
	m := MinIO{}
	if m.Engine() != "minio" {
		t.Errorf("Engine() = %q, want minio", m.Engine())
	}
	if got := m.Kinds(); len(got) == 0 || got[0] != "bucket" {
		t.Errorf("Kinds() = %v, want bucket first", got)
	}
}

func TestMinIOEnsureBucketWithVersioningAndLifecycle(t *testing.T) {
	f := newFakeS3()
	m := fakeMinIO(f)
	attrs, err := m.Ensure(context.Background(), minioTarget(), Resource{
		Engine: "minio", Kind: "bucket", Name: "web-uploads", Owner: "web",
		Params: map[string]any{"versioning": true, "expire_days": 30},
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !f.buckets["web-uploads"] {
		t.Errorf("bucket web-uploads not created: %v", f.buckets)
	}
	if f.versioning["web-uploads"] != "Enabled" {
		t.Errorf("versioning not enabled: %v", f.versioning)
	}
	rules := f.lifecycle["web-uploads"]
	if len(rules) != 1 || rules[0].Expiration == nil || aws.ToInt32(rules[0].Expiration.Days) != 30 {
		t.Errorf("expiry rule not set to 30 days: %+v", rules)
	}
	if attrs["bucket"] != "web-uploads" || attrs["endpoint"] != "http://127.0.0.1:49000" {
		t.Errorf("attrs = %v", attrs)
	}
	if attrs["accessKey"] != "devstackadmin" {
		t.Errorf("accessKey not surfaced from root creds: %v", attrs)
	}
}

func TestMinIOEnsureBucketIdempotent(t *testing.T) {
	f := newFakeS3()
	f.buckets["web-uploads"] = true // already exists
	m := fakeMinIO(f)
	if _, err := m.Ensure(context.Background(), minioTarget(), Resource{
		Engine: "minio", Kind: "bucket", Name: "web-uploads", Owner: "web",
	}); err != nil {
		t.Fatalf("Ensure idempotent: %v", err)
	}
	// HeadBucket short-circuits — CreateBucket must not be called for an existing one.
	for _, c := range f.calls {
		if c == "CreateBucket:web-uploads" {
			t.Errorf("existing bucket must not be re-created: %v", f.calls)
		}
	}
}

func TestMinIOLifecycleKind(t *testing.T) {
	f := newFakeS3()
	f.buckets["web-uploads"] = true
	m := fakeMinIO(f)
	if _, err := m.Ensure(context.Background(), minioTarget(), Resource{
		Engine: "minio", Kind: "lifecycle", Name: "web-uploads",
		Params: map[string]any{"expire_days": 7, "transition_days": 3, "transition_tier": "STANDARD_IA"},
	}); err != nil {
		t.Fatalf("Ensure lifecycle: %v", err)
	}
	rules := f.lifecycle["web-uploads"]
	if len(rules) != 1 || len(rules[0].Transitions) != 1 {
		t.Fatalf("lifecycle+transition not set: %+v", rules)
	}
	if string(rules[0].Transitions[0].StorageClass) != "STANDARD_IA" {
		t.Errorf("transition tier = %q, want STANDARD_IA", rules[0].Transitions[0].StorageClass)
	}
}

func TestMinIOVersioningToggle(t *testing.T) {
	f := newFakeS3()
	f.buckets["web-data"] = true
	m := fakeMinIO(f)
	if err := m.SetVersioning(context.Background(), minioTarget(), "web-data", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetVersioning(context.Background(), minioTarget(), "web-data"); got != "Enabled" {
		t.Errorf("versioning = %q, want Enabled", got)
	}
	if err := m.SetVersioning(context.Background(), minioTarget(), "web-data", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetVersioning(context.Background(), minioTarget(), "web-data"); got != "Suspended" {
		t.Errorf("versioning = %q, want Suspended", got)
	}
}

func TestMinIOPolicyAndCORS(t *testing.T) {
	f := newFakeS3()
	f.buckets["web-pub"] = true
	m := fakeMinIO(f)
	pol := `{"Version":"2012-10-17","Statement":[]}`
	if err := m.SetPolicy(context.Background(), minioTarget(), "web-pub", pol); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetPolicy(context.Background(), minioTarget(), "web-pub"); got != pol {
		t.Errorf("policy = %q, want %q", got, pol)
	}
	// No policy on an untouched bucket → empty, not an error.
	if got, err := m.GetPolicy(context.Background(), minioTarget(), "web-pub2"); err != nil || got != "" {
		t.Errorf("absent policy: got=%q err=%v, want empty/no-error", got, err)
	}
	rules := []s3types.CORSRule{{AllowedMethods: []string{"GET"}, AllowedOrigins: []string{"*"}}}
	if err := m.SetCORS(context.Background(), minioTarget(), "web-pub", rules); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetCORS(context.Background(), minioTarget(), "web-pub")
	if err != nil || len(got) != 1 || got[0].AllowedMethods[0] != "GET" {
		t.Errorf("cors round-trip: %+v err=%v", got, err)
	}
}

func TestMinIOListBucketsPrefixIsolation(t *testing.T) {
	f := newFakeS3()
	f.buckets["web-uploads"] = true
	f.buckets["web-assets"] = true
	f.buckets["api-orders"] = true // another tenant
	m := fakeMinIO(f)
	names, err := m.ListBuckets(context.Background(), minioTarget(), "web-")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "web-assets" || names[1] != "web-uploads" {
		t.Errorf("prefix filter should return only web- buckets sorted, got %v", names)
	}
}

func TestMinIODropBucket(t *testing.T) {
	f := newFakeS3()
	f.buckets["web-uploads"] = true
	m := fakeMinIO(f)
	if err := m.Drop(context.Background(), minioTarget(), Resource{Engine: "minio", Kind: "bucket", Name: "web-uploads"}); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if f.buckets["web-uploads"] {
		t.Error("bucket not deleted")
	}
	// Idempotent: dropping a missing bucket is not an error.
	if err := m.Drop(context.Background(), minioTarget(), Resource{Engine: "minio", Kind: "bucket", Name: "web-uploads"}); err != nil {
		t.Errorf("drop of missing bucket must be idempotent: %v", err)
	}
}

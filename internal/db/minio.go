package db

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// This file is the MinIO (S3) snapshot/restore Dumper (spec 15). Unlike the
// pg/redis dumpers it needs NO external binary: it reuses the already-vendored,
// PURE-GO aws-sdk-go-v2 S3 client (so it stays inside the single static binary and
// there is nothing to install). A snapshot lists + gets every object in the
// project's tenant bucket and writes them into a deterministic tar archive under
// the content-addressed snapshot store; a restore reads the tar and puts each
// object back into the SAME bucket (bucket names are globally unique per instance,
// spec 15). It never touches the shared MinIO container — only the tenant bucket's
// objects — so the never-recreate guard holds.
//
// The S3 surface sits behind the S3Factory seam so unit tests run without a live
// endpoint (inject a fake S3Snapshotter); nil selects the real path-style client.

// S3Snapshotter is the minimal object-copy surface the MinIO dumper needs: list
// the tenant bucket's keys, get one object's bytes, put one object back. The real
// aws-sdk-go-v2 client is wrapped by awsS3Snapshotter; tests inject a fake.
type S3Snapshotter interface {
	ListKeys(ctx context.Context, bucket string) ([]string, error)
	Get(ctx context.Context, bucket, key string) ([]byte, error)
	Put(ctx context.Context, bucket, key string, body []byte) error
}

// S3Factory builds an S3Snapshotter for a resolved tenant endpoint (the 127.0.0.1
// overlay host/port + the instance root creds carried on ConnInfo). Injectable so
// the dumper is endpoint-free in tests; nil selects the real client.
type S3Factory func(ctx context.Context, conn ConnInfo) (S3Snapshotter, error)

// MinioDumper snapshots/restores a tenant bucket via the S3 API. Factory nil → the
// real pure-Go path-style aws-sdk-go-v2 client.
type MinioDumper struct {
	Factory S3Factory
}

// Ensure MinioDumper satisfies the Dumper seam at compile time.
var _ Dumper = MinioDumper{}

// Preflight always succeeds: the MinIO dumper uses the in-process pure-Go S3
// client, so there is no external tool to probe (unlike pg/redis). Endpoint/creds
// reachability surfaces on the first List call instead.
func (MinioDumper) Preflight(context.Context) error { return nil }

// client resolves the S3Snapshotter for a tenant endpoint (the injected factory
// or the real path-style aws-sdk-go-v2 client with the instance root creds).
func (m MinioDumper) client(ctx context.Context, conn ConnInfo) (S3Snapshotter, error) {
	if m.Factory != nil {
		return m.Factory(ctx, conn)
	}
	return newAWSS3Snapshotter(conn)
}

// Snapshot writes every object in the tenant bucket (ConnInfo.Database) into a
// deterministic tar at outPath (keys sorted so identical bucket contents produce
// an identical archive → the content-addressed store dedupes them). Only the
// tenant bucket is read; a snapshot of project A can never read project B's data.
func (m MinioDumper) Snapshot(ctx context.Context, conn ConnInfo, outPath string) error {
	c, err := m.client(ctx, conn)
	if err != nil {
		return err
	}
	bucket := conn.Database
	keys, err := c.ListKeys(ctx, bucket)
	if err != nil {
		return fmt.Errorf("list objects in bucket %q: %w", bucket, err)
	}
	sort.Strings(keys) // determinism: byte-identical archive for identical contents
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create snapshot %q: %w", outPath, err)
	}
	tw := tar.NewWriter(f)
	for _, key := range keys {
		body, err := c.Get(ctx, bucket, key)
		if err != nil {
			_ = tw.Close()
			_ = f.Close()
			return fmt.Errorf("get object %q from %q: %w", key, bucket, err)
		}
		hdr := &tar.Header{Name: key, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = tw.Close()
			_ = f.Close()
			return fmt.Errorf("write tar header %q: %w", key, err)
		}
		if _, err := tw.Write(body); err != nil {
			_ = tw.Close()
			_ = f.Close()
			return fmt.Errorf("write tar body %q: %w", key, err)
		}
	}
	if err := tw.Close(); err != nil {
		_ = f.Close()
		return fmt.Errorf("close tar writer: %w", err)
	}
	return f.Close()
}

// Restore reads the tar at inPath and puts each object back into the SAME tenant
// bucket. The caller has emptied/recreated the bucket (the tenant reset) before
// this runs; a plain put over existing keys overwrites in place.
func (m MinioDumper) Restore(ctx context.Context, conn ConnInfo, inPath string) error {
	c, err := m.client(ctx, conn)
	if err != nil {
		return err
	}
	bucket := conn.Database
	f, err := os.Open(inPath)
	if err != nil {
		return fmt.Errorf("open snapshot %q: %w", inPath, err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar %q: %w", inPath, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return fmt.Errorf("read tar entry %q: %w", hdr.Name, err)
		}
		if err := c.Put(ctx, bucket, hdr.Name, body); err != nil {
			return fmt.Errorf("put object %q into %q: %w", hdr.Name, bucket, err)
		}
	}
	return nil
}

// IsEmpty reports whether the tenant bucket has no objects (the restore-over-
// non-empty guard).
func (m MinioDumper) IsEmpty(ctx context.Context, conn ConnInfo) (bool, error) {
	c, err := m.client(ctx, conn)
	if err != nil {
		return false, err
	}
	keys, err := c.ListKeys(ctx, conn.Database)
	if err != nil {
		return false, fmt.Errorf("list objects in bucket %q: %w", conn.Database, err)
	}
	return len(keys) == 0, nil
}

// awsS3Snapshotter wraps a real *s3.Client with the small S3Snapshotter surface.
type awsS3Snapshotter struct{ c *s3.Client }

// newAWSS3Snapshotter builds a pure-Go path-style S3 client against the tenant
// endpoint (http://Host:Port) with the instance root credentials (MinIO does not
// do virtual-host buckets — path style is required).
func newAWSS3Snapshotter(conn ConnInfo) (S3Snapshotter, error) {
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(conn.User, conn.Password, ""),
	}
	endpoint := fmt.Sprintf("http://%s:%d", conn.Host, conn.Port)
	c := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	return &awsS3Snapshotter{c: c}, nil
}

func (a *awsS3Snapshotter) ListKeys(ctx context.Context, bucket string) ([]string, error) {
	var keys []string
	var token *string
	for {
		out, err := a.c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(bucket), ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, o := range out.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			return keys, nil
		}
		token = out.NextContinuationToken
	}
}

func (a *awsS3Snapshotter) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	out, err := a.c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (a *awsS3Snapshotter) Put(ctx context.Context, bucket, key string, body []byte) error {
	_, err := a.c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body),
	})
	return err
}

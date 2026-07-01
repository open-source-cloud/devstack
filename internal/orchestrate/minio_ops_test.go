package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/resource"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/workspace"
	"github.com/open-source-cloud/devstack/templates"
)

// stubS3 embeds the S3API interface (nil) so the type satisfies it; only the
// methods the tests exercise are overridden. Others panic if unexpectedly called.
type stubS3 struct {
	resource.S3API
	buckets   map[string]bool
	created   []string
	lifecycle map[string][]s3types.LifecycleRule
}

func (s *stubS3) HeadBucket(_ context.Context, in *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	if s.buckets[aws.ToString(in.Bucket)] {
		return &s3.HeadBucketOutput{}, nil
	}
	return nil, &s3types.NotFound{}
}

func (s *stubS3) CreateBucket(_ context.Context, in *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	s.created = append(s.created, aws.ToString(in.Bucket))
	s.buckets[aws.ToString(in.Bucket)] = true
	return &s3.CreateBucketOutput{}, nil
}

func (s *stubS3) PutBucketLifecycleConfiguration(_ context.Context, in *s3.PutBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error) {
	if s.lifecycle == nil {
		s.lifecycle = map[string][]s3types.LifecycleRule{}
	}
	s.lifecycle[aws.ToString(in.Bucket)] = in.LifecycleConfiguration.Rules
	return &s3.PutBucketLifecycleConfigurationOutput{}, nil
}

func minioFixture(t *testing.T) (UpDeps, *fakeRunner, *state.DB, *stubS3) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("workspace.yaml", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\nshared:\n  minio: { template: minio }\nprojects:\n  - { name: web, path: web }\n")
	write("web/devstack.yaml", `apiVersion: devstack/v1
kind: Project
name: web
services:
  app:
    template: node.vite
    uses: [workspace.shared.minio]
`)
	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	db, err := state.Open(context.Background(), filepath.Join(root, "state"), "ctx")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mc := &docker.MockClient{
		Containers: []docker.Container{{
			ID: "m1", Name: "devstack-shared-minio-1", State: "running",
			Labels: map[string]string{generate.LabelManaged: "true", generate.LabelShared: "minio"},
		}},
		Details: map[string]docker.ContainerDetails{
			"m1": {ID: "m1", State: "running", Running: true, Health: docker.HealthHealthy},
		},
	}
	src := template.NewFSSource(templates.FS)
	lockPath := filepath.Join(root, "lock")
	mgr := &workspace.Manager{Model: m, DB: db, Docker: mc, Source: src, LockPath: lockPath}
	fr := &fakeRunner{}
	stub := &stubS3{buckets: map[string]bool{}}
	d := UpDeps{
		Model: m, DB: db, Docker: mc, Manager: mgr, Source: src,
		LockPath: lockPath, Runner: fr, Env: map[string]string{},
		S3Factory: func(context.Context, resource.Target) (resource.S3API, error) { return stub, nil },
	}
	return d, fr, db, stub
}

func TestCreateBucketImperative(t *testing.T) {
	d, fr, db, stub := minioFixture(t)
	attrs, err := CreateResource(context.Background(), d, resource.Resource{
		Engine: "minio", Kind: "bucket", Name: "web-uploads", Owner: "web",
		Params: map[string]any{"expire_days": 30}, CredKind: resource.CredPredictable,
	})
	if err != nil {
		t.Fatalf("CreateResource bucket: %v", err)
	}
	if !slices.Contains(stub.created, "web-uploads") {
		t.Errorf("bucket not created via S3 client: %v", stub.created)
	}
	if len(stub.lifecycle["web-uploads"]) != 1 {
		t.Errorf("expiry rule not applied: %+v", stub.lifecycle)
	}
	if attrs["bucket"] != "web-uploads" {
		t.Errorf("attrs[bucket] = %q", attrs["bucket"])
	}
	// Ledger recorded the bucket ownership row.
	rows, _ := db.ProvisionedFor("web")
	var found bool
	for _, r := range rows {
		if r.Kind == "bucket" && r.Name == "web-uploads" {
			found = true
		}
	}
	if !found {
		t.Errorf("bucket ownership row not recorded: %v", rows)
	}
	// The minio loopback overlay was applied publishing :9000 (not 5432).
	overlay := filepath.Join(d.Model.Root, generate.GenDir, "shared", "compose.provision.yaml")
	body, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatalf("overlay not written: %v", err)
	}
	if !strings.Contains(string(body), ":9000") {
		t.Errorf("minio overlay must publish container port 9000, got:\n%s", body)
	}
	if !fr.saw("-p "+generate.SharedStackName, "compose.provision.yaml") {
		t.Errorf("overlay not applied via compose up: %v", fr.cmds)
	}
}

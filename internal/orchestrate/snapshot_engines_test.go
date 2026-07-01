package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	dbpkg "github.com/open-source-cloud/devstack/internal/db"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/workspace"
	"github.com/open-source-cloud/devstack/templates"
)

// engineFixture builds UpDeps for a workspace whose single shared instance is the
// given engine (redis|minio), so the snapshot/restore engine-selection paths can
// be exercised with the mock docker client + a fake compose runner.
func engineFixture(t *testing.T, engine string) (UpDeps, *fakeRunner, *state.DB) {
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
	shared := map[string]string{
		"redis": "  redis: { template: redis, params: { version: \"7\" } }\n",
		"minio": "  minio: { template: minio, params: { rootUser: admin, rootPassword: secret } }\n",
	}[engine]
	write("workspace.yaml", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\nshared:\n"+shared+"projects:\n  - { name: app, path: app }\n")
	write("app/devstack.yaml", "apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n  web:\n    template: node.vite\n")

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
			ID: "eng1", Name: "devstack-shared-" + engine + "-1", State: "running",
			Labels: map[string]string{generate.LabelManaged: "true", generate.LabelShared: engine},
		}},
		Details: map[string]docker.ContainerDetails{
			"eng1": {ID: "eng1", State: "running", Running: true, Health: docker.HealthHealthy},
		},
	}
	src := template.NewFSSource(templates.FS)
	lockPath := filepath.Join(root, "lock")
	mgr := &workspace.Manager{Model: m, DB: db, Docker: mc, Source: src, LockPath: lockPath}
	fr := &fakeRunner{}
	d := UpDeps{
		Model: m, DB: db, Docker: mc, Manager: mgr, Source: src,
		LockPath: lockPath, Runner: fr, Env: map[string]string{},
	}
	return d, fr, db
}

// redisRunner records argv and materializes the RDB file on `redis-cli --rdb`.
type redisRunner struct {
	cmds [][]string
	envs [][]string
}

func (r *redisRunner) Run(_ context.Context, env []string, _, name string, args ...string) error {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	r.envs = append(r.envs, env)
	if name == "redis-cli" {
		for i, a := range args {
			if a == "--rdb" && i+1 < len(args) {
				_ = os.WriteFile(args[i+1], []byte("REDIS0011-fake-rdb"), 0o644)
			}
		}
	}
	return nil
}
func (r *redisRunner) Output(_ context.Context, env []string, _, name string, args ...string) ([]byte, error) {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	r.envs = append(r.envs, env)
	return []byte("0\n"), nil
}
func (r *redisRunner) sawTool(tool string) []string {
	for _, c := range r.cmds {
		if c[0] == tool {
			return c
		}
	}
	return nil
}

func TestSelectDumperByEngine(t *testing.T) {
	d, _, _ := engineFixture(t, "redis")
	for _, tc := range []struct {
		kind string
		want string // type name fragment
	}{
		{"pg", "PgDumper"},
		{"postgres", "PgDumper"},
		{"redis", "RedisDumper"},
		{"minio", "MinioDumper"},
		{"s3", "MinioDumper"},
		{"", "PgDumper"},
	} {
		got, err := SelectDumper(d, tc.kind)
		if err != nil {
			t.Fatalf("SelectDumper(%q): %v", tc.kind, err)
		}
		if name := typeName(got); !strings.Contains(name, tc.want) {
			t.Errorf("SelectDumper(%q) = %s, want %s", tc.kind, name, tc.want)
		}
	}
	if _, err := SelectDumper(d, "cassandra"); err == nil {
		t.Error("SelectDumper should reject an unsupported engine")
	}
}

func typeName(v any) string { return fmt.Sprintf("%T", v) }

// orchFakeS3 is an in-memory S3Snapshotter for the minio engine-selection test.
type orchFakeS3 struct {
	objects map[string]map[string][]byte
}

func newOrchFakeS3() *orchFakeS3 {
	return &orchFakeS3{objects: map[string]map[string][]byte{}}
}
func (f *orchFakeS3) seed(bucket, key string, body []byte) {
	if f.objects[bucket] == nil {
		f.objects[bucket] = map[string][]byte{}
	}
	f.objects[bucket][key] = body
}
func (f *orchFakeS3) ListKeys(_ context.Context, bucket string) ([]string, error) {
	var keys []string
	for k := range f.objects[bucket] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}
func (f *orchFakeS3) Get(_ context.Context, bucket, key string) ([]byte, error) {
	return f.objects[bucket][key], nil
}
func (f *orchFakeS3) Put(_ context.Context, bucket, key string, body []byte) error {
	if f.objects[bucket] == nil {
		f.objects[bucket] = map[string][]byte{}
	}
	f.objects[bucket][key] = append([]byte(nil), body...)
	return nil
}

func TestRedisSnapshotRestoreByEngine(t *testing.T) {
	newSnapEnv(t)
	d, _, ledger := engineFixture(t, "redis")
	rr := &redisRunner{}
	dumper := dbpkg.RedisDumper{Runner: rr, LookPath: func(string) (string, error) { return "/usr/bin/redis-cli", nil }}

	meta, err := Snapshot(context.Background(), d, dumper, SnapshotOptions{Kind: "redis", Project: "app", Name: "r1"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if meta.Kind != "redis" {
		t.Errorf("meta.Kind = %q, want redis", meta.Kind)
	}
	if !strings.HasSuffix(meta.Path, ".rdb") {
		t.Errorf("redis dump path should end .rdb: %s", meta.Path)
	}
	rc := rr.sawTool("redis-cli")
	if rc == nil {
		t.Fatalf("redis-cli never ran: %v", rr.cmds)
	}
	joined := strings.Join(rc, " ")
	for _, want := range []string{"-h 127.0.0.1", "--rdb"} {
		if !strings.Contains(joined, want) {
			t.Errorf("redis-cli argv missing %q: %s", want, joined)
		}
	}
	// Ledger row recorded.
	rows, _ := ledger.ProvisionedFor("app")
	found := false
	for _, r := range rows {
		if r.Kind == snapshotKind && r.Name == "r1" {
			found = true
		}
	}
	if !found {
		t.Errorf("snapshot ledger row not recorded: %v", rows)
	}

	// Restore shells sh -c redis-cli --pipe.
	if _, err := Restore(context.Background(), d, dumper, RestoreOptions{Kind: "redis", Project: "app", Name: "r1"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	sh := rr.sawTool("sh")
	if sh == nil {
		t.Fatalf("restore never shelled sh -c: %v", rr.cmds)
	}
	if !strings.Contains(strings.Join(sh, " "), "--pipe") {
		t.Errorf("restore payload missing --pipe: %v", sh)
	}
}

func TestMinioSnapshotRestoreByEngine(t *testing.T) {
	newSnapEnv(t)
	d, _, ledger := engineFixture(t, "minio")

	src := newOrchFakeS3()
	src.seed("app", "obj/one", []byte("payload-1"))
	src.seed("app", "obj/two", []byte("payload-2"))
	dumper := dbpkg.MinioDumper{Factory: func(context.Context, dbpkg.ConnInfo) (dbpkg.S3Snapshotter, error) { return src, nil }}

	meta, err := Snapshot(context.Background(), d, dumper, SnapshotOptions{Kind: "minio", Project: "app", Name: "m1"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if meta.Kind != "minio" {
		t.Errorf("meta.Kind = %q, want minio", meta.Kind)
	}
	if meta.Database != "app" {
		t.Errorf("default minio tenant bucket = %q, want app", meta.Database)
	}
	if !strings.HasSuffix(meta.Path, ".tar") {
		t.Errorf("minio dump path should end .tar: %s", meta.Path)
	}
	if _, err := os.Stat(meta.Path); err != nil {
		t.Errorf("tar not written: %v", err)
	}
	rows, _ := ledger.ProvisionedFor("app")
	found := false
	for _, r := range rows {
		if r.Kind == snapshotKind && r.Name == "m1" {
			found = true
		}
	}
	if !found {
		t.Errorf("snapshot ledger row not recorded: %v", rows)
	}

	// Restore into an empty target and confirm the two objects come back.
	dst := newOrchFakeS3()
	dumper2 := dbpkg.MinioDumper{Factory: func(context.Context, dbpkg.ConnInfo) (dbpkg.S3Snapshotter, error) { return dst, nil }}
	if _, err := Restore(context.Background(), d, dumper2, RestoreOptions{Kind: "minio", Project: "app", Name: "m1"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := string(dst.objects["app"]["obj/one"]); got != "payload-1" {
		t.Errorf("restored obj/one = %q, want payload-1", got)
	}
}

package db

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// fakeS3 is an in-memory S3Snapshotter: a bucket→key→bytes map that records which
// verbs the dumper called (list/get/put), so tests assert the snapshot/restore
// flow without a live MinIO endpoint.
type fakeS3 struct {
	objects  map[string]map[string][]byte
	listed   int
	gets     int
	puts     int
	putBytes map[string][]byte // key → last put body (restore target)
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string]map[string][]byte{}, putBytes: map[string][]byte{}}
}

func (f *fakeS3) seed(bucket, key string, body []byte) {
	if f.objects[bucket] == nil {
		f.objects[bucket] = map[string][]byte{}
	}
	f.objects[bucket][key] = body
}

func (f *fakeS3) ListKeys(_ context.Context, bucket string) ([]string, error) {
	f.listed++
	var keys []string
	for k := range f.objects[bucket] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

func (f *fakeS3) Get(_ context.Context, bucket, key string) ([]byte, error) {
	f.gets++
	return f.objects[bucket][key], nil
}

func (f *fakeS3) Put(_ context.Context, bucket, key string, body []byte) error {
	f.puts++
	if f.objects[bucket] == nil {
		f.objects[bucket] = map[string][]byte{}
	}
	cp := append([]byte(nil), body...)
	f.objects[bucket][key] = cp
	f.putBytes[key] = cp
	return nil
}

func minioConn() ConnInfo {
	return ConnInfo{Host: "127.0.0.1", Port: 49000, User: "admin", Password: "secret", Database: "app-bucket"}
}

func TestMinioDumperSnapshotRestoreRoundTrip(t *testing.T) {
	src := newFakeS3()
	src.seed("app-bucket", "a/one.txt", []byte("hello"))
	src.seed("app-bucket", "b/two.bin", []byte{0x00, 0x01, 0x02, 0x03})
	src.seed("other-bucket", "leak.txt", []byte("SHOULD-NOT-APPEAR")) // tenant B, must never be read

	m := MinioDumper{Factory: func(context.Context, ConnInfo) (S3Snapshotter, error) { return src, nil }}
	out := filepath.Join(t.TempDir(), "app.tar")
	if err := m.Snapshot(context.Background(), minioConn(), out); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if src.listed == 0 || src.gets != 2 {
		t.Errorf("expected 1 list + 2 gets of the tenant bucket, got list=%d get=%d", src.listed, src.gets)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("tar not written: %v", err)
	}

	// Restore into a fresh (empty) target bucket via a new fake — the objects must
	// come back byte-identical, and only into the SAME tenant bucket.
	dst := newFakeS3()
	m2 := MinioDumper{Factory: func(context.Context, ConnInfo) (S3Snapshotter, error) { return dst, nil }}
	if err := m2.Restore(context.Background(), minioConn(), out); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if dst.puts != 2 {
		t.Errorf("expected 2 puts on restore, got %d", dst.puts)
	}
	if got := string(dst.objects["app-bucket"]["a/one.txt"]); got != "hello" {
		t.Errorf("restored a/one.txt = %q, want hello", got)
	}
	if got := dst.objects["app-bucket"]["b/two.bin"]; string(got) != string([]byte{0x00, 0x01, 0x02, 0x03}) {
		t.Errorf("restored b/two.bin = %v, want binary payload", got)
	}
	// Project B's object was never captured (tenant isolation).
	if _, ok := dst.objects["other-bucket"]; ok {
		t.Errorf("restore leaked into another tenant's bucket: %v", dst.objects)
	}
}

func TestMinioDumperSnapshotDeterministic(t *testing.T) {
	seedOne := func() *fakeS3 {
		f := newFakeS3()
		f.seed("app-bucket", "z-last", []byte("z"))
		f.seed("app-bucket", "a-first", []byte("a"))
		f.seed("app-bucket", "m-mid", []byte("m"))
		return f
	}
	m := MinioDumper{Factory: func(context.Context, ConnInfo) (S3Snapshotter, error) { return seedOne(), nil }}
	dir := t.TempDir()
	a := filepath.Join(dir, "a.tar")
	b := filepath.Join(dir, "b.tar")
	if err := m.Snapshot(context.Background(), minioConn(), a); err != nil {
		t.Fatal(err)
	}
	if err := m.Snapshot(context.Background(), minioConn(), b); err != nil {
		t.Fatal(err)
	}
	ab, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	if string(ab) != string(bb) {
		t.Errorf("snapshot archives differ for identical contents (key ordering not deterministic)")
	}
}

func TestMinioDumperIsEmpty(t *testing.T) {
	empty := newFakeS3()
	m := MinioDumper{Factory: func(context.Context, ConnInfo) (S3Snapshotter, error) { return empty, nil }}
	got, err := m.IsEmpty(context.Background(), minioConn())
	if err != nil || !got {
		t.Errorf("IsEmpty on empty bucket = %v (err %v), want true", got, err)
	}

	full := newFakeS3()
	full.seed("app-bucket", "k", []byte("v"))
	m2 := MinioDumper{Factory: func(context.Context, ConnInfo) (S3Snapshotter, error) { return full, nil }}
	got, err = m2.IsEmpty(context.Background(), minioConn())
	if err != nil || got {
		t.Errorf("IsEmpty on non-empty bucket = %v (err %v), want false", got, err)
	}
}

func TestMinioDumperPreflightAlwaysPasses(t *testing.T) {
	// Pure-Go S3 client → no external tool to probe.
	if err := (MinioDumper{}).Preflight(context.Background()); err != nil {
		t.Errorf("Preflight should always pass for the pure-Go MinIO dumper: %v", err)
	}
}

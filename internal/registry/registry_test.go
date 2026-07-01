package registry

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oras.land/oras-go/v2/content/memory"
)

// memResolver returns a TargetResolver backed by a single shared in-memory oras
// store, so push/pull round-trips with no network (spec 19 test guidance).
func memResolver() TargetResolver {
	store := memory.New()
	return func(_ context.Context, _ Reference) (Target, error) { return store, nil }
}

// writeBundle scaffolds a minimal-but-complete template bundle on disk and returns
// its directory. name becomes the bundle's top-level template directory.
func writeBundle(t *testing.T, name, version string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"template.yaml":    "schemaVersion: 1\ndescription: \"test\"\nparams:\n  version:\n    type: string\n    default: \"" + version + "\"\nservice:\n  image: alpine:[[ .params.version ]]\n",
		"build/Dockerfile": "FROM alpine:[[ .params.version ]]\n",
		"golden.yaml":      "services:\n  test: {}\n",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPushPullRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := writeBundle(t, "postgres", "16")
	c := NewWithResolver(memResolver())

	ref, err := ParseReference("oci://ghcr.io/acme/postgres:1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	desc, err := c.Push(ctx, ref, dir)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if desc.Digest == "" || desc.Name != "postgres" || desc.SchemaVersion != 1 {
		t.Fatalf("unexpected descriptor: %+v", desc)
	}

	// Pull by the resolved digest (the reproducible path) and unpack.
	pinned := refWithDigest(ref, desc.Digest)
	pulled, err := c.Pull(ctx, pinned)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pulled.Descriptor.Digest != desc.Digest {
		t.Errorf("pulled digest %s != pushed %s", pulled.Descriptor.Digest, desc.Digest)
	}

	dest := t.TempDir()
	gotName, err := UnpackBundle(pulled.Tar, dest)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if gotName != "postgres" {
		t.Errorf("unpacked name %q, want postgres", gotName)
	}
	// Round-trip fidelity: the unpacked template.yaml matches the source.
	want, _ := os.ReadFile(filepath.Join(dir, "template.yaml"))
	got, err := os.ReadFile(filepath.Join(dest, "postgres", "template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("template.yaml round-trip mismatch:\n want %q\n got  %q", want, got)
	}
	for _, f := range []string{"build/Dockerfile", "golden.yaml"} {
		if _, err := os.Stat(filepath.Join(dest, "postgres", f)); err != nil {
			t.Errorf("missing %s after round-trip: %v", f, err)
		}
	}
}

func TestPackBundleDeterministic(t *testing.T) {
	dir := writeBundle(t, "redis", "7")
	a, _, _, err := PackBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := PackBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("PackBundle is not deterministic: two packs of identical content differ")
	}

	// Same content in a different directory (different mtimes/paths) packs identically.
	dir2 := writeBundle(t, "redis", "7")
	c, _, _, err := PackBundle(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, c) {
		t.Fatal("PackBundle output depends on filesystem metadata — digest would not be content-addressed")
	}
}

func TestPullDigestMismatchRefused(t *testing.T) {
	ctx := context.Background()
	dir := writeBundle(t, "postgres", "16")
	c := NewWithResolver(memResolver())
	ref, _ := ParseReference("oci://ghcr.io/acme/postgres:1.0.0")
	if _, err := c.Push(ctx, ref, dir); err != nil {
		t.Fatal(err)
	}

	// Pin a bogus digest: the resolved manifest digest won't match → refused.
	bad := refWithDigest(ref, "sha256:"+strings.Repeat("0", 64))
	if _, err := c.Pull(ctx, bad); err == nil {
		t.Fatal("want a digest-mismatch error, got nil")
	}
}

func TestResolveDigest(t *testing.T) {
	ctx := context.Background()
	dir := writeBundle(t, "minio", "latest")
	c := NewWithResolver(memResolver())
	ref, _ := ParseReference("oci://ghcr.io/acme/minio:2.0.0")
	pushed, err := c.Push(ctx, ref, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ResolveDigest(ctx, ref)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Digest != pushed.Digest {
		t.Errorf("resolve digest %s != pushed %s", got.Digest, pushed.Digest)
	}
}

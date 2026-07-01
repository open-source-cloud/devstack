package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oras.land/oras-go/v2/content/memory"

	"github.com/open-source-cloud/devstack/internal/registry"
	"github.com/open-source-cloud/devstack/internal/store"
)

// registryHarness isolates the store home, digest cache, and lock dir, and points
// the registry-client seam at a single shared in-memory oras store so the whole
// push/add/update/diff/verify flow round-trips with no network.
func registryHarness(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("DEVSTACK_HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))

	mem := memory.New()
	resolver := func(context.Context, registry.Reference) (registry.Target, error) { return mem, nil }
	orig := newRegistryClient
	newRegistryClient = func() (*registry.Client, error) { return registry.NewWithResolver(resolver), nil }
	t.Cleanup(func() { newRegistryClient = orig })
}

// writeCLIBundle scaffolds a template bundle dir named `name` with the given image
// tag baked into template.yaml, and returns its path.
func writeCLIBundle(t *testing.T, name, image string, schemaVersion int) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "schemaVersion: " + itoa(schemaVersion) + "\ndescription: \"t\"\nservice:\n  image: " + image + "\n"
	if err := os.WriteFile(filepath.Join(dir, "template.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	return digits
}

func TestTemplateAddPinsDigest(t *testing.T) {
	registryHarness(t)
	dir := writeCLIBundle(t, "acmepg", "postgres:16", 1)

	// push then add.
	if out, err := runCmd(t, "template", "push", dir, "oci://ghcr.io/acme/acmepg:1.0.0"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	out, err := runCmd(t, "template", "add", "oci://ghcr.io/acme/acmepg:1.0.0", "--json")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	var desc registry.Descriptor
	if err := json.Unmarshal([]byte(out), &desc); err != nil {
		t.Fatalf("parse add json: %v\n%s", err, out)
	}
	if desc.Name != "acmepg" || !strings.HasPrefix(desc.Digest, "sha256:") {
		t.Fatalf("unexpected add descriptor: %+v", desc)
	}

	// The store config carries a pinned entry: the human version + the digest.
	cfg, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("store load: ok=%v err=%v", ok, err)
	}
	rt, found := cfg.Template("acmepg")
	if !found {
		t.Fatal("acmepg not registered in the store config")
	}
	if rt.Version != "1.0.0" {
		t.Errorf("recorded version = %q, want 1.0.0 (provenance)", rt.Version)
	}
	if rt.Digest != desc.Digest {
		t.Errorf("lockfile digest %q != resolved %q — generation must fetch by digest", rt.Digest, desc.Digest)
	}

	// The bundle content is cached under the digest key and resolves by name.
	cacheDir, _ := store.TemplateCacheDir(rt.Digest)
	if _, err := os.Stat(filepath.Join(cacheDir, "acmepg", "template.yaml")); err != nil {
		t.Errorf("bundle not cached under digest key: %v", err)
	}
	if src := remoteTemplateSource(); src == nil || !src.Has("acmepg") {
		t.Error("registered remote template does not resolve in the source chain")
	}
}

func TestTemplateAddRejectsNewerSchema(t *testing.T) {
	registryHarness(t)
	dir := writeCLIBundle(t, "future", "redis:7", maxTemplateSchemaVersion+1)
	if out, err := runCmd(t, "template", "push", dir, "oci://ghcr.io/acme/future:1.0.0"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	_, err := runCmd(t, "template", "add", "oci://ghcr.io/acme/future:1.0.0")
	if err == nil {
		t.Fatal("adding a bundle with a newer schemaVersion must fail with an upgrade message")
	}
	if !strings.Contains(err.Error(), "upgrade devstack") {
		t.Errorf("error should tell the user to upgrade, got: %v", err)
	}
}

func TestTemplateAddRejectsFloatingTag(t *testing.T) {
	registryHarness(t)
	if _, err := runCmd(t, "template", "add", "oci://ghcr.io/acme/x:latest"); err == nil {
		t.Fatal("add must refuse a floating :latest tag without --allow-floating")
	}
}

func TestTemplateUpdateAndDiffDetectDrift(t *testing.T) {
	registryHarness(t)

	pgV1 := writeCLIBundle(t, "acmepg", "postgres:16", 1)
	if _, err := runCmd(t, "template", "push", pgV1, "oci://ghcr.io/acme/acmepg:1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCmd(t, "template", "add", "oci://ghcr.io/acme/acmepg:1.0.0"); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := store.Load()
	rt, _ := cfg.Template("acmepg")
	oldDigest := rt.Digest

	// Re-push DIFFERENT content to the SAME tag: the pinned workspace is unaffected
	// until update, but diff detects the drift.
	pgV2 := writeCLIBundle(t, "acmepg", "postgres:17", 1)
	if _, err := runCmd(t, "template", "push", pgV2, "oci://ghcr.io/acme/acmepg:1.0.0"); err != nil {
		t.Fatal(err)
	}

	diffOut, err := runCmd(t, "template", "diff", "acmepg", "--json")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, diffOut)
	}
	var d struct {
		DigestDrift bool `json:"digestDrift"`
		RenderDrift bool `json:"renderDrift"`
	}
	if err := json.Unmarshal([]byte(diffOut), &d); err != nil {
		t.Fatalf("parse diff json: %v\n%s", err, diffOut)
	}
	if !d.DigestDrift || !d.RenderDrift {
		t.Fatalf("diff should report digest + render drift, got %+v", d)
	}

	// dry-run update reports the move but does NOT rewrite the lockfile.
	if _, err := runCmd(t, "template", "update", "acmepg", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ = store.Load()
	rt, _ = cfg.Template("acmepg")
	if rt.Digest != oldDigest {
		t.Error("--dry-run must not rewrite the pinned digest")
	}

	// A real update rewrites the pin to the new digest.
	if _, err := runCmd(t, "template", "update", "acmepg"); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ = store.Load()
	rt, _ = cfg.Template("acmepg")
	if rt.Digest == oldDigest {
		t.Error("update must move the pinned digest to the re-pushed content")
	}

	// After update, diff is clean again.
	if out, _ := runCmd(t, "template", "diff", "acmepg"); !strings.Contains(out, "up to date") {
		t.Errorf("diff after update should be up to date, got: %s", out)
	}
}

func TestTemplateVerifySignaturePolicy(t *testing.T) {
	registryHarness(t)
	dir := writeCLIBundle(t, "acmepg", "postgres:16", 1)
	if _, err := runCmd(t, "template", "push", dir, "oci://ghcr.io/acme/acmepg:1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCmd(t, "template", "add", "oci://ghcr.io/acme/acmepg:1.0.0"); err != nil {
		t.Fatal(err)
	}

	// Inject a fake verifier accepting the signature: verify with a keyless policy passes.
	origV := registry.DefaultVerifier
	registry.DefaultVerifier = &acceptVerifier{}
	t.Cleanup(func() { registry.DefaultVerifier = origV })
	if out, err := runCmd(t, "template", "verify", "acmepg", "--identity", "^https://github.com/acme/.+$", "--issuer", "https://token.actions.githubusercontent.com"); err != nil {
		t.Fatalf("verify with good signature should pass: %v\n%s", err, out)
	}

	// A rejecting verifier fails verify.
	registry.DefaultVerifier = &rejectVerifier{}
	if _, err := runCmd(t, "template", "verify", "acmepg", "--identity", "^https://github.com/acme/.+$", "--issuer", "https://token.actions.githubusercontent.com"); err == nil {
		t.Fatal("verify with a bad signature must fail")
	}

	// Without a policy, verify only re-checks the digest pin (no cosign needed).
	if out, err := runCmd(t, "template", "verify", "acmepg"); err != nil {
		t.Fatalf("digest-only verify should pass without cosign: %v\n%s", err, out)
	}
}

// acceptVerifier / rejectVerifier are fake registry.Verifiers for CLI tests.
type acceptVerifier struct{}

func (acceptVerifier) Available() bool { return true }
func (acceptVerifier) Verify(context.Context, string, registry.VerifyPolicy) error {
	return nil
}

type rejectVerifier struct{}

func (rejectVerifier) Available() bool { return true }
func (rejectVerifier) Verify(context.Context, string, registry.VerifyPolicy) error {
	return errBadSig
}

var errBadSig = &cliError{"signature mismatch"}

type cliError struct{ s string }

func (e *cliError) Error() string { return e.s }

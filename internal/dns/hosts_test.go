package dns

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tmpHosts(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const baseHosts = "127.0.0.1 localhost\n::1 localhost\n"

func TestApplyInsertsFencedBlock(t *testing.T) {
	p := tmpHosts(t, baseHosts)
	changed, err := Apply(p, []string{"api.shop.localhost", "web.shop.localhost"})
	if err != nil || !changed {
		t.Fatalf("Apply = %v, %v; want changed", changed, err)
	}
	got := read(t, p)
	// Original content preserved.
	if !strings.HasPrefix(got, baseHosts) {
		t.Errorf("original hosts content was disturbed:\n%s", got)
	}
	// Block present with both entries → 127.0.0.1.
	for _, h := range []string{"api.shop.localhost", "web.shop.localhost"} {
		if !strings.Contains(got, "127.0.0.1\t"+h) {
			t.Errorf("missing entry for %s:\n%s", h, got)
		}
	}
	if !strings.Contains(got, markerBegin) || !strings.Contains(got, markerEnd) {
		t.Errorf("fence markers missing:\n%s", got)
	}
}

func TestApplyIdempotent(t *testing.T) {
	p := tmpHosts(t, baseHosts)
	hosts := []string{"api.shop.localhost"}
	if _, err := Apply(p, hosts); err != nil {
		t.Fatal(err)
	}
	first := read(t, p)
	changed, err := Apply(p, hosts)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("re-applying the same hosts should report no change")
	}
	if read(t, p) != first {
		t.Error("idempotent Apply must not modify the file")
	}
}

func TestApplyReplacesBlockNotAppend(t *testing.T) {
	p := tmpHosts(t, baseHosts)
	if _, err := Apply(p, []string{"old.localhost"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, []string{"new.localhost"}); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if strings.Contains(got, "old.localhost") {
		t.Errorf("stale entry not replaced:\n%s", got)
	}
	if !strings.Contains(got, "new.localhost") {
		t.Errorf("new entry missing:\n%s", got)
	}
	if strings.Count(got, markerBegin) != 1 {
		t.Errorf("must keep exactly one fenced block:\n%s", got)
	}
}

func TestRemoveStripsBlockOnly(t *testing.T) {
	p := tmpHosts(t, baseHosts)
	if _, err := Apply(p, []string{"api.shop.localhost"}); err != nil {
		t.Fatal(err)
	}
	changed, err := Remove(p)
	if err != nil || !changed {
		t.Fatalf("Remove = %v, %v; want changed", changed, err)
	}
	got := read(t, p)
	if strings.Contains(got, markerBegin) || strings.Contains(got, "api.shop.localhost") {
		t.Errorf("block not removed:\n%s", got)
	}
	if got != baseHosts {
		t.Errorf("Remove should restore the original content exactly, got:\n%q", got)
	}
	// Removing again is a no-op.
	if changed, _ := Remove(p); changed {
		t.Error("second Remove should be a no-op")
	}
}

func TestApplyEmptyRemoves(t *testing.T) {
	p := tmpHosts(t, baseHosts)
	if _, err := Apply(p, []string{"api.localhost"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, p) != baseHosts {
		t.Errorf("Apply(nil) should remove the block, got:\n%q", read(t, p))
	}
}

func TestPresentAndMissing(t *testing.T) {
	p := tmpHosts(t, baseHosts)
	if _, err := Apply(p, []string{"a.localhost", "b.localhost"}); err != nil {
		t.Fatal(err)
	}
	present, err := Present(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 2 {
		t.Errorf("Present = %v, want 2", present)
	}
	missing, err := Missing(p, []string{"a.localhost", "c.localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "c.localhost" {
		t.Errorf("Missing = %v, want [c.localhost]", missing)
	}
}

func TestApplyCreatesFileWhenAbsent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "newhosts")
	// Apply on a non-existent file currently errors on read; ensure a graceful
	// path: Remove on absent is a no-op, Apply on absent should create it.
	changed, err := Apply(p, []string{"x.localhost"})
	if err != nil {
		// Acceptable only if it's a not-exist that we then surface; assert create.
		t.Fatalf("Apply on absent file: %v", err)
	}
	if !changed || !strings.Contains(read(t, p), "x.localhost") {
		t.Errorf("Apply should create the file with the block")
	}
}

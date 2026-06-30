package scaffold

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteBundleAtomicAndLayout(t *testing.T) {
	dir := t.TempDir()
	b, _ := Build(appSpec())
	written, err := WriteBundle(dir, "node.bun", b, false)
	if err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	if len(written) != len(b) {
		t.Fatalf("wrote %d files, bundle has %d", len(written), len(b))
	}
	// Files land under <dir>/<name>/ at mode 0644 with the build/ subtree intact.
	df := filepath.Join(dir, "node.bun", "build", "Dockerfile")
	info, err := os.Stat(df)
	if err != nil {
		t.Fatalf("stat Dockerfile: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("Dockerfile mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestWriteBundleNoClobber(t *testing.T) {
	dir := t.TempDir()
	b, _ := Build(appSpec())
	if _, err := WriteBundle(dir, "node.bun", b, false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := WriteBundle(dir, "node.bun", b, false); err == nil {
		t.Fatal("second write without --force should refuse to clobber")
	}
}

func TestWriteBundleForceBacksUp(t *testing.T) {
	dir := t.TempDir()
	b, _ := Build(appSpec())
	if _, err := WriteBundle(dir, "node.bun", b, false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := WriteBundle(dir, "node.bun", b, true); err != nil {
		t.Fatalf("force write: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	var backups int
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > len("node.bun.bak.") && e.Name()[:len("node.bun.bak.")] == "node.bun.bak." {
			backups++
		}
	}
	if backups != 1 {
		t.Errorf("expected exactly one backup dir, found %d", backups)
	}
}

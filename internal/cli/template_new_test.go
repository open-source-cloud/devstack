package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readDirBytes reads every file under root into a relpath→bytes map for byte
// comparison.
func readDirBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// TestTemplateNewFromRoundTrip asserts `--print-spec` then `--from` yields a
// byte-identical bundle to the equivalent flag invocation.
func TestTemplateNewFromRoundTrip(t *testing.T) {
	t.Setenv("DEVSTACK_HOME", t.TempDir())
	flagArgs := []string{
		"--kind", "app", "--name", "node.bun", "--base-image", "oven/bun:1",
		"--param", "bunVersion:string:1", "--param", "port:int:5173", "--entrypoint",
	}

	specOut, err := runCmd(t, append([]string{"template", "new", "--print-spec"}, flagArgs...)...)
	if err != nil {
		t.Fatalf("print-spec: %v\n%s", err, specOut)
	}
	specFile := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(specFile, []byte(specOut), 0o644); err != nil {
		t.Fatal(err)
	}

	d1 := t.TempDir()
	if out, err := runCmd(t, append([]string{"template", "new", "--no-input", "--dir", d1}, flagArgs...)...); err != nil {
		t.Fatalf("flag path: %v\n%s", err, out)
	}
	d2 := t.TempDir()
	if out, err := runCmd(t, "template", "new", "--from", specFile, "--json", "--dir", d2); err != nil {
		t.Fatalf("from path: %v\n%s", err, out)
	}

	a := readDirBytes(t, filepath.Join(d1, "node.bun"))
	b := readDirBytes(t, filepath.Join(d2, "node.bun"))
	if len(a) != len(b) {
		t.Fatalf("file sets differ: %v vs %v", keysOf(a), keysOf(b))
	}
	for k, av := range a {
		if av != b[k] {
			t.Errorf("file %q differs between flag and --from path", k)
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestTemplateNewNonTTYNeverPrompts asserts that with no TTY / --json / --no-input
// and no name, `template new` returns a clear error instead of entering bubbletea
// (the test would hang if it tried to prompt).
func TestTemplateNewNonTTYNeverPrompts(t *testing.T) {
	out, err := runCmd(t, "template", "new", "--json")
	if err == nil {
		t.Fatalf("expected a no-TTY error, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "no TTY") && !strings.Contains(err.Error(), "--kind") {
		t.Errorf("error should mention the no-TTY fallback, got: %v", err)
	}

	// --no-input with --kind but no name also fails fast, never prompts.
	if _, err := runCmd(t, "template", "new", "--no-input", "--kind", "app"); err == nil {
		t.Error("expected an error requiring --name on the non-interactive path")
	}
}

// TestTemplateNewAuthoredAppPassesLintAndTest asserts an authored app bundle passes
// `template lint` and `template test` immediately (golden matches rendered output).
func TestTemplateNewAuthoredAppPassesLintAndTest(t *testing.T) {
	t.Setenv("DEVSTACK_HOME", t.TempDir())
	dir := t.TempDir()
	if out, err := runCmd(t, "template", "new", "--no-input", "--dir", dir,
		"--kind", "app", "--name", "node.bun", "--base-image", "oven/bun:1",
		"--param", "bunVersion:string:1", "--entrypoint"); err != nil {
		t.Fatalf("author: %v\n%s", err, out)
	}
	tdir := filepath.Join(dir, "node.bun")
	if _, err := os.Stat(filepath.Join(tdir, "golden.yaml")); err != nil {
		t.Fatalf("expected golden.yaml: %v", err)
	}
	if out, err := runCmd(t, "template", "lint", tdir); err != nil {
		t.Fatalf("lint authored: %v\n%s", err, out)
	}
	if out, err := runCmd(t, "template", "test", tdir); err != nil {
		t.Fatalf("test authored: %v\n%s", err, out)
	}
}

// TestTemplateNewEngineHasNoBuildTree asserts --kind engine emits image:/provides
// and no build/ tree.
func TestTemplateNewEngineHasNoBuildTree(t *testing.T) {
	t.Setenv("DEVSTACK_HOME", t.TempDir())
	dir := t.TempDir()
	if out, err := runCmd(t, "template", "new", "--no-input", "--dir", dir,
		"--kind", "engine", "--name", "mariadb", "--base-image", "mariadb:11",
		"--provides", "mariadb", "--exports", "host,port,user", "--port", "3306"); err != nil {
		t.Fatalf("author engine: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "mariadb", "build")); !os.IsNotExist(err) {
		t.Errorf("engine template must have no build/ tree, stat err = %v", err)
	}
	manifest, _ := os.ReadFile(filepath.Join(dir, "mariadb", "template.yaml"))
	for _, want := range []string{"image: mariadb:11", "provides: mariadb"} {
		if !strings.Contains(string(manifest), want) {
			t.Errorf("engine template.yaml missing %q:\n%s", want, manifest)
		}
	}
}

// TestTemplateNewRejectsBadName mirrors source.validRef exactly.
func TestTemplateNewRejectsBadName(t *testing.T) {
	t.Setenv("DEVSTACK_HOME", t.TempDir())
	for _, bad := range []string{"my/app", "..", "/abs"} {
		if _, err := runCmd(t, "template", "new", "--no-input", "--dir", t.TempDir(),
			"--kind", "app", "--name", bad, "--base-image", "x"); err == nil {
			t.Errorf("name %q should be rejected", bad)
		}
	}
}

// TestTemplateInitUnchanged asserts `template init` still writes only template.yaml
// + build/Dockerfile and NO golden.yaml (spec 23 keeps init exactly as it was).
func TestTemplateInitUnchanged(t *testing.T) {
	dir := t.TempDir()
	if out, err := runCmd(t, "template", "init", "myapp", "--dir", dir); err != nil {
		t.Fatalf("template init: %v\n%s", err, out)
	}
	files := readDirBytes(t, filepath.Join(dir, "myapp"))
	want := map[string]bool{"template.yaml": true, "build/Dockerfile": true}
	if len(files) != len(want) {
		t.Fatalf("template init wrote %v, want exactly %v", keysOf(files), want)
	}
	for k := range want {
		if _, ok := files[k]; !ok {
			t.Errorf("template init missing %q", k)
		}
	}
	if _, ok := files["golden.yaml"]; ok {
		t.Error("template init must NOT write golden.yaml")
	}
	// Re-init refuses to overwrite (unchanged behavior).
	if _, err := runCmd(t, "template", "init", "myapp", "--dir", dir); err == nil {
		t.Error("template init must refuse to overwrite an existing dir")
	}
}

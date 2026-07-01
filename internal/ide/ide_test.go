package ide

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
)

// fixedSchemaVersion pins the modeline URL so goldens do not drift with the build
// stamp (version.Version is "dev" during `go test`).
const fixedSchemaVersion = "1.2.3"

// newGen loads the shared two-file config fixture and returns a hermetic Generator
// with a fixed schema version.
func newGen(t *testing.T) *Generator {
	t.Helper()
	m, err := config.LoadAt(filepath.Join("..", "config", "testdata", "valid"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	return New(m, WithSchemaVersion(fixedSchemaVersion))
}

// TestGolden asserts every IDE artifact for the fixture workspace matches its
// committed golden byte-for-byte. Re-materialize after an intentional change with:
//
//	UPDATE_GOLDEN=1 go test ./internal/ide -run TestGolden
func TestGolden(t *testing.T) {
	g := newGen(t)
	arts, err := g.Build(All())
	if err != nil {
		t.Fatal(err)
	}
	update := os.Getenv("UPDATE_GOLDEN") == "1"
	for _, a := range arts {
		golden := filepath.Join("testdata", "golden", filepath.FromSlash(a.Rel))
		if update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, a.Data, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("missing golden %s (run UPDATE_GOLDEN=1): %v", golden, err)
		}
		if string(want) != string(a.Data) {
			t.Errorf("%s: artifact does not match golden\n--- got ---\n%s", a.Rel, a.Data)
		}
	}
}

// TestDeterministic — two independent builds of the same config produce byte-identical
// artifacts (spec 17 acceptance #4).
func TestDeterministic(t *testing.T) {
	a1, err := newGen(t).Build(All())
	if err != nil {
		t.Fatal(err)
	}
	a2, err := newGen(t).Build(All())
	if err != nil {
		t.Fatal(err)
	}
	if len(a1) != len(a2) {
		t.Fatalf("artifact count differs: %d vs %d", len(a1), len(a2))
	}
	for i := range a1 {
		if a1[i].Rel != a2[i].Rel {
			t.Fatalf("artifact[%d] path differs: %q vs %q", i, a1[i].Rel, a2[i].Rel)
		}
		if string(a1[i].Data) != string(a2[i].Data) {
			t.Errorf("artifact %s not byte-deterministic", a1[i].Rel)
		}
	}
}

// TestIdempotentWrite — writing artifacts to a fresh workspace copy reports every
// file as changed the first time and NOTHING the second time (spec 17 #4).
func TestIdempotentWrite(t *testing.T) {
	root := copyFixture(t)
	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatalf("load copied fixture: %v", err)
	}
	g := New(m, WithSchemaVersion(fixedSchemaVersion))
	arts, err := g.Build(All())
	if err != nil {
		t.Fatal(err)
	}

	first, err := Write(arts)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range first {
		if !r.Changed {
			t.Errorf("first write of %s reported unchanged", r.Path)
		}
	}

	second, err := Write(arts)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range second {
		if r.Changed {
			t.Errorf("second write of %s reported a spurious change", r.Path)
		}
	}
	if !UpToDate(arts) {
		t.Error("UpToDate false after a write with no config change")
	}
}

// TestTargetSelection — the --devcontainer / --vscode / --all flag paths emit only
// the requested artifact families.
func TestTargetSelection(t *testing.T) {
	g := newGen(t)
	cases := []struct {
		name    string
		targets Targets
		want    map[string]int // kind -> expected count
	}{
		{"devcontainer-only", Targets{Devcontainer: true}, map[string]int{"devcontainer": 2}},
		{"vscode-only", Targets{VSCode: true}, map[string]int{"launch": 2, "settings": 2, "code-workspace": 1}},
		{"all", All(), map[string]int{"devcontainer": 2, "launch": 2, "settings": 2, "code-workspace": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arts, err := g.Build(tc.targets)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]int{}
			for _, a := range arts {
				got[a.Kind]++
			}
			for kind, n := range tc.want {
				if got[kind] != n {
					t.Errorf("kind %q: got %d, want %d", kind, got[kind], n)
				}
			}
			// No unexpected kinds.
			for kind := range got {
				if _, ok := tc.want[kind]; !ok {
					t.Errorf("unexpected artifact kind %q emitted", kind)
				}
			}
		})
	}
}

// TestDevcontainerWiring — the devcontainer.json points at the SAME tool-owned
// project/compose/network as `devstack up`, and forwards the declared host port.
func TestDevcontainerWiring(t *testing.T) {
	g := newGen(t)
	arts, err := g.Build(Targets{Devcontainer: true})
	if err != nil {
		t.Fatal(err)
	}
	var api devcontainer
	found := false
	for _, a := range arts {
		if a.Rel == "services/api/.devcontainer/devcontainer.json" {
			if err := json.Unmarshal(a.Data, &api); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("api devcontainer not emitted")
	}
	if api.Name != "devstack-api" {
		t.Errorf("devcontainer name = %q, want devstack-api (tool-owned project)", api.Name)
	}
	if api.Service != "api" {
		t.Errorf("service = %q, want api", api.Service)
	}
	if len(api.DockerComposeFile) != 1 || api.DockerComposeFile[0] != "../.devstack/docker-compose.yaml" {
		t.Errorf("dockerComposeFile = %v, want the generated project compose", api.DockerComposeFile)
	}
	if api.ShutdownAction != "none" || api.OverrideCommand {
		t.Errorf("must not hijack entrypoint / tear down shared stack: shutdownAction=%q overrideCommand=%v", api.ShutdownAction, api.OverrideCommand)
	}
	if len(api.ForwardPorts) != 1 || api.ForwardPorts[0] != 8080 {
		t.Errorf("forwardPorts = %v, want [8080] from the declared http port", api.ForwardPorts)
	}
}

// TestServiceRenameReflows — the artifacts derive references from the name index,
// so renaming a service re-flows into the devcontainer's `service` field.
func TestServiceRenameReflows(t *testing.T) {
	m, err := config.LoadAt(filepath.Join("..", "config", "testdata", "valid"))
	if err != nil {
		t.Fatal(err)
	}
	// Rename web's sole service; primaryService falls back to the sorted first name.
	web := m.Projects["web"]
	renamed := map[string]config.Service{}
	for _, svc := range web.Services {
		renamed["frontend"] = svc
	}
	web.Services = renamed
	m.Projects["web"] = web

	got := primaryService("web", m.Projects["web"])
	if got != "frontend" {
		t.Errorf("primaryService after rename = %q, want frontend", got)
	}
}

// TestNoSecretLeak — no resolved secret value (and no containerEnv/remoteEnv sink)
// ever lands in an IDE artifact (spec 17 gotcha / ARCHITECTURE §7.5).
func TestNoSecretLeak(t *testing.T) {
	g := newGen(t)
	arts, err := g.Build(All())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range arts {
		s := string(a.Data)
		for _, forbidden := range []string{"secret://", "containerEnv", "remoteEnv"} {
			if strings.Contains(s, forbidden) {
				t.Errorf("%s must not contain %q (secret-leak / value-copy sink)", a.Rel, forbidden)
			}
		}
	}
}

// copyFixture copies the valid config fixture into a fresh temp dir so writes do not
// touch the committed testdata tree.
func copyFixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "config", "testdata", "valid")
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

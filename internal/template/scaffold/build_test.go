package scaffold

import (
	"bytes"
	"strings"
	"testing"
)

func appSpec() Spec {
	return Spec{
		Kind:        KindApp,
		Name:        "node.bun",
		Description: "Bun dev server",
		BaseImage:   "oven/bun:1",
		Params: []Param{
			{Name: "bunVersion", Type: "string", Default: "1"},
			{Name: "port", Type: "int", Default: "5173"},
		},
		Entrypoint: true,
		Golden:     true,
	}
}

func engineSpec() Spec {
	return Spec{
		Kind:        KindEngine,
		Name:        "mariadb",
		Description: "MariaDB engine",
		BaseImage:   "mariadb:11",
		Provides:    "mariadb",
		Exports:     []string{"host", "port", "user"},
		DefaultPort: 3306,
		Params:      []Param{{Name: "version", Type: "string", Default: "11"}},
		Volumes:     []string{"data"},
	}
}

// TestBuildDeterministic asserts the same Spec yields byte-identical bytes (the
// spec-23 acceptance gate: no clock/uuid/random anywhere).
func TestBuildDeterministic(t *testing.T) {
	for _, s := range []Spec{appSpec(), engineSpec()} {
		a, err := Build(s)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		b, err := Build(s)
		if err != nil {
			t.Fatalf("Build (2nd): %v", err)
		}
		if len(a) != len(b) {
			t.Fatalf("%s: file-count differs: %d vs %d", s.Name, len(a), len(b))
		}
		for k, av := range a {
			if !bytes.Equal(av, b[k]) {
				t.Errorf("%s: file %q not byte-stable", s.Name, k)
			}
		}
	}
}

// TestBuildAppBranch asserts an app emits build/Dockerfile + service.build and NO
// provides:; build args come only from string params.
func TestBuildAppBranch(t *testing.T) {
	b, err := Build(appSpec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	mustHave := []string{"template.yaml", "build/Dockerfile", "build/entrypoint.sh"}
	for _, f := range mustHave {
		if _, ok := b[f]; !ok {
			t.Errorf("app bundle missing %q", f)
		}
	}
	manifest := string(b["template.yaml"])
	if strings.Contains(manifest, "provides:") {
		t.Error("app template.yaml must not contain provides:")
	}
	if !strings.Contains(manifest, "build:") {
		t.Error("app template.yaml must contain service.build")
	}
	// Only the string param becomes a build arg; the int param does not.
	if !strings.Contains(manifest, "BUN_VERSION:") {
		t.Error("expected BUN_VERSION build arg from the string param")
	}
	if strings.Contains(manifest, "PORT:") {
		t.Error("int param must NOT become a build arg")
	}
	df := string(b["build/Dockerfile"])
	if !strings.Contains(df, "FROM oven/bun:1") {
		t.Error("Dockerfile must FROM the base image")
	}
	if !strings.Contains(df, "[[ .params.bunVersion ]]") {
		t.Error("Dockerfile must wire the string param via a [[ ]] action")
	}
}

// TestBuildEngineBranch asserts an engine emits image: + provides + volumes and NO
// build/ tree (generate.buildSharedService rejects build: on a shared service).
func TestBuildEngineBranch(t *testing.T) {
	b, err := Build(engineSpec())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for f := range b {
		if strings.HasPrefix(f, "build/") {
			t.Errorf("engine bundle must have no build/ tree, found %q", f)
		}
	}
	manifest := string(b["template.yaml"])
	for _, want := range []string{"image: mariadb:11", "provides: mariadb", "defaultPort: 3306", "volumes:"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("engine template.yaml missing %q\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "build:") {
		t.Error("engine template.yaml must not contain a build: key")
	}
}

// TestBuildMetaIsLiteral asserts no meta key carries a [[ ]] action — actions live
// only under service:/build.
func TestBuildMetaIsLiteral(t *testing.T) {
	b, _ := Build(appSpec())
	manifest := string(b["template.yaml"])
	idx := strings.Index(manifest, "service:")
	if idx < 0 {
		t.Fatal("no service: block")
	}
	meta := manifest[:idx]
	if strings.Contains(meta, "[[") || strings.Contains(meta, "]]") {
		t.Errorf("meta section must contain no template actions:\n%s", meta)
	}
}

// TestBuildValidate covers the compile-time app/engine guards.
func TestBuildValidate(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{"app needs base image", Spec{Kind: KindApp, Name: "a"}, "base-image"},
		{"app rejects provides", Spec{Kind: KindApp, Name: "a", BaseImage: "x", Provides: "p"}, "engine-only"},
		{"engine needs base image", Spec{Kind: KindEngine, Name: "e"}, "base-image"},
		{"engine rejects build tree", Spec{Kind: KindEngine, Name: "e", BaseImage: "x", Entrypoint: true}, "app-only"},
		{"unknown kind", Spec{Kind: "weird", Name: "z"}, "kind must be"},
		{"bad extra build", Spec{Kind: KindApp, Name: "a", BaseImage: "x", ExtraBuild: []string{"../escape"}}, "invalid extra build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestUpperSnake(t *testing.T) {
	cases := map[string]string{
		"bunVersion":  "BUN_VERSION",
		"phpVersion":  "PHP_VERSION",
		"php.version": "PHP_VERSION",
		"node-major":  "NODE_MAJOR",
		"already_ok":  "ALREADY_OK",
	}
	for in, want := range cases {
		if got := upperSnake(in); got != want {
			t.Errorf("upperSnake(%q) = %q, want %q", in, got, want)
		}
	}
}

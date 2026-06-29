package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHomeOverride(t *testing.T) {
	t.Setenv(HomeEnv, "/tmp/custom-devstack")
	if got := Home(); got != "/tmp/custom-devstack" {
		t.Errorf("Home() = %q, want the DEVSTACK_HOME override", got)
	}
}

func TestHomeDefaultUnderHome(t *testing.T) {
	t.Setenv(HomeEnv, "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, DirName)
	if got := Home(); got != want {
		t.Errorf("Home() = %q, want %q", got, want)
	}
}

func TestInitAndLoadRoundTrip(t *testing.T) {
	t.Setenv(HomeEnv, t.TempDir())

	if Initialized() {
		t.Fatal("a fresh home should not be initialized")
	}
	if err := EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := DefaultConfig().Save(); err != nil {
		t.Fatal(err)
	}
	if !Initialized() {
		t.Fatal("store should be initialized after Save")
	}

	// Layout dirs exist.
	for _, d := range []string{TemplatesPath(), SharedPath()} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("missing layout dir %s", d)
		}
	}

	cfg, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if cfg.Kind != "Store" || cfg.APIVersion != "devstack/v1" {
		t.Errorf("header = %s/%s", cfg.APIVersion, cfg.Kind)
	}
	for _, want := range []string{"postgres", "redis", "minio"} {
		if _, ok := cfg.Shared[want]; !ok {
			t.Errorf("default shared service %q missing", want)
		}
	}
	if cfg.Shared["postgres"].Params["version"] != "16" {
		t.Errorf("postgres version param = %v, want 16", cfg.Shared["postgres"].Params["version"])
	}
}

func TestLoadAbsentIsNotError(t *testing.T) {
	t.Setenv(HomeEnv, t.TempDir())
	cfg, ok, err := Load()
	if err != nil {
		t.Fatalf("Load of absent store should not error: %v", err)
	}
	if ok || cfg != nil {
		t.Errorf("absent store should report ok=false")
	}
}

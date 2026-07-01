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

func TestTelemetryDefaultOff(t *testing.T) {
	t.Setenv(HomeEnv, t.TempDir())
	// Never decided (no store) → OFF, no error.
	consent, err := TelemetryConsent()
	if err != nil {
		t.Fatal(err)
	}
	if consent.Enabled {
		t.Fatal("telemetry must default OFF with no store present")
	}
}

func TestTelemetryEnableDisableRoundTrip(t *testing.T) {
	t.Setenv(HomeEnv, t.TempDir())

	// Enable works even before `store init` and mints an install id + consentAt.
	on, err := SetTelemetry(true)
	if err != nil {
		t.Fatal(err)
	}
	if !on.Enabled || on.InstallID == "" || on.ConsentAt == "" {
		t.Fatalf("enable produced %+v; want enabled with install id + consentAt", on)
	}

	// Persisted to the global config.yaml.
	got, err := TelemetryConsent()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.InstallID != on.InstallID {
		t.Fatalf("persisted consent = %+v, want enabled with install id %q", got, on.InstallID)
	}

	// Disable flips it OFF and clears the correlatable install id.
	off, err := SetTelemetry(false)
	if err != nil {
		t.Fatal(err)
	}
	if off.Enabled || off.InstallID != "" {
		t.Fatalf("disable produced %+v; want disabled with no install id", off)
	}
	got, err = TelemetryConsent()
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("consent should be OFF after disable")
	}

	// Enabling shared-service defaults are preserved (we didn't clobber the store).
	cfg, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if _, ok := cfg.Shared["postgres"]; !ok {
		t.Error("enable/disable clobbered the default shared services")
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

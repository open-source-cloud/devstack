package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelemetryRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	// The stub must be gone: telemetry is now a real command group.
	for _, sub := range []string{"status", "enable", "disable", "show"} {
		c, _, err := root.Find([]string{"telemetry", sub})
		if err != nil || c.Name() != sub || c.RunE == nil {
			t.Errorf("telemetry %s not registered as a real command: %v", sub, err)
		}
	}
}

func TestTelemetryStatusDefaultOff(t *testing.T) {
	t.Setenv("DEVSTACK_HOME", filepath.Join(t.TempDir(), ".devstack"))

	out, err := runCmd(t, "telemetry", "status", "--json")
	if err != nil {
		t.Fatalf("telemetry status: %v\n%s", err, out)
	}
	var res struct {
		Enabled   bool   `json:"enabled"`
		Endpoint  string `json:"endpoint"`
		ShipEmpty bool   `json:"shipEmpty"`
		InstallID string `json:"installId"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if res.Enabled {
		t.Error("telemetry must report OFF by default")
	}
	if res.Endpoint != "" || !res.ShipEmpty {
		t.Errorf("ship-empty: endpoint=%q shipEmpty=%v", res.Endpoint, res.ShipEmpty)
	}
	if res.InstallID != "" {
		t.Errorf("a disabled install must carry no install id, got %q", res.InstallID)
	}
}

func TestTelemetryEnableDisableRoundTripCLI(t *testing.T) {
	t.Setenv("DEVSTACK_HOME", filepath.Join(t.TempDir(), ".devstack"))

	// enable
	out, err := runCmd(t, "telemetry", "enable", "--json")
	if err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	var en struct {
		Enabled   bool   `json:"enabled"`
		InstallID string `json:"installId"`
	}
	if err := json.Unmarshal([]byte(out), &en); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if !en.Enabled || en.InstallID == "" {
		t.Fatalf("enable json = %+v; want enabled with install id", en)
	}

	// status reflects it
	out, _ = runCmd(t, "telemetry", "status", "--json")
	if !strings.Contains(out, `"enabled": true`) {
		t.Errorf("status should report enabled after enable:\n%s", out)
	}

	// disable
	if out, err := runCmd(t, "telemetry", "disable", "--json"); err != nil {
		t.Fatalf("disable: %v\n%s", err, out)
	}
	out, _ = runCmd(t, "telemetry", "status", "--json")
	if !strings.Contains(out, `"enabled": false`) {
		t.Errorf("status should report disabled after disable:\n%s", out)
	}
}

// TestTelemetryShowNoPII asserts `telemetry show` prints only allowlisted fields —
// no path/secret/PII — mirroring the spec-04 no-secret-in-output guardrail.
func TestTelemetryShowNoPII(t *testing.T) {
	t.Setenv("DEVSTACK_HOME", filepath.Join(t.TempDir(), ".devstack"))

	out, err := runCmd(t, "telemetry", "show", "--json")
	if err != nil {
		t.Fatalf("telemetry show: %v\n%s", err, out)
	}
	var res struct {
		Endpoint string         `json:"endpoint"`
		Event    map[string]any `json:"event"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if res.Endpoint != "" {
		t.Errorf("ship-empty show should print an empty endpoint, got %q", res.Endpoint)
	}
	allowed := map[string]bool{
		"command": true, "flags": true, "outcome": true, "error_category": true,
		"duration_ms": true, "os": true, "arch": true, "is_wsl2": true,
		"tool_version": true, "install_id": true,
	}
	for k := range res.Event {
		if !allowed[k] {
			t.Errorf("telemetry show event carried un-allowlisted key %q", k)
		}
	}
}

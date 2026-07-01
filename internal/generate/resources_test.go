package generate

import (
	"testing"

	"github.com/goccy/go-yaml"
)

// composeService parses a stack's compose bytes and returns one service's map.
func composeService(t *testing.T, compose []byte, name string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		t.Fatalf("unmarshal compose: %v", err)
	}
	svcs, ok := doc["services"].(map[string]any)
	if !ok {
		t.Fatalf("compose has no services map")
	}
	svc, ok := svcs[name].(map[string]any)
	if !ok {
		t.Fatalf("service %q not found in compose:\n%s", name, compose)
	}
	return svc
}

// TestResourceLimitsDualWrite — a service declaring cpus + memory (via the
// memoryMB shorthand) + reservation + pids emits BOTH the deploy.resources block
// and the legacy top-level knobs, with agreeing canonical byte values (spec 18).
func TestResourceLimitsDualWrite(t *testing.T) {
	g, _ := newGen(t)
	svc := composeService(t, mustProject(t, g, "api").Compose, "api")

	// Legacy top-level knobs.
	if got := svc["cpus"]; got != 1.5 {
		t.Errorf("top-level cpus = %v, want 1.5", got)
	}
	if got := svc["mem_limit"]; got != "805306368" { // 768 MiB
		t.Errorf("mem_limit = %v, want 805306368", got)
	}
	if got := svc["pids_limit"]; got != uint64(512) && got != 512 {
		t.Errorf("pids_limit = %v (%T), want 512", got, got)
	}

	// deploy.resources block.
	deploy, ok := svc["deploy"].(map[string]any)
	if !ok {
		t.Fatalf("api service has no deploy block: %v", svc["deploy"])
	}
	res := deploy["resources"].(map[string]any)
	limits := res["limits"].(map[string]any)
	if limits["cpus"] != 1.5 {
		t.Errorf("limits.cpus = %v, want 1.5", limits["cpus"])
	}
	if limits["memory"] != "805306368" {
		t.Errorf("limits.memory = %v, want 805306368 (must agree with mem_limit)", limits["memory"])
	}
	reservations := res["reservations"].(map[string]any)
	if reservations["memory"] != "268435456" { // 256 MiB
		t.Errorf("reservations.memory = %v, want 268435456", reservations["memory"])
	}
}

// TestPlatformPassthrough — a service-declared platform: is emitted verbatim to
// the compose service (spec 18 multi-arch selector).
func TestPlatformPassthrough(t *testing.T) {
	g, _ := newGen(t)
	svc := composeService(t, mustProject(t, g, "api").Compose, "api")
	if got := svc["platform"]; got != "linux/amd64" {
		t.Errorf("platform = %v, want linux/amd64", got)
	}
}

// TestNoLimitsNoDeployBlock — a service without a resources block or memoryMB
// emits neither a deploy block nor any legacy limit knob (no spurious diff).
func TestNoLimitsNoDeployBlock(t *testing.T) {
	g, _ := newGen(t)
	svc := composeService(t, mustProject(t, g, "web").Compose, "web")
	for _, k := range []string{"deploy", "cpus", "mem_limit", "pids_limit", "platform"} {
		if _, ok := svc[k]; ok {
			t.Errorf("web service unexpectedly emitted %q: %v", k, svc[k])
		}
	}
}

// TestSharedResourceLimits — shared-stack services honor workspace.shared.<svc>.
// resources the same way project services do (spec 18).
func TestSharedResourceLimits(t *testing.T) {
	g, _ := newGen(t)
	shared, err := g.GenerateShared()
	if err != nil {
		t.Fatal(err)
	}
	pg := composeService(t, shared.Compose, "postgres")
	if pg["mem_limit"] != "1073741824" { // 1024 MiB
		t.Errorf("postgres mem_limit = %v, want 1073741824", pg["mem_limit"])
	}
	deploy := pg["deploy"].(map[string]any)
	limits := deploy["resources"].(map[string]any)["limits"].(map[string]any)
	// `cpus: 2` decodes as an integer scalar; compare numerically not by type.
	if v := scalarString(limits["cpus"]); v != "2" {
		t.Errorf("postgres limits.cpus = %v, want 2", limits["cpus"])
	}
	// redis declares no resources → no deploy block.
	redis := composeService(t, shared.Compose, "redis")
	if _, ok := redis["deploy"]; ok {
		t.Errorf("redis unexpectedly emitted a deploy block: %v", redis["deploy"])
	}
}

// TestBytesFromMB — the canonical mebibyte→bytes rendering the dual-write derives
// both spellings from (must be exact for compose-go agreement + determinism).
func TestBytesFromMB(t *testing.T) {
	cases := []struct {
		mb   int
		want string
	}{
		{768, "805306368"},
		{1024, "1073741824"},
		{256, "268435456"},
		{1, "1048576"},
	}
	for _, c := range cases {
		if got := bytesFromMB(c.mb); got != c.want {
			t.Errorf("bytesFromMB(%d) = %s, want %s", c.mb, got, c.want)
		}
	}
}

// TestApplyResourcesEmptyIsNoop — applyResources with no limits and no platform
// leaves the service map untouched (the "no spurious diff" invariant, unit level).
func TestApplyResourcesEmptyIsNoop(t *testing.T) {
	out := map[string]any{"image": "nginx:latest"}
	applyResources(out, nil, 0, "")
	if len(out) != 1 {
		t.Errorf("applyResources(empty) mutated the service map: %v", out)
	}
}

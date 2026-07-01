//go:build e2e

package e2e

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file is the command-surface release gate (owner directive): it drives the
// real devstack binary against a live daemon and asserts every data-plane command
// group WORKS end to end — the coverage that would have caught the shared-localstack
// health deadlock and the `db create` connect race before they reached main.
//
// Two tiers:
//   - CoreResources (postgres + minio + redis): db / s3 / shared expose+ports —
//     the commands a developer hits daily. Runs under DEVSTACK_E2E=1 (per-PR CI).
//   - CloudEngines (adds localstack): the localstack health-gate regression + the
//     `aws` shim. Heavy image → additionally gated on DEVSTACK_E2E_CLOUD=1 (nightly).

const wsCoreCloud = `apiVersion: devstack/v1
kind: Workspace
name: e2ecmd
shared:
  postgres: { template: postgres, params: { version: "18" } }
  minio: { template: minio }
  redis: { template: redis }
projects:
  - { name: app, path: app }
`

const projUsesAll = `apiVersion: devstack/v1
kind: Project
name: app
services:
  web:
    template: node.vite
    uses: [workspace.shared.postgres, workspace.shared.minio, workspace.shared.redis]
`

func coreCloudWorkspace() map[string]string {
	return map[string]string{"workspace.yaml": wsCoreCloud, "app/devstack.yaml": projUsesAll}
}

// requireCloudE2E additionally gates the heavy localstack tier.
func requireCloudE2E(t *testing.T) {
	t.Helper()
	requireDaemon(t)
	if os.Getenv("DEVSTACK_E2E_CLOUD") != "1" {
		t.Skip("cloud-engine e2e pulls heavy images (localstack); set DEVSTACK_E2E_CLOUD=1 to run")
	}
}

func cleanupSharedStack(t *testing.T, s *sandbox) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = s.tryRun("shared", "expose", "--off")
		_, _ = s.tryRun("down")
		dockerComposeDown("devstack-shared")
		dockerComposeDown("devstack-app")
		_ = exec.Command("docker", "network", "rm", "devstack_shared").Run()
	})
}

// TestE2E_Commands_CoreResources is the daily-driver command gate: bring up a
// postgres+minio+redis stack, then exercise db / s3 / shared expose+ports and
// assert each works (the db path is exactly the one that used to fail with the
// connect-reset race).
func TestE2E_Commands_CoreResources(t *testing.T) {
	requireDaemon(t)
	s := newSandbox(t, coreCloudWorkspace())
	cleanupSharedStack(t, s)

	// up: three shared engines health-gated (minio via `mc ready`, postgres via
	// pg_isready, redis via redis-cli ping) + app's postgres role/db provisioned.
	up := s.run(t, "up")
	if !strings.Contains(up, "[ok]") || strings.Contains(up, "[failed]") {
		t.Fatalf("up did not complete cleanly:\n%s", up)
	}
	for _, alias := range []string{"shared-postgres", "shared-minio", "shared-redis"} {
		if st := s.run(t, "shared", "status"); !strings.Contains(st, alias) {
			t.Errorf("shared status missing %s:\n%s", alias, st)
		}
	}

	// --- db (postgres, in-process pgx) --------------------------------------
	if out := s.run(t, "db", "create", "orders"); !strings.Contains(out, "app_orders") {
		t.Errorf("db create did not report app_orders:\n%s", out)
	}
	// Idempotent second create.
	s.run(t, "db", "create", "orders")
	if out := s.run(t, "db", "list", "--json"); !strings.Contains(out, "app_orders") {
		t.Errorf("db list --json missing app_orders:\n%s", out)
	}
	s.run(t, "db", "drop", "orders", "--yes")

	// --- s3 (minio, in-process aws-sdk-go-v2) -------------------------------
	if out := s.run(t, "s3", "mb", "uploads"); !strings.Contains(out, "app-uploads") {
		t.Errorf("s3 mb did not report app-uploads:\n%s", out)
	}
	if out := s.run(t, "s3", "ls"); !strings.Contains(out, "app-uploads") {
		t.Errorf("s3 ls missing app-uploads:\n%s", out)
	}
	s.run(t, "s3", "rb", "uploads", "--yes", "--force")

	// --- resource list (the generic surface) --------------------------------
	if out := s.run(t, "resource", "list", "--json"); !strings.Contains(out, "resources") && !strings.Contains(out, "[]") {
		t.Errorf("resource list --json not valid:\n%s", out)
	}

	// --- messaging degrades cleanly when the engine isn't in the workspace ---
	if out, err := s.tryRun("queue", "create", "jobs", "--engine", "nats"); err == nil {
		t.Errorf("queue create with no nats engine should fail cleanly, got:\n%s", out)
	}

	// --- shared expose + ports (GUI-client host access) ---------------------
	if out := s.run(t, "shared", "expose", "postgres"); !strings.Contains(out, "127.0.0.1") {
		t.Errorf("shared expose did not print a host address:\n%s", out)
	}
	assertExposedPortReachable(t, s, "postgres")
	s.run(t, "shared", "expose", "--off")

	s.run(t, "down")
}

// assertExposedPortReachable parses `shared ports --json`, finds the engine's
// primary port, and dials it to prove the publish is live (what DataGrip needs).
func assertExposedPortReachable(t *testing.T, s *sandbox, engine string) {
	t.Helper()
	out := s.run(t, "shared", "ports", "--json")
	var payload struct {
		Exposed []struct {
			Engine  string `json:"engine"`
			Port    int    `json:"port"`
			Primary bool   `json:"primary"`
		} `json:"exposed"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("shared ports --json invalid: %v\n%s", err, out)
	}
	var port int
	for _, p := range payload.Exposed {
		if p.Engine == engine && p.Primary {
			port = p.Port
		}
	}
	if port == 0 {
		t.Fatalf("no exposed primary port for %s:\n%s", engine, out)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("exposed %s port %s not reachable: %v", engine, addr, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// TestE2E_Commands_LocalStackHealthAndAws is the localstack health-gate
// regression: a workspace whose only shared engine is localstack must reach
// [ok] on `up` (the deadlock made this [failed] "unhealthy after 1 attempt"),
// and the `aws` shim must resolve the endpoint and list buckets.
func TestE2E_Commands_LocalStackHealthAndAws(t *testing.T) {
	requireCloudE2E(t)
	if _, err := exec.LookPath("aws"); err != nil {
		t.Skip("aws CLI not installed; the shim test needs it")
	}
	ws := map[string]string{
		"workspace.yaml": `apiVersion: devstack/v1
kind: Workspace
name: e2eaws
shared:
  localstack: { template: localstack }
projects:
  - { name: app, path: app }
`,
		"app/devstack.yaml": `apiVersion: devstack/v1
kind: Project
name: app
services:
  web:
    template: node.vite
    uses: [workspace.shared.localstack]
`,
	}
	s := newSandbox(t, ws)
	cleanupSharedStack(t, s)

	up := s.run(t, "up")
	if !strings.Contains(up, "[ok]") || strings.Contains(up, "[failed]") {
		t.Fatalf("localstack up did not go healthy (the health-gate regression):\n%s", up)
	}
	// The aws shim prepends --endpoint-url + dev creds; `s3 ls` on a fresh
	// localstack succeeds with empty output.
	if _, err := s.tryRun("aws", "--", "s3", "ls"); err != nil {
		out, _ := s.tryRun("aws", "--", "s3", "ls")
		t.Errorf("aws shim `s3 ls` failed:\n%s", out)
	}
	s.run(t, "down")
}

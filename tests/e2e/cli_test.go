//go:build e2e

package e2e

import (
	"os/exec"
	"strings"
	"testing"
)

const wsSharedPG = `apiVersion: devstack/v1
kind: Workspace
name: e2e
shared:
  postgres: { template: postgres, params: { version: "16" } }
projects:
  - { name: app, path: app }
`

const projNodeUsesPG = `apiVersion: devstack/v1
kind: Project
name: app
services:
  web:
    template: node.vite
    uses: [workspace.shared.postgres]
`

func appWorkspace() map[string]string {
	return map[string]string{"workspace.yaml": wsSharedPG, "app/devstack.yaml": projNodeUsesPG}
}

// --- functional (no daemon) -------------------------------------------------

func TestFunctional_Version(t *testing.T) {
	s := newSandbox(t, appWorkspace())
	if out := s.run(t, "version"); !strings.Contains(out, "commit") {
		t.Errorf("version output = %q, want it to mention commit", out)
	}
}

func TestFunctional_GenerateValidateTemplate(t *testing.T) {
	s := newSandbox(t, appWorkspace())

	s.run(t, "generate")
	// Shared + project compose materialized.
	for _, rel := range []string{".devstack/shared/docker-compose.yaml", "app/.devstack/docker-compose.yaml"} {
		if _, err := exec.Command("test", "-f", s.ws+"/"+rel).Output(); err != nil {
			t.Errorf("expected generated file %s", rel)
		}
	}
	// --check is idempotent right after generate.
	s.run(t, "generate", "--check")
	// config validate passes; template list shows a built-in.
	s.run(t, "config", "validate")
	if out := s.run(t, "template", "list"); !strings.Contains(out, "php.laravel.nginx") {
		t.Errorf("template list missing a built-in:\n%s", out)
	}
}

func TestFunctional_StatusAndDoctor(t *testing.T) {
	s := newSandbox(t, appWorkspace())
	if out := s.run(t, "status"); !strings.Contains(out, "SHARED") {
		t.Errorf("status output missing SHARED section:\n%s", out)
	}
	// doctor may exit non-zero when the daemon is down; we only assert the JSON
	// contract is emitted.
	out, _ := s.tryRun("doctor", "--json")
	if !strings.Contains(out, `"checks"`) {
		t.Errorf("doctor --json missing checks:\n%s", out)
	}
}

func TestFunctional_UpOutsideWorkspaceErrors(t *testing.T) {
	// A sandbox with no workspace.yaml.
	s := newSandbox(t, map[string]string{"README": "no workspace here"})
	if out, err := s.tryRun("up"); err == nil {
		t.Errorf("up outside a workspace should fail; got:\n%s", out)
	}
}

// --- daemon e2e (Docker; DEVSTACK_E2E=1) ------------------------------------

func TestE2E_UpStatusDown(t *testing.T) {
	requireDaemon(t)
	s := newSandbox(t, appWorkspace())
	t.Cleanup(func() {
		_, _ = s.tryRun("down")
		dockerComposeDown("devstack-shared")
		dockerComposeDown("devstack-app")
		_ = exec.Command("docker", "network", "rm", "devstack_shared").Run()
	})

	// First up: full saga, shared-postgres health-gated, project started.
	out := s.run(t, "up")
	if !strings.Contains(out, "[ok]") || strings.Contains(out, "[failed]") {
		t.Fatalf("up did not complete cleanly:\n%s", out)
	}

	// Status shows the shared service with a ref from app.
	st := s.run(t, "status")
	if !strings.Contains(st, "shared-postgres") {
		t.Errorf("status missing shared-postgres:\n%s", st)
	}

	// Re-run is idempotent: satisfied phases skip.
	reup := s.run(t, "up")
	if !strings.Contains(reup, "[skipped]") {
		t.Errorf("re-run should skip satisfied phases:\n%s", reup)
	}

	// down stops the project + releases its ref; shared keeps running.
	s.run(t, "down")
}

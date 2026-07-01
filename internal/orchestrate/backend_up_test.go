package orchestrate

import (
	"context"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
)

// TestBuildUpRemoteThreadsContextEnv: with a remote backend and --no-provision
// (so the host-provisioning guard doesn't fire), every compose verb — the shared
// stack up AND the project up — is pinned to the remote endpoint via DOCKER_CONTEXT,
// while the local default path adds nothing. Uses the mock client + fake runner
// (no real remote).
func TestBuildUpRemoteThreadsContextEnv(t *testing.T) {
	d, fr, db := upFixture(t)
	d.Backend = docker.Backend{Context: "prod-cluster"}
	d.NoProvision = true // host-side provisioning over remote is the flagged follow-up

	phases, err := BuildUp(d)
	if err != nil {
		t.Fatalf("BuildUp: %v", err)
	}
	saga := &Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}
	recs, err := saga.Run(context.Background(), phases)
	if err != nil {
		t.Fatalf("saga: %v\n%+v", err, recs)
	}
	if AnyFailed(recs) {
		t.Fatalf("a phase failed: %+v", recs)
	}

	// Both the shared stack up and the project (devstack-app) up ran, and each was
	// pinned to the remote endpoint via DOCKER_CONTEXT.
	if env := fr.envForUp("devstack-app"); env == nil {
		t.Fatal("expected the project stack to be brought up")
	} else if !contains(env, "DOCKER_CONTEXT=prod-cluster") {
		t.Errorf("project up env = %v, want DOCKER_CONTEXT=prod-cluster", env)
	}
	if env := fr.envForUp(generate.SharedStackName); env == nil {
		t.Fatal("expected the shared stack to be brought up")
	} else if !contains(env, "DOCKER_CONTEXT=prod-cluster") {
		t.Errorf("shared up env = %v, want DOCKER_CONTEXT=prod-cluster", env)
	}
	// No DOCKER_HOST leaked (context path only).
	for _, env := range fr.envs {
		for _, e := range env {
			if strings.HasPrefix(e, "DOCKER_HOST=") {
				t.Errorf("unexpected DOCKER_HOST entry %q for a context-selected backend", e)
			}
		}
	}
}

// TestBuildUpLocalAddsNoBackendEnv pins the default-path invariant: a local
// backend threads NO DOCKER_CONTEXT/DOCKER_HOST into compose — byte-for-byte
// unchanged from before spec 21.
func TestBuildUpLocalAddsNoBackendEnv(t *testing.T) {
	d, fr, db := upFixture(t)
	// d.Backend is the zero value (local) — set explicitly for clarity.
	d.Backend = docker.LocalBackend()

	phases, err := BuildUp(d)
	if err != nil {
		t.Fatalf("BuildUp: %v", err)
	}
	saga := &Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}
	if _, err := saga.Run(context.Background(), phases); err != nil {
		t.Fatalf("saga: %v", err)
	}
	for _, env := range fr.envs {
		for _, e := range env {
			if strings.HasPrefix(e, "DOCKER_CONTEXT=") || strings.HasPrefix(e, "DOCKER_HOST=") {
				t.Errorf("local backend leaked backend env %q", e)
			}
		}
	}
}

// TestBuildUpRemoteProvisionGuard: a remote backend with host-side provisioning
// required (a project uses shared Postgres, provisioning NOT skipped) fails the
// shared phase with a clear, actionable error rather than silently publishing a
// loopback port the laptop cannot reach (spec 21 §Network reachability).
func TestBuildUpRemoteProvisionGuard(t *testing.T) {
	d, _, db := upFixture(t)
	d.Backend = docker.Backend{Host: "ssh://dev@build-host"}
	// NoProvision stays false → the app's shared Postgres is a provision target.

	phases, err := BuildUp(d)
	if err != nil {
		t.Fatalf("BuildUp: %v", err)
	}
	saga := &Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}
	recs, err := saga.Run(context.Background(), phases)
	if err == nil {
		t.Fatal("expected the saga to fail on the remote-provision guard")
	}
	var sharedRec *Record
	for i := range recs {
		if recs[i].Phase == "shared" {
			sharedRec = &recs[i]
		}
	}
	if sharedRec == nil || sharedRec.Status != StatusFailed {
		t.Fatalf("shared phase = %+v, want failed", sharedRec)
	}
	if sharedRec.Error == nil {
		t.Fatal("failed shared phase carried no error message")
	}
	msg := *sharedRec.Error
	if !strings.Contains(msg, "not supported yet") || !strings.Contains(msg, "--no-provision") {
		t.Errorf("guard error = %q, want it to flag the follow-up and suggest --no-provision", msg)
	}
}

func contains(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}

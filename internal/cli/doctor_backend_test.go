package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/open-source-cloud/devstack/internal/docker"
)

// TestDoctorRemoteBackendProbeReachable: with a remote backend selected and a
// reachable client, doctor emits a green backend.remote check reporting the
// engine version.
func TestDoctorRemoteBackendProbeReachable(t *testing.T) {
	s := &doctorSession{
		backend: docker.Backend{Context: "prod"},
		client:  &docker.MockClient{Context: "prod", Server: "27.1.1"},
	}
	p, ok := s.remoteBackendProbe(context.Background())
	if !ok {
		t.Fatal("no backend.remote probe emitted for a remote backend")
	}
	if p.check.ID != "backend.remote" || p.check.Status != docker.StatusOK || p.check.Detail != "Engine v27.1.1" {
		t.Fatalf("backend.remote = %+v, want ok / Engine v27.1.1", p.check)
	}
	if p.check.Category != catCritical {
		t.Errorf("category = %q, want %q", p.check.Category, catCritical)
	}
}

// TestDoctorRemoteBackendProbeUnreachable: an unreachable remote endpoint yields
// a failing backend.remote check with an actionable remediation.
func TestDoctorRemoteBackendProbeUnreachable(t *testing.T) {
	s := &doctorSession{
		backend: docker.Backend{Host: "ssh://dev@box"},
		client:  &docker.MockClient{PingErr: errors.New("dial ssh: no route to host")},
	}
	p, ok := s.remoteBackendProbe(context.Background())
	if !ok {
		t.Fatal("no backend.remote probe emitted for a remote backend")
	}
	if p.check.Status != docker.StatusFail || p.check.Remediation == "" {
		t.Fatalf("backend.remote = %+v, want fail with remediation", p.check)
	}
}

// TestDoctorLocalBackendNoRemoteProbe: the default local backend adds no
// backend.remote probe (the docker-daemon preflight already covers it).
func TestDoctorLocalBackendNoRemoteProbe(t *testing.T) {
	s := &doctorSession{
		backend: docker.LocalBackend(),
		client:  &docker.MockClient{Context: "default"},
	}
	if _, ok := s.remoteBackendProbe(context.Background()); ok {
		t.Fatal("local backend emitted a backend.remote probe")
	}
}

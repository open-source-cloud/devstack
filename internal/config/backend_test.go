package config

import (
	"strings"
	"testing"
)

// minimalWS is a valid single-file workspace (no projects) with the given
// backend: block spliced in. A shared postgres keeps it non-trivial.
func minimalWS(backendBlock string) string {
	return `apiVersion: devstack/v1
kind: Workspace
name: acme
` + backendBlock + `
shared:
  postgres:
    template: postgres
    params:
      version: "16"
`
}

func loadWS(t *testing.T, backendBlock string) (*Model, error) {
	t.Helper()
	root := writeTree(t, map[string]string{"workspace.yaml": minimalWS(backendBlock)})
	return LoadAt(root)
}

func TestBackendDefaultLocal(t *testing.T) {
	m, err := loadWS(t, "")
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	if m.Workspace.Backend != nil {
		t.Errorf("Backend = %+v, want nil (default local)", m.Workspace.Backend)
	}
	if m.Workspace.Backend.IsRemote() {
		t.Error("nil Backend.IsRemote() = true, want false")
	}
}

func TestBackendContextValid(t *testing.T) {
	m, err := loadWS(t, "backend:\n  context: prod-cluster")
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	if m.Workspace.Backend == nil || m.Workspace.Backend.Context != "prod-cluster" {
		t.Fatalf("Backend = %+v, want context=prod-cluster", m.Workspace.Backend)
	}
	if !m.Workspace.Backend.IsRemote() {
		t.Error("Backend.IsRemote() = false, want true")
	}
}

func TestBackendHostValid(t *testing.T) {
	m, err := loadWS(t, "backend:\n  host: ssh://dev@build-host")
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	if m.Workspace.Backend == nil || m.Workspace.Backend.Host != "ssh://dev@build-host" {
		t.Fatalf("Backend = %+v, want host=ssh://dev@build-host", m.Workspace.Backend)
	}
}

func TestBackendHostBadScheme(t *testing.T) {
	_, err := loadWS(t, "backend:\n  host: dev@build-host")
	if err == nil {
		t.Fatal("expected validation error for a scheme-less DOCKER_HOST")
	}
	if !strings.Contains(err.Error(), "dockerhost") && !strings.Contains(err.Error(), "Host") {
		t.Errorf("error = %q, want it to mention the host field/rule", err)
	}
}

func TestBackendContextAndHostMutuallyExclusive(t *testing.T) {
	_, err := loadWS(t, "backend:\n  context: prod\n  host: ssh://dev@box")
	if err == nil {
		t.Fatal("expected error when both context and host are set")
	}
	if !strings.Contains(err.Error(), "not both") {
		t.Errorf("error = %q, want it to explain context/host are mutually exclusive", err)
	}
}

func TestBackendHostSchemes(t *testing.T) {
	for _, host := range []string{"ssh://u@h", "tcp://1.2.3.4:2376", "unix:///var/run/docker.sock"} {
		if _, err := loadWS(t, "backend:\n  host: "+host); err != nil {
			t.Errorf("host %q rejected: %v", host, err)
		}
	}
}

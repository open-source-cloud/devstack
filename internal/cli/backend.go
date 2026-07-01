package cli

import (
	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/store"
)

// backendFor resolves the Docker backend (spec 21) for a loaded workspace: WHERE
// the shared stack (and, in the all-remote topology, the project stacks) run. The
// precedence is workspace.yaml `backend:` → the machine-global store default →
// local. A local backend is the default and reproduces today's behavior verbatim.
//
// The read-only docker client and the compose CLI are then bound to the returned
// backend (Backend.NewClient / Compose.ContextEnv), and the state ledger is keyed
// by the backend's context so remote rows never bleed into local counts.
func backendFor(m *config.Model) docker.Backend {
	if m != nil && m.Workspace.Backend.IsRemote() {
		b := m.Workspace.Backend
		return docker.Backend{Context: b.Context, Host: b.Host}
	}
	// Fall back to the machine-global default (best-effort: a missing/broken store
	// simply yields the local backend).
	if cfg, ok, err := store.Load(); err == nil && ok && cfg.Backend.IsRemote() {
		return docker.Backend{Context: cfg.Backend.Context, Host: cfg.Backend.Host}
	}
	return docker.Backend{}
}

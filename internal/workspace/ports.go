package workspace

import (
	"context"
	"fmt"
	"net"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
)

// Default host-port base ranges (spec 03): app ports and DB-GUI ports live in
// separate bands so a psql/GUI port never collides with an app port.
const (
	PortBaseApp   = 13000
	PortBaseDBGUI = 15432
	portRangeSpan = 1000
)

// FreeHostPort allocates a stable host port for (owner, purpose), persisting it
// inside the lock. A port is considered free only if it is ALL of: not already
// persisted in the ledger, bindable on 127.0.0.1 (advisory), and not published
// by a live tool-managed container. The last check is essential on Docker Desktop
// (macOS/WSL2), where a host bind-test does not reflect the VM's port proxy, so
// the bind-test alone would hand out a port Docker already holds (spec 03/08).
func (m *Manager) FreeHostPort(ctx context.Context, owner, purpose string, base int) (int, error) {
	published, err := m.publishedPorts(ctx)
	if err != nil {
		return 0, err
	}
	var port int
	err = lock.WithLock(ctx, m.LockPath, func() error {
		var e error
		port, e = m.DB.AllocatePort(owner, purpose, base, base+portRangeSpan, func(p int) bool {
			return !published[p] && bindable(p)
		})
		return e
	})
	return port, err
}

// publishedPorts returns the set of host ports currently published by live
// tool-managed containers (the union half the bind-test cannot see).
func (m *Manager) publishedPorts(ctx context.Context) (map[int]bool, error) {
	cs, err := m.Docker.ListManaged(ctx, map[string]string{generate.LabelManaged: "true"})
	if err != nil {
		return nil, fmt.Errorf("enumerate published ports: %w", err)
	}
	out := map[int]bool{}
	for _, c := range cs {
		for _, p := range c.Ports {
			if p.HostPort != 0 {
				out[p.HostPort] = true
			}
		}
	}
	return out, nil
}

// bindable advisory-tests whether a TCP port can be bound on loopback. TOCTOU by
// nature — it is the lock + immediate persistence that makes allocation safe;
// this only avoids obviously-taken ports.
func bindable(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

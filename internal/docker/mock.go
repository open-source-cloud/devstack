package docker

import (
	"context"
	"fmt"
	"strings"
)

// MockClient is an in-memory Client for unit/race tests so concurrent-invocation
// logic can be exercised without a real daemon (ARCHITECTURE §7.7).
type MockClient struct {
	Context    string
	Server     string
	PingErr    error
	VersionErr error

	// Networks is the set of existing network names.
	Networks map[string]bool
	// Containers is the in-memory container list ListManaged filters over.
	Containers []Container
	// Details maps a container ID or name to the projection ContainerInspect
	// returns; a miss yields a "no such container" error like the real daemon.
	Details map[string]ContainerDetails
	// LogLines maps a container ID or name to its canned log text.
	LogLines map[string]string
	// Streams maps a container ID or name to the demuxed lines ContainerLogStream
	// emits (in order) before closing the channel — the seam for logs/dashboard
	// tests without a daemon.
	Streams map[string][]LogLine
	// Stats maps a container ID or name to its canned resource-usage sample the
	// dashboard's stats fetch reads — the seam for CPU/mem tests without a daemon.
	Stats map[string]Stats
	// StatsCalls counts ContainerStats invocations so a test can assert the
	// --no-stats path skips the fetch entirely.
	StatsCalls int
	// NetworkErr / ListErr / InspectErr / LogsErr force the op to fail.
	NetworkErr error
	ListErr    error
	InspectErr error
	LogsErr    error
	// StreamErr forces ContainerLogStream to fail (e.g. the unreadable-driver case).
	StreamErr error
	// StatsErr forces ContainerStats to fail.
	StatsErr error
}

var _ Client = (*MockClient)(nil)

func (m *MockClient) Ping(context.Context) error { return m.PingErr }

func (m *MockClient) ServerVersion(context.Context) (string, error) {
	if m.VersionErr != nil {
		return "", m.VersionErr
	}
	if m.Server == "" {
		return "0.0.0-mock", nil
	}
	return m.Server, nil
}

func (m *MockClient) ContextName() string {
	if m.Context == "" {
		return "mock"
	}
	return m.Context
}

func (m *MockClient) EnsureNetwork(_ context.Context, name string, _ map[string]string) error {
	if m.NetworkErr != nil {
		return m.NetworkErr
	}
	if m.Networks == nil {
		m.Networks = map[string]bool{}
	}
	m.Networks[name] = true
	return nil
}

func (m *MockClient) NetworkExists(_ context.Context, name string) (bool, error) {
	if m.NetworkErr != nil {
		return false, m.NetworkErr
	}
	return m.Networks[name], nil
}

// ListManaged returns the seeded containers whose labels are a superset of the
// requested labels (compose one-offs excluded), mirroring the real filter.
func (m *MockClient) ListManaged(_ context.Context, labels map[string]string) ([]Container, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	var out []Container
	for _, c := range m.Containers {
		if c.Labels["com.docker.compose.oneoff"] == "True" {
			continue
		}
		match := true
		for k, v := range labels {
			if c.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, c)
		}
	}
	return out, nil
}

// ContainerInspect returns the seeded details for id (by ID or name), or a
// not-found error mirroring the daemon when unseeded.
func (m *MockClient) ContainerInspect(_ context.Context, id string) (ContainerDetails, error) {
	if m.InspectErr != nil {
		return ContainerDetails{}, m.InspectErr
	}
	if d, ok := m.Details[id]; ok {
		return d, nil
	}
	return ContainerDetails{}, fmt.Errorf("no such container: %s", id)
}

// ContainerLogs returns the seeded log text for id, tail-trimmed to the last
// `tail` lines (tail<=0 means all) to mirror the daemon's Tail behaviour.
func (m *MockClient) ContainerLogs(_ context.Context, id string, tail int) (string, error) {
	if m.LogsErr != nil {
		return "", m.LogsErr
	}
	out := m.LogLines[id]
	if tail > 0 {
		out = lastLines(out, tail)
	}
	return out, nil
}

// ContainerLogStream emits the seeded lines for id then closes the channel,
// honoring ctx cancellation so follow-mode teardown is testable without a daemon.
func (m *MockClient) ContainerLogStream(ctx context.Context, id string, _ LogOptions) (<-chan LogLine, error) {
	if m.StreamErr != nil {
		return nil, m.StreamErr
	}
	lines := m.Streams[id]
	out := make(chan LogLine, len(lines)+1)
	go func() {
		defer close(out)
		for _, ll := range lines {
			select {
			case out <- ll:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// ContainerStats returns the seeded sample for id (by ID or name) and records
// the call, so tests can both feed the model and assert the fetch was (or was
// not) issued. An unseeded id yields a zero Stats value, not an error.
func (m *MockClient) ContainerStats(_ context.Context, id string) (Stats, error) {
	m.StatsCalls++
	if m.StatsErr != nil {
		return Stats{}, m.StatsErr
	}
	return m.Stats[id], nil
}

// lastLines returns the final n lines of s, preserving a trailing newline.
func lastLines(s string, n int) string {
	if s == "" || n <= 0 {
		return ""
	}
	trimmed := strings.TrimSuffix(s, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= n {
		return s
	}
	out := strings.Join(lines[len(lines)-n:], "\n")
	if strings.HasSuffix(s, "\n") {
		out += "\n"
	}
	return out
}

func (m *MockClient) Close() error { return nil }

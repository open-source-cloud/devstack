package docker

import "context"

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
	// NetworkErr / ListErr force the corresponding op to fail.
	NetworkErr error
	ListErr    error
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

func (m *MockClient) Close() error { return nil }

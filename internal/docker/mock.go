package docker

import "context"

// MockClient is an in-memory Client for unit/race tests so concurrent-invocation
// logic can be exercised without a real daemon (ARCHITECTURE §7.7).
type MockClient struct {
	Context    string
	Server     string
	PingErr    error
	VersionErr error
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

func (m *MockClient) Close() error { return nil }

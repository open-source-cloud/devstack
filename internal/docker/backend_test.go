package docker

import (
	"context"
	"errors"
	"testing"
)

func TestBackendIsRemoteAndReachability(t *testing.T) {
	tests := []struct {
		name    string
		b       Backend
		remote  bool
		reach   Reachability
		compose []string
	}{
		{"local zero value", Backend{}, false, HostRoutable, nil},
		{"remote context", Backend{Context: "prod"}, true, ViaProxy, []string{"DOCKER_CONTEXT=prod"}},
		{"remote host", Backend{Host: "ssh://dev@box"}, true, ViaProxy, []string{"DOCKER_HOST=ssh://dev@box"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.b.IsRemote(); got != tt.remote {
				t.Errorf("IsRemote() = %v, want %v", got, tt.remote)
			}
			if got := tt.b.Reachability(); got != tt.reach {
				t.Errorf("Reachability() = %v, want %v", got, tt.reach)
			}
			got := tt.b.ComposeEnv()
			if len(got) != len(tt.compose) {
				t.Fatalf("ComposeEnv() = %v, want %v", got, tt.compose)
			}
			for i := range got {
				if got[i] != tt.compose[i] {
					t.Errorf("ComposeEnv()[%d] = %q, want %q", i, got[i], tt.compose[i])
				}
			}
		})
	}
}

// TestLocalBackendComposeEnvEmpty pins the default-path invariant: the local
// backend adds NOTHING to the compose environment, so local behavior is
// byte-for-byte unchanged.
func TestLocalBackendComposeEnvEmpty(t *testing.T) {
	if env := LocalBackend().ComposeEnv(); env != nil {
		t.Fatalf("local ComposeEnv() = %v, want nil", env)
	}
	if LocalBackend().IsRemote() {
		t.Fatal("LocalBackend().IsRemote() = true, want false")
	}
}

func TestBackendString(t *testing.T) {
	tests := []struct {
		b    Backend
		want string
	}{
		{Backend{}, "local"},
		{Backend{Context: "prod"}, "remote context prod"},
		{Backend{Host: "ssh://dev@box"}, "remote host ssh://dev@box"},
	}
	for _, tt := range tests {
		if got := tt.b.String(); got != tt.want {
			t.Errorf("Backend%+v.String() = %q, want %q", tt.b, got, tt.want)
		}
	}
}

// TestComposeContextEnvMerge verifies the ContextEnv threads through to the
// runner AFTER the secret Env, so a remote endpoint pins compose without
// clobbering resolved-secret values — using a fake runner (no real docker).
func TestComposeContextEnvMerge(t *testing.T) {
	fr := &fakeEnvRunner{}
	cp := Compose{
		Project:    "devstack-app",
		File:       "compose.yaml",
		Dir:        "/tmp/x",
		Env:        []string{"SECRET=shh"},
		ContextEnv: Backend{Context: "prod"}.ComposeEnv(),
		Runner:     fr,
	}
	if err := cp.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	want := []string{"SECRET=shh", "DOCKER_CONTEXT=prod"}
	if len(fr.env) != len(want) {
		t.Fatalf("runner env = %v, want %v", fr.env, want)
	}
	for i := range want {
		if fr.env[i] != want[i] {
			t.Errorf("env[%d] = %q, want %q", i, fr.env[i], want[i])
		}
	}
	// The docker CLI args are unchanged by the backend selection (endpoint is env,
	// not an arg) — the up verb is still `compose -p ... -f ... up -d`.
	if len(fr.args) == 0 || fr.args[0] != "compose" {
		t.Errorf("args = %v, want to start with compose", fr.args)
	}
}

// TestComposeLocalEnvUnchanged pins that a local Compose (no ContextEnv) passes
// exactly its secret Env — the default path is untouched.
func TestComposeLocalEnvUnchanged(t *testing.T) {
	fr := &fakeEnvRunner{}
	cp := Compose{Project: "p", File: "f", Env: []string{"SECRET=shh"}, Runner: fr}
	if err := cp.Down(context.Background(), false); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(fr.env) != 1 || fr.env[0] != "SECRET=shh" {
		t.Fatalf("runner env = %v, want [SECRET=shh]", fr.env)
	}
}

func TestRemoteReachable(t *testing.T) {
	ctx := context.Background()

	// Local backend → empty check (nothing remote to probe).
	if c := LocalBackend().RemoteReachable(ctx, &MockClient{}); c.Name != "" {
		t.Errorf("local RemoteReachable Name = %q, want empty", c.Name)
	}

	b := Backend{Context: "prod"}

	// Nil client → fail (construction already failed upstream).
	if c := b.RemoteReachable(ctx, nil); c.Status != StatusFail || c.ID != "backend.remote" {
		t.Errorf("nil-client check = %+v, want fail/backend.remote", c)
	}

	// Ping error → fail with the error detail and a remediation.
	pingErr := &MockClient{PingErr: errors.New("dial tcp: connection refused")}
	if c := b.RemoteReachable(ctx, pingErr); c.Status != StatusFail || c.Remediation == "" {
		t.Errorf("ping-error check = %+v, want fail with remediation", c)
	}

	// Reachable → ok, reporting the engine version.
	ok := &MockClient{Server: "27.1.1"}
	c := b.RemoteReachable(ctx, ok)
	if c.Status != StatusOK {
		t.Fatalf("reachable check = %+v, want ok", c)
	}
	if c.Detail != "Engine v27.1.1" {
		t.Errorf("detail = %q, want Engine v27.1.1", c.Detail)
	}
}

// fakeEnvRunner records the env + args it was called with (no real exec).
type fakeEnvRunner struct {
	env  []string
	args []string
}

func (f *fakeEnvRunner) Run(_ context.Context, env []string, _ /*dir*/, _ /*name*/ string, args ...string) error {
	f.env = env
	f.args = args
	return nil
}

func (f *fakeEnvRunner) Output(_ context.Context, env []string, _, _ string, args ...string) ([]byte, error) {
	f.env = env
	f.args = args
	return nil, nil
}

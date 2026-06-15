package docker

import (
	"context"
	"os"
	"os/exec"
	"strings"

	moby "github.com/moby/moby/client"
)

// mobyClient implements the read-only Client using the Engine SDK.
type mobyClient struct {
	cli     *moby.Client
	ctxName string
}

var _ Client = (*mobyClient)(nil)

// NewClient constructs the read-only Engine SDK client from the environment
// (DOCKER_HOST / the active docker context). Construction does NOT contact the
// daemon — call Ping to verify reachability.
func NewClient(ctx context.Context) (Client, error) {
	// API-version negotiation is on by default in moby/moby/client v0.4+.
	cli, err := moby.New(moby.FromEnv)
	if err != nil {
		return nil, err
	}
	return &mobyClient{cli: cli, ctxName: resolveContextName(ctx, cli)}, nil
}

func (m *mobyClient) Ping(ctx context.Context) error {
	_, err := m.cli.Ping(ctx, moby.PingOptions{})
	return err
}

func (m *mobyClient) ServerVersion(ctx context.Context) (string, error) {
	v, err := m.cli.ServerVersion(ctx, moby.ServerVersionOptions{})
	if err != nil {
		return "", err
	}
	return v.Version, nil
}

func (m *mobyClient) ContextName() string { return m.ctxName }

func (m *mobyClient) Close() error { return m.cli.Close() }

// resolveContextName keys the state ledger by Docker context so WSL2's two
// daemons (Desktop vs in-distro dockerd) never share counts. Resolved without
// requiring the daemon to be up.
//
// Order matters: an explicit DOCKER_HOST must win over `docker context show`,
// because the docker CLI reports "default" for ANY host set via DOCKER_HOST — so
// two distinct daemons selected purely by DOCKER_HOST would otherwise collapse to
// the same key (the exact WSL2 mis-count D6/spec 08 exist to prevent).
func resolveContextName(ctx context.Context, cli *moby.Client) string {
	if c := os.Getenv("DOCKER_CONTEXT"); c != "" {
		return c
	}
	if os.Getenv("DOCKER_HOST") != "" {
		// DaemonHost() reflects the DOCKER_HOST endpoint, distinguishing daemons.
		return cli.DaemonHost()
	}
	if out, err := exec.CommandContext(ctx, "docker", "context", "show").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	if h := cli.DaemonHost(); h != "" {
		return h
	}
	return "default"
}

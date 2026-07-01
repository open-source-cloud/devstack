package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	moby "github.com/moby/moby/client"
)

// Backend selects WHERE the shared stack (and, in the all-remote topology, the
// project stacks) run — the local Docker daemon or a remote host reached over an
// SSH/TCP `docker context` or a DOCKER_HOST endpoint (spec 21). It is the seam
// that lets internal/workspace stay written against capabilities rather than a
// socket: the ref-counting, provisioning, and DNS/alias model are backend-
// agnostic; a Backend only swaps the Docker endpoint the CLI + read-only SDK
// target, and the reachability strategy for host-side consumers.
//
// The zero value is the LOCAL backend and reproduces today's behavior verbatim
// (default socket / the active docker context, host-routable published ports).
// This is the default path and MUST stay byte-for-byte unchanged.
//
// Exactly one of Context or Host is set for a remote backend:
//   - Context names a `docker context` (typically an `ssh://` one, which inherits
//     the user's ~/.ssh/config, agent, and ProxyJump for free — DECISIONS D9).
//   - Host is a raw DOCKER_HOST endpoint (ssh://, tcp://, unix://).
//
// Scope note (spec 21 is a frontier spec): this pass delivers the single-user,
// SSH-context, all-remote topology with the local flock still serializing a
// single developer's invocations. The distributed lock (two developers, two
// machines) and per-user tenant isolation on a shared cluster are the ~8w
// follow-up and are deliberately NOT implemented here (see the PR body).
type Backend struct {
	// Context is the `docker context` name to target (empty = not selected).
	Context string
	// Host is the DOCKER_HOST endpoint to target (empty = not selected).
	Host string
}

// Reachability classifies how a host-side consumer (host pgx, a psql/GUI) can
// reach the shared services, which drives the port/URL strategy (spec 21
// §Network reachability).
type Reachability string

const (
	// HostRoutable: services can publish host ports the developer's machine can
	// dial on loopback (the local backend, and Docker Desktop's port proxy).
	HostRoutable Reachability = "host-routable"
	// ViaProxy: a remote bridge network is on another host entirely and is NOT
	// host-routable — host access must go through the proxy/tunnel (an SSH local
	// -forward or cloudflared), never a local loopback published port.
	ViaProxy Reachability = "via-proxy"
)

// LocalBackend is the explicit local (default socket / active context) backend.
func LocalBackend() Backend { return Backend{} }

// IsRemote reports whether this backend targets a non-local endpoint.
func (b Backend) IsRemote() bool { return b.Context != "" || b.Host != "" }

// Reachability returns the host-reachability class for this backend. Remote
// backends are ViaProxy (the remote bridge is not host-routable); the local
// backend is HostRoutable.
func (b Backend) Reachability() Reachability {
	if b.IsRemote() {
		return ViaProxy
	}
	return HostRoutable
}

// String is a short, stable description for log/doctor output.
func (b Backend) String() string {
	switch {
	case b.Host != "":
		return "remote host " + b.Host
	case b.Context != "":
		return "remote context " + b.Context
	default:
		return "local"
	}
}

// ComposeEnv returns the extra environment entries that pin the `docker compose`
// CLI (and any `docker` invocation) to this backend's endpoint. They are appended
// to the child process env by the Runner (exec.Cmd.Env), never written to disk —
// exactly how resolved secrets are threaded (§7.5). Empty for the local backend
// so the default path stays byte-for-byte unchanged.
//
// A remote Host sets DOCKER_HOST; a remote Context sets DOCKER_CONTEXT. They are
// mutually exclusive (validated in config) so the two env vars never contend.
func (b Backend) ComposeEnv() []string {
	switch {
	case b.Host != "":
		return []string{"DOCKER_HOST=" + b.Host}
	case b.Context != "":
		return []string{"DOCKER_CONTEXT=" + b.Context}
	default:
		return nil
	}
}

// NewClient builds the READ-ONLY Engine SDK client bound to this backend's
// endpoint. The local backend defers to the package NewClient (FromEnv), so its
// context-name resolution — and therefore the ledger key — is unchanged. A remote
// backend binds moby to the resolved endpoint and keys the ledger by the context
// name (or the host endpoint) so remote rows never bleed into the local counts,
// exactly like the WSL2 Desktop-vs-dockerd split (DECISIONS D6, spec 08).
//
// Construction does NOT contact the daemon — call Ping (or the doctor reachability
// probe) to verify the endpoint is reachable. This matters more for a remote
// backend: an unreachable SSH host must fail with a clear, actionable error, not a
// crash mid-up.
func (b Backend) NewClient(ctx context.Context) (Client, error) {
	switch {
	case b.Host != "":
		cli, err := moby.New(moby.WithHost(b.Host), moby.WithAPIVersionNegotiation())
		if err != nil {
			return nil, fmt.Errorf("connect to remote docker host %q: %w", b.Host, err)
		}
		return &mobyClient{cli: cli, ctxName: b.Host}, nil
	case b.Context != "":
		endpoint, err := contextEndpoint(ctx, b.Context)
		if err != nil {
			return nil, err
		}
		cli, err := moby.New(moby.WithHost(endpoint), moby.WithAPIVersionNegotiation())
		if err != nil {
			return nil, fmt.Errorf("connect to docker context %q (%s): %w", b.Context, endpoint, err)
		}
		// Key the ledger by the context NAME (stable across endpoint edits), not the
		// resolved endpoint, so `docker context` rebinds don't fork the counts.
		return &mobyClient{cli: cli, ctxName: b.Context}, nil
	default:
		return NewClient(ctx)
	}
}

// contextEndpoint resolves a `docker context`'s Docker endpoint (e.g.
// ssh://user@host) by shelling the docker CLI, which already knows how to read
// ~/.docker/contexts. Kept behind the CLI (rather than re-parsing the context
// store) so the user's existing context definitions are the single source of
// truth. A missing/mistyped context surfaces the CLI's own error verbatim.
func contextEndpoint(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "context", "inspect", name,
		"--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return "", &CmdError{
			Cmd: "docker context inspect " + name,
			Err: err,
			Stderr: fmt.Sprintf("docker context %q not found or has no docker endpoint; "+
				"create it with `docker context create %s --docker host=ssh://user@host`", name, name),
		}
	}
	endpoint := strings.TrimSpace(string(out))
	if endpoint == "" {
		return "", fmt.Errorf("docker context %q resolves to an empty endpoint", name)
	}
	return endpoint, nil
}

// RemoteReachable probes a remote backend's endpoint and returns a doctor Check.
// For the local backend it returns a zero Check with an empty Name (callers skip
// empty-named checks) — there is nothing remote to probe. The client should be
// one built via Backend.NewClient; a nil client means construction already failed.
func (b Backend) RemoteReachable(ctx context.Context, client Client) Check {
	if !b.IsRemote() {
		return Check{}
	}
	name := "remote backend (" + b.String() + ")"
	remediation := "verify the endpoint: `docker --context " + b.Context + " info` (or DOCKER_HOST) works, " +
		"the remote daemon is running, and your SSH access (~/.ssh/config, agent) is set up"
	if b.Host != "" {
		remediation = "verify `docker -H " + b.Host + " info` works and the remote daemon is reachable"
	}
	if client == nil {
		return Check{
			Name: name, ID: "backend.remote", Category: "critical",
			Status: StatusFail, Detail: "could not construct a docker client for the remote endpoint",
			Remediation: remediation,
		}
	}
	if err := client.Ping(ctx); err != nil {
		return Check{
			Name: name, ID: "backend.remote", Category: "critical",
			Status: StatusFail, Detail: err.Error(), Remediation: remediation,
		}
	}
	detail := "reachable"
	if sv, err := client.ServerVersion(ctx); err == nil {
		detail = "Engine v" + sv
	}
	return Check{Name: name, ID: "backend.remote", Category: "critical", Status: StatusOK, Detail: detail}
}

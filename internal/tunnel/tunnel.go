// Package tunnel manages the optional public tunnel via cloudflared (spec 05).
// The tunnel is DEFAULT-DOWN and account-gated; it reuses local routing by
// pointing its ingress at the shared Caddy container, so the same proxy []Route
// drives both local and public access (no drift). A tunnel must refuse to expose
// a service whose env carries non-local secret:// values without an override.
//
// cloudflared runs through an injectable Runner so the package is fully
// unit-testable without the binary or a Cloudflare account (locked decision #3:
// build the logic, fake-runner test, flag the human/account step).
package tunnel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/open-source-cloud/devstack/internal/secrets"
)

// Runner runs the external cloudflared binary. Injectable for tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
	LookPath(file string) (string, error)
}

// Tunnel wraps cloudflared. The zero value uses the real OS exec runner.
type Tunnel struct {
	Runner Runner
}

// New returns a Tunnel backed by the real exec runner.
func New() *Tunnel { return &Tunnel{Runner: execRunner{}} }

func (t *Tunnel) runner() Runner {
	if t.Runner != nil {
		return t.Runner
	}
	return execRunner{}
}

// Available reports whether the cloudflared binary is on PATH.
func (t *Tunnel) Available() bool {
	_, err := t.runner().LookPath("cloudflared")
	return err == nil
}

// Login runs the interactive `cloudflared tunnel login` (account-gated).
func (t *Tunnel) Login(ctx context.Context) error {
	return t.exec(ctx, "tunnel", "login")
}

// Create creates a named tunnel (writes <UUID>.json creds) via
// `cloudflared tunnel create <name>` — avoids the login regression.
func (t *Tunnel) Create(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("tunnel name required")
	}
	return t.exec(ctx, "tunnel", "create", name)
}

// RouteDNS points a hostname at the tunnel. NOTE: cloudflared rejects a wildcard
// (`*.project`) — that single wildcard CNAME must be created manually in DNS
// (spec 05 gotcha); this routes concrete hostnames.
func (t *Tunnel) RouteDNS(ctx context.Context, name, hostname string) error {
	if strings.HasPrefix(hostname, "*") {
		return fmt.Errorf("cloudflared cannot route a wildcard %q — create the *.<project> CNAME manually in the Cloudflare dashboard", hostname)
	}
	return t.exec(ctx, "tunnel", "route", "dns", name, hostname)
}

// Run starts the tunnel in the foreground with a config file
// (`cloudflared tunnel --config <path> run`).
func (t *Tunnel) Run(ctx context.Context, configPath string) error {
	return t.exec(ctx, "tunnel", "--config", configPath, "run")
}

func (t *Tunnel) exec(ctx context.Context, args ...string) error {
	if !t.Available() {
		return fmt.Errorf("cloudflared not found on PATH — install it (https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) and run `devstack tunnel login`")
	}
	if err := t.runner().Run(ctx, "cloudflared", args...); err != nil {
		return fmt.Errorf("cloudflared %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// IngressConfig renders a cloudflared config.yml whose ingress maps every public
// hostname to the shared Caddy upstream (so public reuses local routing), with
// the required catch-all 404 last. Deterministic (hostnames sorted).
func IngressConfig(name, credentialsFile, caddyUpstream string, hostnames []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tunnel: %s\n", name)
	fmt.Fprintf(&b, "credentials-file: %s\n", credentialsFile)
	b.WriteString("ingress:\n")
	hosts := append([]string(nil), hostnames...)
	sort.Strings(hosts)
	for _, h := range hosts {
		fmt.Fprintf(&b, "  - hostname: %s\n    service: %s\n", h, caddyUpstream)
	}
	b.WriteString("  - service: http_status:404\n")
	return b.String()
}

// SecretBearing returns the non-local secret:// references found in envValues — a
// tunnel must refuse to expose a service carrying these (unless overridden).
// isLocalProvider classifies a provider name as local (offline, e.g. sops+age)
// vs non-local (aws/infisical); an unknown/empty classifier treats all as
// non-local (fail safe).
func SecretBearing(envValues []string, isLocalProvider func(provider string) bool) []string {
	var out []string
	for _, v := range envValues {
		if !secrets.IsRef(v) {
			continue
		}
		ref, err := secrets.ParseRef(v)
		if err != nil {
			continue
		}
		if isLocalProvider != nil && isLocalProvider(ref.Provider) {
			continue
		}
		out = append(out, ref.Raw)
	}
	sort.Strings(out)
	return out
}

// execRunner is the production Runner. tunnel verbs are interactive/long-running,
// so stdio is inherited.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
func (execRunner) LookPath(file string) (string, error) { return exec.LookPath(file) }

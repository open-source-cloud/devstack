package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/alias"
	"github.com/open-source-cloud/devstack/internal/dns"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/trust"
	"github.com/open-source-cloud/devstack/internal/xdg"
)

// composeProjectLabel is the standard Compose label devstack stacks carry; it
// groups containers/volumes into a project for label-driven teardown.
const composeProjectLabel = "com.docker.compose.project"

// newUninstallCmd wires `devstack uninstall` (spec 13): the reverse of everything
// the tool creates, machine-wide. Strict order — network/volumes before the
// ledger that records them, the CA last (the security-critical one). Gated by an
// explicit data-loss confirmation; the whole operation holds the flock.
func newUninstallCmd(g *GlobalOpts) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove EVERY machine-global devstack artifact (stacks, network, volumes, CA, hosts, aliases, ledger)",
		Long: "uninstall reverses everything devstack created on this machine:\n" +
			"  1. compose down -v every managed stack (containers + their volumes, incl. shared DB data)\n" +
			"  2. remove the external `devstack_shared` network (compose won't — devstack owns it)\n" +
			"  3. remove the local root CA from every trust store (host + Firefox/NSS + Windows)\n" +
			"  4. remove the marker-fenced /etc/hosts entries\n" +
			"  5. remove alias symlinks\n" +
			"  6. remove the ledger, template cache and config (XDG data/cache/config)\n\n" +
			"It does NOT touch your committed workspace.yaml/devstack.yaml. This DESTROYS DATA\n" +
			"(database volumes included) and is irreversible.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				if g.JSON {
					return fmt.Errorf("refusing to uninstall without confirmation: pass --yes for --json/non-interactive use")
				}
				if !confirm(cmd, "This DESTROYS all devstack data on this machine (database volumes included) and\n"+
					"removes the local CA from your trust stores. Type 'uninstall' to continue: ") {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}

			ctx := cmd.Context()
			client, err := docker.NewClient(ctx)
			if err != nil {
				return fmt.Errorf("docker client: %w", err)
			}
			defer client.Close()

			var res UninstallResult
			lockPath := lockPathForUninstall()
			err = lock.WithLock(ctx, lockPath, func() error {
				res = runUninstall(ctx, uninstallEnv{
					Client:    client,
					Runner:    docker.ExecRunner{},
					Trust:     trust.New(),
					HostsPath: dns.DefaultHostsPath,
					Dirs:      []string{xdg.DataHome(), xdg.CacheHome(), xdg.ConfigHome()},
				})
				return nil
			})
			if err != nil {
				return err
			}

			if g.JSON {
				if err := writeJSON(cmd, res); err != nil {
					return err
				}
			} else {
				renderUninstall(cmd, res)
			}
			if len(res.Warnings) > 0 {
				return fmt.Errorf("uninstall completed with %d warning(s) — see above", len(res.Warnings))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt (required for --json/non-interactive)")
	return cmd
}

// uninstallEnv holds the (injectable) collaborators so the sequencer is testable
// with mocks + temp paths.
type uninstallEnv struct {
	Client    docker.Client
	Runner    docker.Runner
	Trust     *trust.Trust
	HostsPath string
	Dirs      []string // XDG dirs to remove (data/cache/config) — devstack-namespaced
}

// UninstallResult is the machine-readable outcome.
type UninstallResult struct {
	ProjectsDown   []string `json:"projects_down"`
	NetworkRemoved bool     `json:"network_removed"`
	CACleared      bool     `json:"ca_cleared"`
	HostsCleared   bool     `json:"hosts_cleared"`
	AliasesRemoved []string `json:"aliases_removed"`
	DirsRemoved    []string `json:"dirs_removed"`
	Warnings       []string `json:"warnings,omitempty"`
}

// runUninstall executes the teardown sequence, best-effort: each step records a
// warning on failure and the rest proceed (a half-broken install must still be
// removable). It returns what it actually did.
func runUninstall(ctx context.Context, env uninstallEnv) UninstallResult {
	var res UninstallResult

	// 1. compose down -v every managed stack (containers + their named volumes,
	// including the shared Postgres/Redis/MinIO data).
	for _, project := range managedProjects(ctx, env, &res) {
		cp := docker.Compose{Project: project, Runner: env.Runner} // label-driven (no -f)
		if err := cp.Down(ctx, true); err != nil {                 // -v: this is the data-destroying step
			res.Warnings = append(res.Warnings, fmt.Sprintf("compose down %s: %v", project, err))
			continue
		}
		res.ProjectsDown = append(res.ProjectsDown, project)
	}

	// 2. remove the external network (compose refuses external networks).
	if err := env.Runner.Run(ctx, nil, "", "docker", "network", "rm", generate.SharedNetwork); err != nil {
		if isNotFound(err) {
			res.NetworkRemoved = true // already gone — the desired end state
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf("network rm %s: %v", generate.SharedNetwork, err))
		}
	} else {
		res.NetworkRemoved = true
	}

	// 3. remove the root CA from every trust store (host + Firefox/NSS + Windows).
	// The load-bearing security step — warn loudly if it can't be cleared.
	if env.Trust != nil {
		if err := env.Trust.Uninstall(ctx); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("CA NOT cleared from trust stores: %v (a CA left behind is a security risk — run `mkcert -uninstall` manually)", err))
		} else {
			res.CACleared = true
		}
	}

	// 4. remove the marker-fenced /etc/hosts entries.
	if removed, err := dns.Remove(env.HostsPath); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("/etc/hosts cleanup: %v (try `%s`)", err, sudoSelfCmd("uninstall")))
	} else {
		res.HostsCleared = removed
	}

	// 5. remove alias symlinks (and their registry entries).
	if reg, err := alias.Load(); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("alias registry: %v", err))
	} else {
		for _, name := range append([]string(nil), reg.Aliases...) {
			if err := alias.Remove(name); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("remove alias %s: %v", name, err))
				continue
			}
			res.AliasesRemoved = append(res.AliasesRemoved, name)
		}
	}

	// 6. remove the ledger, template cache and config (XDG, devstack-namespaced).
	for _, dir := range env.Dirs {
		if err := os.RemoveAll(dir); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("remove %s: %v", dir, err))
			continue
		}
		res.DirsRemoved = append(res.DirsRemoved, dir)
	}
	return res
}

// managedProjects returns the distinct Compose project names of every managed
// container (running or stopped). A daemon error is recorded as a warning.
func managedProjects(ctx context.Context, env uninstallEnv, res *UninstallResult) []string {
	cs, err := env.Client.ListManaged(ctx, map[string]string{generate.LabelManaged: "true"})
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("list managed containers: %v", err))
		return nil
	}
	seen := map[string]bool{}
	for _, c := range cs {
		if p := c.Labels[composeProjectLabel]; p != "" && !seen[p] {
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func renderUninstall(cmd *cobra.Command, res UninstallResult) {
	w := cmd.OutOrStdout()
	for _, p := range res.ProjectsDown {
		fmt.Fprintf(w, "[ok]      removed stack %s (with volumes)\n", p)
	}
	if res.NetworkRemoved {
		fmt.Fprintf(w, "[ok]      removed network %s\n", generate.SharedNetwork)
	}
	if res.CACleared {
		fmt.Fprintln(w, "[ok]      cleared the local CA from all trust stores")
	}
	if res.HostsCleared {
		fmt.Fprintln(w, "[ok]      removed /etc/hosts entries")
	}
	for _, a := range res.AliasesRemoved {
		fmt.Fprintf(w, "[ok]      removed alias %s\n", a)
	}
	for _, d := range res.DirsRemoved {
		fmt.Fprintf(w, "[ok]      removed %s\n", d)
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(w, "[warn]    %s\n", warn)
	}
	fmt.Fprintln(w, "uninstall complete.")
}

// isNotFound reports whether a docker CLI error is a benign "no such object".
func isNotFound(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "no such")
}

// lockPathForUninstall returns the flock path (same as the rest of the tool).
func lockPathForUninstall() string {
	return filepath.Join(xdg.RuntimeDir(), "devstack.lock")
}

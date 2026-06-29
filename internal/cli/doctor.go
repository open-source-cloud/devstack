package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/trust"
	"github.com/open-source-cloud/devstack/internal/xdg"
)

func newDoctorCmd(g *GlobalOpts) *cobra.Command {
	var (
		fix          bool
		rebuildState bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe the environment and report capabilities with remediations",
		Long: "doctor runs the REAL branch logic (not docs) for the tools and paths devstack\n" +
			"depends on, and prints a one-line remediation for anything that isn't OK.\n\n" +
			"With --rebuild-state, the shared_service + ref ledger is reconstructed from\n" +
			"on-disk config intersected with live container labels (recovery when state.db\n" +
			"is lost or corrupt — the ledger is a cache of reality).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rebuildState {
				return rebuildLedger(cmd, g)
			}
			checks := runDoctor(cmd)
			if g.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"checks": checks})
			}
			renderChecks(cmd, checks)
			if fix {
				fmt.Fprintln(cmd.OutOrStdout(), "\n(--fix has no automatic remediations wired yet; planned with M6 doctor)")
			}
			for _, c := range checks {
				if c.Status == docker.StatusFail {
					return fmt.Errorf("doctor found %d failing check(s)", countFails(checks))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply safe automatic remediations (M6)")
	cmd.Flags().BoolVar(&rebuildState, "rebuild-state", false, "reconstruct the ledger from config + live container labels")
	return cmd
}

// rebuildLedger reconstructs the shared_service + ref ledger from on-disk config
// intersected with live container labels (spec 09 §crash-recovery).
func rebuildLedger(cmd *cobra.Command, g *GlobalOpts) error {
	mgr, closeFn, err := buildManager(cmd)
	if err != nil {
		return err
	}
	defer closeFn()
	sum, err := mgr.RebuildState(cmd.Context())
	if err != nil {
		return err
	}
	if g.JSON {
		return writeJSON(cmd, sum)
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"rebuilt ledger from live labels: %d shared service(s), %d ref row(s)\n",
		len(sum.Shared), sum.Refs)
	return nil
}

// runDoctor assembles the full capability matrix. Each probe is independent so a
// single failure never hides the others.
func runDoctor(cmd *cobra.Command) []docker.Check {
	ctx := cmd.Context()
	var checks []docker.Check

	// Working-directory safety (WSL2 /mnt refusal).
	cwd, _ := os.Getwd()
	if err := xdg.RefuseWindowsMount(cwd); err != nil {
		checks = append(checks, docker.Check{
			Name: "working dir", Status: docker.StatusFail, Detail: err.Error(),
			Remediation: "move the workspace onto the Linux filesystem",
		})
	} else {
		checks = append(checks, docker.Check{Name: "working dir", Status: docker.StatusOK, Detail: cwd})
	}

	// State dir filesystem (SQLite reliability) and lock dir filesystem (flock
	// reliability) — they can be on different filesystems, so probe both.
	stateDir := xdg.DataHome()
	checks = append(checks, fsCheck("state dir (SQLite)", stateDir,
		"set XDG_DATA_HOME to a local (ext4/apfs) path on the Linux filesystem"))
	lockDir := xdg.RuntimeDir()
	if lockDir != stateDir {
		checks = append(checks, fsCheck("lock dir (flock)", lockDir,
			"set XDG_RUNTIME_DIR to a local tmpfs/ext4 path; the advisory lock lives here"))
	}

	// WSL2 awareness (informational).
	if xdg.IsWSL2() {
		checks = append(checks, docker.Check{Name: "platform", Status: docker.StatusOK, Detail: "WSL2 detected"})
	}

	// Docker / compose / git preflight.
	client, err := docker.NewClient(ctx)
	if err != nil {
		checks = append(checks, docker.Preflight(ctx, nil)...)
		checks = append(checks, docker.Check{
			Name: "docker client", Status: docker.StatusWarn, Detail: err.Error(),
			Remediation: "ensure DOCKER_HOST / the active docker context is valid",
		})
	} else {
		defer client.Close()
		checks = append(checks, docker.Preflight(ctx, client)...)
	}

	// State ledger opens (and migrates) cleanly.
	ctxName := state.DefaultContext
	if client != nil {
		ctxName = client.ContextName()
	}
	if db, err := state.Open(ctx, stateDir, ctxName); err != nil {
		checks = append(checks, docker.Check{
			Name: "state ledger", Status: docker.StatusFail, Detail: err.Error(),
			Remediation: "remove a corrupt state.db (a backup is kept) or run `devstack doctor --rebuild-state` (M2+)",
		})
	} else {
		v, _ := db.SchemaVersion()
		db.Close()
		checks = append(checks, docker.Check{Name: "state ledger", Status: docker.StatusOK, Detail: fmt.Sprintf("schema v%d @ %s", v, ctxName)})
	}

	// Local-CA trust (spec 05) — opt-in, so never fatal: report readiness as a
	// warning with the exact remediation when not fully set up.
	ts := trust.New().Status(ctx)
	if ts.OK() {
		checks = append(checks, docker.Check{Name: "trust (mkcert)", Status: docker.StatusOK, Detail: "local CA installed (" + ts.CARoot + ")"})
	} else {
		checks = append(checks, docker.Check{
			Name:        "trust (mkcert)",
			Status:      docker.StatusWarn,
			Detail:      fmt.Sprintf("mkcert=%v CA=%v firefox=%v", ts.MkcertFound, ts.CAInstalled, ts.FirefoxTrust),
			Remediation: ts.Remediation,
		})
	}

	return checks
}

func renderChecks(cmd *cobra.Command, checks []docker.Check) {
	w := cmd.OutOrStdout()
	for _, c := range checks {
		var icon string
		switch c.Status {
		case docker.StatusOK:
			icon = "✓"
		case docker.StatusWarn:
			icon = "!"
		default:
			icon = "✗"
		}
		fmt.Fprintf(w, "%s %-32s %s\n", icon, c.Name, c.Detail)
		if c.Status != docker.StatusOK && c.Remediation != "" {
			fmt.Fprintf(w, "    → %s\n", c.Remediation)
		}
	}
}

// fsCheck warns when dir is backed by a 9p/networked filesystem where SQLite and
// flock locking are unreliable (spec 08).
func fsCheck(name, dir, remediation string) docker.Check {
	fsType := xdg.FilesystemType(dir)
	switch {
	case fsType == "":
		return docker.Check{Name: name, Status: docker.StatusOK, Detail: dir}
	case xdg.IsUnreliableLockFS(fsType):
		return docker.Check{
			Name: name, Status: docker.StatusWarn,
			Detail:      fmt.Sprintf("%s is on %q where locking is unreliable", dir, fsType),
			Remediation: remediation,
		}
	default:
		return docker.Check{Name: name, Status: docker.StatusOK, Detail: fmt.Sprintf("%s (%s)", dir, fsType)}
	}
}

func countFails(checks []docker.Check) int {
	n := 0
	for _, c := range checks {
		if c.Status == docker.StatusFail {
			n++
		}
	}
	return n
}

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/envingest"
	"github.com/open-source-cloud/devstack/internal/workspace"
)

// newEnvCmd wires the `env` group (spec 30): view and edit a project service's
// local environment variables (env.raw) — the zero-setup, no-external-store
// key/value mechanism. Edits are comment/order-preserving AST rewrites of the
// project's devstack.yaml (the same engine `secrets ingest` uses). A value that is
// a secret:// ref is stored verbatim and resolved at generation (spec 04).
func newEnvCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "View and set a project service's local env vars (env.raw)",
	}
	cmd.AddCommand(newEnvListCmd(g), newEnvSetCmd(g), newEnvUnsetCmd(g))
	return cmd
}

func newEnvListCmd(g *GlobalOpts) *cobra.Command {
	var project, service string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List a service's env vars (raw, prefixed, imports)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			proj, svc, _, sv, err := resolveEnvTarget(mgr, project, service)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{
					"project": proj, "service": svc,
					"raw": sv.Env.Raw, "prefixed": sv.Env.Prefixed, "import": sv.Env.Import,
				})
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "# %s / %s\n", proj, svc)
			printEnvMap(tw, "raw", sv.Env.Raw)
			printEnvMap(tw, "prefixed", sv.Env.Prefixed)
			for _, im := range sv.Env.Import {
				fmt.Fprintf(tw, "import\t%s\t%s\n", im.From, strings.Join(im.Vars, ","))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "target project (default: the active/first project)")
	cmd.Flags().StringVar(&service, "service", "", "target service (default: the only service)")
	return cmd
}

func newEnvSetCmd(g *GlobalOpts) *cobra.Command {
	var project, service string
	cmd := &cobra.Command{
		Use:   "set KEY=VALUE [KEY=VALUE ...]",
		Short: "Set local env var(s) on a service (comment-preserving)",
		Long: "Set one or more KEY=VALUE env vars in a service's env.raw block. Existing keys\n" +
			"and comments are preserved. A VALUE of the form secret://... is stored as-is and\n" +
			"resolved from your secret provider at generation (never written to compose).",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kv := map[string]string{}
			for _, a := range args {
				k, v, ok := strings.Cut(a, "=")
				if !ok || k == "" {
					return fmt.Errorf("invalid KEY=VALUE: %q", a)
				}
				kv[k] = v
			}
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			proj, svc, path, _, err := resolveEnvTarget(mgr, project, service)
			if err != nil {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out, err := envingest.SetEnv(src, svc, "raw", kv)
			if err != nil {
				return err
			}
			if err := backupAndWrite(path, out); err != nil {
				return err
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "set %d var(s) on %s/%s\n", len(kv), proj, svc)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "target project (default: the active/first project)")
	cmd.Flags().StringVar(&service, "service", "", "target service (default: the only service)")
	return cmd
}

func newEnvUnsetCmd(g *GlobalOpts) *cobra.Command {
	var project, service string
	cmd := &cobra.Command{
		Use:   "unset KEY [KEY ...]",
		Short: "Remove local env var(s) from a service",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			proj, svc, path, sv, err := resolveEnvTarget(mgr, project, service)
			if err != nil {
				return err
			}
			remaining := map[string]string{}
			for k, v := range sv.Env.Raw {
				remaining[k] = v
			}
			for _, k := range args {
				delete(remaining, k)
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out, err := envingest.ReplaceEnvBlock(src, svc, "raw", remaining)
			if err != nil {
				return err
			}
			if err := backupAndWrite(path, out); err != nil {
				return err
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "unset %d var(s) on %s/%s\n", len(args), proj, svc)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "target project (default: the active/first project)")
	cmd.Flags().StringVar(&service, "service", "", "target service (default: the only service)")
	return cmd
}

// resolveEnvTarget resolves the (project, service, devstack.yaml path, service
// config). The service defaults to the sole service, else --service is required.
func resolveEnvTarget(mgr *workspace.Manager, project, service string) (string, string, string, config.Service, error) {
	proj := project
	if proj == "" {
		proj = resolveActiveProject(mgr.Model, mgr.DB)
	}
	p, ok := mgr.Model.Projects[proj]
	if !ok {
		return "", "", "", config.Service{}, fmt.Errorf("unknown project %q", proj)
	}
	svc := service
	if svc == "" {
		names := make([]string, 0, len(p.Services))
		for n := range p.Services {
			names = append(names, n)
		}
		if len(names) != 1 {
			sort.Strings(names)
			return "", "", "", config.Service{}, fmt.Errorf("project %q has %d services (%s) — pass --service", proj, len(names), strings.Join(names, ", "))
		}
		svc = names[0]
	}
	sv, ok := p.Services[svc]
	if !ok {
		return "", "", "", config.Service{}, fmt.Errorf("service %q not found in project %q", svc, proj)
	}
	path := filepath.Join(mgr.Model.ProjectDir(proj), "devstack.yaml")
	return proj, svc, path, sv, nil
}

func printEnvMap(w *tabwriter.Writer, block string, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		if strings.HasPrefix(v, "secret://") {
			v = "secret://… (resolved at generate)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", block, k, v)
	}
}

// backupAndWrite backs up path to path.bak.<ts> then writes data (0644), matching
// the init/import overwrite pattern.
func backupAndWrite(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, fmt.Sprintf("%s.bak.%d", path, time.Now().Unix())); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/store"
)

// newStoreCmd wires `store init|path|show` — the machine-global devstack home at
// ~/.devstack/ (override with $DEVSTACK_HOME): the store config file, custom
// templates, and the global shared-services stack.
func newStoreCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Manage the global devstack home (~/.devstack): config, templates, shared services",
	}
	cmd.AddCommand(newStoreInitCmd(g), newStorePathCmd(g), newStoreShowCmd(g))
	return cmd
}

func newStoreInitCmd(g *GlobalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create ~/.devstack with a config file, templates/ and shared/",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if store.Initialized() && !force {
				return fmt.Errorf("%s already exists; pass --force to overwrite its config", store.ConfigPath())
			}
			if err := store.EnsureLayout(); err != nil {
				return err
			}
			if err := store.DefaultConfig().Save(); err != nil {
				return err
			}
			if err := store.SeedTemplatesReadme(); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{
					"home": store.Home(), "config": store.ConfigPath(),
					"templates": store.TemplatesPath(),
					"shared":    store.SharedPath(),
				})
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "initialized %s\n", store.Home())
			fmt.Fprintf(w, "  config:    %s  (postgres, redis, minio)\n", store.ConfigFile)
			fmt.Fprintf(w, "  templates: %s/  (custom templates override built-ins by name)\n", store.TemplatesDir)
			fmt.Fprintf(w, "  shared:    %s/  (global shared-stack artifacts)\n", store.SharedDir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing store config")
	return cmd
}

func newStorePathCmd(_ *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the devstack home directory",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), store.Home())
		},
	}
}

func newStoreShowCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the store config (the global shared services)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, ok, err := store.Load()
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("store not initialized — run `%s store init`", rootName(cmd))
			}
			if g.JSON {
				return writeJSON(cmd, cfg)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "home:   %s\n", store.Home())
			fmt.Fprintf(w, "shared services:\n")
			names := make([]string, 0, len(cfg.Shared))
			for n := range cfg.Shared {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				s := cfg.Shared[n]
				if len(s.Params) > 0 {
					fmt.Fprintf(w, "  - %-10s template=%s params=%v\n", n, s.Template, s.Params)
				} else {
					fmt.Fprintf(w, "  - %-10s template=%s\n", n, s.Template)
				}
			}
			return nil
		},
	}
}

// userTemplatesDir returns the store templates dir if it exists, else "".
func userTemplatesDir() string {
	dir := store.TemplatesPath()
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return dir
	}
	return ""
}

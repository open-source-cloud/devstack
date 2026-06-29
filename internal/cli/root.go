// Package cli builds the cobra command tree, owns the global flag/error/log
// contract (spec 07, ARCHITECTURE §7.6/§7.9), and is wrapped by fang for styled
// help/errors, auto --version, man, and completions. Commands stay vanilla cobra
// so fang is removable in a day.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	fang "charm.land/fang/v2"
	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/alias"
	"github.com/open-source-cloud/devstack/internal/version"
)

// GlobalOpts holds the global flags every command renders through.
type GlobalOpts struct {
	JSON    bool
	Quiet   bool
	Debug   bool
	Verbose bool
}

// Options parameterize the root command.
type Options struct {
	// InvokedAs is the effective name (argv[0] base or --as override) used for
	// per-alias branding. The command tree is identical regardless.
	InvokedAs string
}

// NewRootCmd assembles the full command tree.
func NewRootCmd(opts Options) *cobra.Command {
	if opts.InvokedAs == "" {
		opts.InvokedAs = alias.Canonical
	}
	brand := alias.BrandFor(opts.InvokedAs)
	g := &GlobalOpts{}

	root := &cobra.Command{
		Use:   brand.Name,
		Short: "Docker-based dev environments with infrastructure shared across projects",
		// The version is shown on top of the help banner (fang renders Long first).
		Long: brand.Name + " " + version.Version + "\n\n" +
			"devstack manages Docker-based development environments and shares infrastructure\n" +
			"(one warm Postgres/Redis/MinIO) across many project stacks in a workspace.",
		Version:       version.String(),
		SilenceUsage:  true, // fang renders errors; don't dump usage on every failure
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			setupLogging(cmd.ErrOrStderr(), g)
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.BoolVar(&g.JSON, "json", false, "machine-readable JSON output where supported")
	pf.BoolVar(&g.Quiet, "quiet", false, "suppress non-essential output")
	pf.BoolVar(&g.Debug, "debug", false, "log every external command (docker/git/sql) and internals")
	pf.BoolVar(&g.Verbose, "verbose", false, "more detailed human output")

	// Stash globals so subcommands can read them via cmd.Context().
	cobra.OnInitialize()

	root.AddCommand(
		newUpCmd(g),
		newDownCmd(g),
		newStatusCmd(g),
		newDnsCmd(g),
		newTrustCmd(g),
		newTunnelCmd(g),
		newDoctorCmd(g),
		newConfigCmd(g),
		newGenerateCmd(g),
		newTemplateCmd(g),
		newSharedCmd(g),
		newWsCmd(g),
		newSelfCmd(g),
		newStoreCmd(g),
		newAliasCmd(g),
		newVersionCmd(),
	)
	addStubCommands(root, g)

	return root
}

// Execute wraps the root command with fang and runs it. The version/commit are
// passed explicitly because fang's `-v`/`--version` derives from debug build info
// otherwise — which renders "unknown (built from source)" for an ldflags-stamped
// binary instead of our actual version.
func Execute(ctx context.Context, root *cobra.Command) error {
	return fang.Execute(ctx, root,
		fang.WithVersion(version.Version),
		fang.WithCommit(version.Commit),
	)
}

// setupLogging configures slog based on the global flags. --debug → Debug level
// with source; --quiet → Error only; default → Warn (human output goes through
// command stdout, not the logger).
func setupLogging(w io.Writer, g *GlobalOpts) {
	level := slog.LevelWarn
	switch {
	case g.Debug:
		level = slog.LevelDebug
	case g.Verbose:
		level = slog.LevelInfo
	case g.Quiet:
		level = slog.LevelError
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:     level,
		AddSource: g.Debug,
	})
	slog.SetDefault(slog.New(h))
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit and build date",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.String())
		},
	}
}

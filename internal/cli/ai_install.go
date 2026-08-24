package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/ai"
	"github.com/open-source-cloud/devstack/internal/version"
)

// newAiInstallCmd wires `ai install` — it materializes the agent-integration
// files into the repository so any AI tool working here knows devstack exists and
// how to drive it.
//
// It is pure file authorship: no Docker, no ledger, no flock, and no workspace
// required. That last part is deliberate — the most valuable moment to install
// these files is often before `devstack init` has been run, when an agent still
// has to be told what devstack is.
func newAiInstallCmd(g *GlobalOpts) *cobra.Command {
	var (
		target string
		check  bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write the skills, AGENTS.md block and MCP registration into this repo",
		Long: "install writes the files that teach AI coding tools to use devstack:\n\n" +
			"  .claude/skills/devstack*/     Claude Code skills (devstack, templates, troubleshooting)\n" +
			"  AGENTS.md                     a marker-fenced block read by Codex, Cursor, Copilot,\n" +
			"                                Gemini CLI, Windsurf, Zed and Aider\n" +
			"  CLAUDE.md                     an @AGENTS.md import, since Claude Code reads CLAUDE.md\n" +
			"  .mcp.json                     registers `devstack ai mcp` as an MCP server\n\n" +
			"AGENTS.md, CLAUDE.md and .mcp.json are USER-owned: only devstack's own block or key is\n" +
			"replaced, so your content survives every regeneration. The skill files are fully\n" +
			"generated and are overwritten.\n\n" +
			"Output is deterministic; --check reports drift without writing (CI-friendly).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			targets := ai.All()
			if target != "" {
				parsed, err := ai.ParseTargets(target)
				if err != nil {
					return err
				}
				if !parsed.Any() {
					return fmt.Errorf("--target selected nothing (available: skills, agents, mcp)")
				}
				targets = parsed
			}
			root, err := aiInstallRoot()
			if err != nil {
				return err
			}
			gen := ai.New(
				ai.WithRoot(root),
				ai.WithBinary(rootName(cmd)),
				ai.WithVersion(version.Version),
			)
			arts, err := gen.Build(targets)
			if err != nil {
				return err
			}
			if check {
				return reportAiCheck(cmd, g, arts)
			}
			results, err := ai.Write(arts)
			if err != nil {
				return err
			}
			return reportAiWrite(cmd, g, results)
		},
	}
	cmd.Flags().StringVar(&target, "target", "",
		"comma-separated families to emit: skills, agents, mcp (default: all)")
	cmd.Flags().BoolVar(&check, "check", false, "report drift without writing (CI)")
	return cmd
}

// newAiCheckCmd is the drift gate, spelled as its own verb because that is how it
// reads in a CI file.
func newAiCheckCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Report whether the emitted agent files are up to date",
		Long: "check reports whether the files `ai install` writes match what this binary would\n" +
			"emit, without writing anything. Exits non-zero on drift, so it works as a CI gate.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := aiInstallRoot()
			if err != nil {
				return err
			}
			gen := ai.New(
				ai.WithRoot(root),
				ai.WithBinary(rootName(cmd)),
				ai.WithVersion(version.Version),
			)
			arts, err := gen.Build(ai.All())
			if err != nil {
				return err
			}
			return reportAiCheck(cmd, g, arts)
		},
	}
}

// aiInstallRoot picks where the files land: the workspace root when there is a
// plausible one, otherwise the current directory. Falling back rather than
// failing is what lets an agent install the guidance before a workspace exists.
//
// Workspace discovery walks UP from the current directory, which is right for
// `generate` but dangerous for a command that writes files: one stray
// workspace.yaml in a home directory would silently redirect the whole install
// there, scattering AGENTS.md, CLAUDE.md and .mcp.json across $HOME. So a
// discovered root is only honored when isSafeInstallRoot accepts it.
func aiInstallRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine the current directory: %w", err)
	}
	if m, lerr := loadWorkspace(); lerr == nil && m.Root != "" && isSafeInstallRoot(m.Root) {
		return m.Root, nil
	}
	if !isSafeInstallRoot(wd) {
		return "", fmt.Errorf(
			"refusing to install agent files into %s: that is your home directory, "+
				"not a project. Run this from inside a repository", wd)
	}
	return wd, nil
}

// isSafeInstallRoot refuses the user's home directory and any ancestor of it.
// These files are meant to be committed alongside a project; installing them
// machine-wide is a separate, deliberate feature (see Q-AI-SCOPE) and must never
// happen as a side effect of where a workspace.yaml happens to sit.
func isSafeInstallRoot(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return true
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	abs = filepath.Clean(abs)
	home = filepath.Clean(home)
	if abs == home {
		return false
	}
	// Also refuse anything above home (/, /home, …).
	if rel, err := filepath.Rel(abs, home); err == nil &&
		rel != "." && !strings.HasPrefix(rel, "..") {
		return false
	}
	return true
}

func reportAiWrite(cmd *cobra.Command, g *GlobalOpts, results []ai.WriteResult) error {
	if g.JSON {
		return writeJSON(cmd, map[string]any{"ok": true, "artifacts": results})
	}
	if g.Quiet {
		return nil
	}
	w := cmd.OutOrStdout()
	changed := 0
	for _, r := range results {
		state := "unchanged"
		if r.Changed {
			state = "wrote"
			changed++
		}
		fmt.Fprintf(w, "  %-9s %s\n", state, r.Path)
	}
	fmt.Fprintf(w, "\n%d of %d artifact(s) changed.\n", changed, len(results))
	if changed > 0 {
		fmt.Fprintf(w, "Commit these so your whole team's AI tools pick them up.\n"+
			"Teammates need `%s` on PATH; in Claude Code the MCP server also has to be\n"+
			"approved once (.claude/settings.local.json → enabledMcpjsonServers).\n",
			rootName(cmd))
	}
	return nil
}

func reportAiCheck(cmd *cobra.Command, g *GlobalOpts, arts []ai.Artifact) error {
	stale, err := ai.Stale(arts)
	if err != nil {
		return err
	}
	if g.JSON {
		paths := make([]string, 0, len(stale))
		for _, a := range stale {
			paths = append(paths, a.Rel)
		}
		if err := writeJSON(cmd, map[string]any{"ok": len(stale) == 0, "stale": paths}); err != nil {
			return err
		}
		if len(stale) > 0 {
			return fmt.Errorf("agent artifacts are stale; run `%s ai install`", rootName(cmd))
		}
		return nil
	}
	if len(stale) == 0 {
		if !g.Quiet {
			fmt.Fprintf(cmd.OutOrStdout(), "ok: %d artifact(s) up to date\n", len(arts))
		}
		return nil
	}
	if !g.Quiet {
		w := cmd.OutOrStdout()
		for _, a := range stale {
			fmt.Fprintf(w, "  stale  %s\n", a.Rel)
		}
	}
	return fmt.Errorf("%d agent artifact(s) are stale; run `%s ai install`", len(stale), rootName(cmd))
}

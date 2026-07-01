package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
)

// newShellCmd wires `devstack shell <service> [--project P] [-- cmd...]` (spec 26):
// an interactive exec into a running service container of the current (or
// --project) project stack. It shells `docker compose exec` with all three std
// streams inherited (a real -it TTY) via the InteractiveRunner and propagates the
// container's exit code verbatim. With no `-- cmd` it opens a login shell
// (bash→sh). A non-TTY invocation errors clearly rather than hanging.
func newShellCmd(g *GlobalOpts) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "shell [service] [-- cmd...]",
		Short: "Open an interactive shell (or run a command) in a service container",
		Long: "shell execs into a running service container of this workspace. With one\n" +
			"service it defaults to it; with many, name the service (completion helps).\n" +
			"With no `-- cmd` it opens a login shell (bash, falling back to sh); pass\n" +
			"`shell <service> -- <cmd...>` to run a one-off command instead. Requires an\n" +
			"interactive terminal.",
		// Args are the service name and, after `--`, the command. Cobra puts args
		// after `--` in ArgsLenAtDash; we split them ourselves below.
		ValidArgsFunction: shellServiceCompletion(&project),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Split the positional service arg from the post-`--` command.
			service, command := splitShellArgs(cmd, args)

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			model, err := config.Load(cwd)
			if err != nil {
				return err
			}
			proj, svc, err := resolveShellTarget(model, project, service)
			if err != nil {
				return err
			}

			// An interactive `compose exec -it` needs a real terminal on stdin; a
			// pipe/CI would otherwise hang or fail opaquely. Fail clearly first.
			if !isTerminal(os.Stdin) {
				return fmt.Errorf("shell needs an interactive terminal (stdin is not a TTY); " +
					"run it from a real terminal, or use a lifecycle hook / `docker compose exec -T` for scripted commands")
			}

			outDir := filepath.Join(model.ProjectDir(proj), generate.GenDir)
			composeFile := filepath.Join(outDir, generate.ComposeFile)
			if _, err := os.Stat(composeFile); err != nil {
				composeFile = "" // fall back to label-driven `compose -p <proj> exec`
			}
			run := command
			if len(run) == 0 {
				run = defaultShellCmd()
			}
			cp := docker.Compose{
				Project: "devstack-" + proj,
				File:    composeFile,
				Dir:     outDir,
				Runner:  docker.InteractiveRunner{},
			}
			err = cp.Exec(cmd.Context(), svc, true, run...)
			if err != nil {
				// Propagate the container's exit code verbatim (spec 26): a non-zero
				// shell exit is the shell's outcome, not a devstack failure.
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					os.Exit(ee.ExitCode())
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project whose stack to exec into (default: the only project, else required)")
	return cmd
}

// splitShellArgs separates the positional service name (before `--`) from the
// command to run (after `--`). Cobra records the `--` position in ArgsLenAtDash.
func splitShellArgs(cmd *cobra.Command, args []string) (service string, command []string) {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 {
		// No `--`: the first arg (if any) is the service; anything else is ignored.
		if len(args) > 0 {
			return args[0], nil
		}
		return "", nil
	}
	if dash > 0 {
		service = args[0]
	}
	command = args[dash:]
	return service, command
}

// resolveShellTarget resolves the project + service to exec into. project defaults
// to the workspace's only project (else it is required); service defaults to the
// project's only service (else it is required). Errors list the choices.
func resolveShellTarget(m *config.Model, project, service string) (proj, svc string, err error) {
	proj = project
	if proj == "" {
		names := sortedProjectNames(m)
		switch len(names) {
		case 0:
			return "", "", fmt.Errorf("this workspace has no projects")
		case 1:
			proj = names[0]
		default:
			return "", "", fmt.Errorf("workspace has multiple projects (%v); pass --project", names)
		}
	}
	p, ok := m.Projects[proj]
	if !ok {
		return "", "", fmt.Errorf("project %q is not in this workspace", proj)
	}
	svc = service
	if svc == "" {
		names := sortedServiceNames(p.Services)
		switch len(names) {
		case 0:
			return "", "", fmt.Errorf("project %q has no services", proj)
		case 1:
			svc = names[0]
		default:
			return "", "", fmt.Errorf("project %q has multiple services (%v); name one", proj, names)
		}
		return proj, svc, nil
	}
	if _, ok := p.Services[svc]; !ok {
		return "", "", fmt.Errorf("service %q is not in project %q (have %v)", svc, proj, sortedServiceNames(p.Services))
	}
	return proj, svc, nil
}

// defaultShellCmd is the login-shell command when no `-- cmd` is given: prefer
// bash, fall back to sh, probed inside the container in a single exec
// (Q-SHELL-DEFAULT-CMD).
func defaultShellCmd() []string {
	return []string{"sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
}

// shellServiceCompletion completes the service argument with the workspace's
// service names (of --project, or all projects), per spec 07's ValidArgsFunction.
func shellServiceCompletion(project *string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		cwd, err := os.Getwd()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		m, err := config.Load(cwd)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		set := map[string]bool{}
		for pname, p := range m.Projects {
			if *project != "" && pname != *project {
				continue
			}
			for sname := range p.Services {
				set[sname] = true
			}
		}
		out := make([]string, 0, len(set))
		for s := range set {
			out = append(out, s)
		}
		sort.Strings(out)
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// isTerminal reports whether f is a character device (a TTY), using only stdlib.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

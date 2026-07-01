package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// newShellInitCmd wires `shell-init <shell>` (spec 30): print the shell code a
// user eval's from their rc to get (a) the install dir on PATH, (b) a `devstack`
// wrapper function that eval's `use`'s output so switching mutates the live shell
// — the "execute, don't print" fix — (c) completion loading, and (d) an opt-in
// prompt-segment helper. The wrapper is named after the invoked binary, so aliases
// (`rq shell-init zsh`) generate a matching `rq()` wrapper.
func newShellInitCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "shell-init <zsh|bash|fish>",
		Short: "Print shell integration to eval (PATH, `use` wrapper, completions, prompt)",
		Long: "Print shell integration code for the given shell. Add it to your shell rc:\n\n" +
			"  zsh/bash:  eval \"$(devstack shell-init zsh)\"\n" +
			"  fish:      devstack shell-init fish | source\n\n" +
			"It puts the install dir on PATH, defines a `devstack` wrapper so `devstack use`\n" +
			"changes your current shell's directory + DEVSTACK_* env, loads completions, and\n" +
			"defines a `devstack_prompt` helper you can splice into your prompt.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := cmd.Root().Name()
			bindir := ""
			if exe, err := os.Executable(); err == nil {
				bindir = filepath.Dir(exe)
			}
			script, err := shellInitScript(name, args[0], bindir)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), script)
			return nil
		},
	}
}

const posixShellInit = `# devstack shell integration — add to your rc: eval "$(%[1]s shell-init %[3]s)"
case ":$PATH:" in
  *":%[2]s:"*) ;;
  *) export PATH="%[2]s:$PATH" ;;
esac
%[1]s() {
  if [ "$1" = use ]; then
    local _ds_out
    _ds_out="$(command %[1]s "$@" --print --shell %[3]s)" || return $?
    eval "$_ds_out"
  else
    command %[1]s "$@"
  fi
}
if command -v %[1]s >/dev/null 2>&1; then
  source <(command %[1]s completion %[3]s) 2>/dev/null || true
fi
%[1]s_prompt() { command %[1]s context --prompt 2>/dev/null; }
`

const fishShellInit = `# devstack shell integration — add to config.fish: %[1]s shell-init fish | source
if not contains %[2]s $PATH
  set -gx PATH %[2]s $PATH
end
function %[1]s
  if test "$argv[1]" = use
    command %[1]s $argv --print --shell fish | source
  else
    command %[1]s $argv
  end
end
command %[1]s completion fish | source
function %[1]s_prompt
  command %[1]s context --prompt 2>/dev/null
end
`

// shellInitScript renders the integration for one shell. name is the wrapper
// function name (the invoked binary), bindir the install dir to add to PATH.
func shellInitScript(name, shell, bindir string) (string, error) {
	switch shell {
	case "zsh", "bash":
		return fmt.Sprintf(posixShellInit, name, bindir, shell), nil
	case "fish":
		return fmt.Sprintf(fishShellInit, name, bindir), nil
	default:
		return "", fmt.Errorf("unsupported shell %q (want zsh, bash or fish)", shell)
	}
}

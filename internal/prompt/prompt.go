// Package prompt is the shared interactive-TUI substrate for devstack commands
// (spec 22): the TTY gate that keeps every wizard non-interactive-degradable, plus
// the shared huh/lipgloss theme and small render helpers. It is UI-only and imports
// no other internal package, so any command can depend on it without a cycle.
package prompt

import (
	"os"

	"golang.org/x/term"
)

// IsInteractive reports whether an interactive wizard should launch: a real TTY on
// both stdin and stdout, not suppressed by --json/--quiet/--no-input, and not under
// CI. Callers fall back to their flag/scriptable path when this is false, so the
// headline-output contract (ARCHITECTURE §7.9) holds and CI never drives Bubble Tea.
func IsInteractive(jsonOut, quiet, noInput bool) bool {
	if jsonOut || quiet || noInput || os.Getenv("CI") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// Package branding renders the devstack ASCII logo for help and version banners
// (spec 30). It is decorative only; callers gate it off under --json/--quiet so
// the machine-output contract (ARCHITECTURE §7.9) always holds.
package branding

import (
	_ "embed"
	"strings"
)

//go:embed logo.txt
var logo string

// Tagline is the one-line description shown under the logo.
const Tagline = "Docker dev environments with shared infrastructure"

// Logo returns the raw ASCII logo, trailing newlines trimmed.
func Logo() string { return strings.TrimRight(logo, "\n") }

// Banner returns the logo, a tagline, and the version, ready to print above help.
func Banner(version string) string {
	return Logo() + "\n\n  " + Tagline + " · " + version + "\n"
}

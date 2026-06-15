// Package version holds build-stamped identity, injected via -ldflags at
// release time (see .goreleaser.yaml). Defaults make `go run`/`go build`
// usable without ldflags.
package version

import "fmt"

// These are overwritten at build time with:
//
//	-ldflags "-X github.com/open-source-cloud/devstack/internal/version.Version=...
//	          -X ....Commit=... -X ....Date=..."
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String renders a human-readable version line.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}

// Command devstack is the single-binary entrypoint. It stays thin: resolve the
// invoked name (argv[0] alias dispatch + `--as` override), build the cobra tree,
// and hand off to fang. Version/commit/date are injected via -ldflags into
// internal/version.
package main

import (
	"context"
	"os"

	"github.com/open-source-cloud/devstack/internal/alias"
	"github.com/open-source-cloud/devstack/internal/cli"
)

func main() {
	// argv[0] dispatch: the base name (or a `--as <name>` override) selects
	// per-alias branding but the command tree is identical. Strip `--as` so
	// cobra never parses it.
	invoked, cleanedArgs := alias.ResolveInvocation(os.Args)
	os.Args = cleanedArgs

	root := cli.NewRootCmd(cli.Options{InvokedAs: invoked})
	if err := cli.Execute(context.Background(), root); err != nil {
		os.Exit(1)
	}
}

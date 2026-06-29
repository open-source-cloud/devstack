package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WithToken returns a copy of the Git handle that authenticates HTTPS remotes
// with token via a generated GIT_ASKPASS shim, plus a cleanup func that removes
// the shim. The token is written to a 0600 file in a private temp dir and never
// embedded in the URL or .git/config or visible in `ps` (spec 06). The secrets
// provider supplies the token (M4); this is the injection mechanism.
func (g *Git) WithToken(token string) (*Git, func(), error) {
	if token == "" {
		return g, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "devstack-askpass-")
	if err != nil {
		return nil, nil, fmt.Errorf("create askpass dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("write token: %w", err)
	}
	// The shim echoes nothing for a username prompt (HTTPS token auth ignores it)
	// and prints the token file for a password prompt. The token is in a 0600
	// file, never an argument, so it never appears in `ps`.
	shim := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in *[Uu]sername*) echo ;; *) cat " + shellQuote(tokenPath) + " ;; esac\n"
	if err := os.WriteFile(shim, []byte(script), 0o700); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("write askpass shim: %w", err)
	}

	cp := &Git{bin: g.bin, env: append(append([]string{}, g.env...), "GIT_ASKPASS="+shim)}
	return cp, cleanup, nil
}

// shellQuote single-quotes a path for safe interpolation into the POSIX shim.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

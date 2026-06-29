// Package trust manages the local CA used for HTTPS at *.localhost (spec 05). It
// shells out to the maintained `mkcert` binary (NOT smallstep/truststore) to
// install/remove the root CA into the host + Firefox/NSS stores, and diagnoses
// the platform tools mkcert needs at runtime so `trust status` can print exact
// remediations.
//
// Installing a CA is sudo-/privilege-gated; per locked decision #3 the logic is
// built + tested with a fake runner, the human/sudo step is flagged, and a
// doctor-style Status probe self-verifies. The mkcert process is run through an
// injectable Runner so the package is fully unit-testable without mkcert present.
package trust

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/open-source-cloud/devstack/internal/xdg"
)

// Runner runs the external mkcert binary. Injectable for tests.
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	Run(ctx context.Context, name string, args ...string) error
	LookPath(file string) (string, error)
}

// Trust wraps mkcert. The zero value uses the real OS exec runner.
type Trust struct {
	Runner Runner
}

// New returns a Trust backed by the real exec runner.
func New() *Trust { return &Trust{Runner: execRunner{}} }

func (t *Trust) runner() Runner {
	if t.Runner != nil {
		return t.Runner
	}
	return execRunner{}
}

// Available reports whether the mkcert binary is on PATH.
func (t *Trust) Available() bool {
	_, err := t.runner().LookPath("mkcert")
	return err == nil
}

// CARoot returns mkcert's CAROOT directory (where rootCA.pem lives).
func (t *Trust) CARoot(ctx context.Context) (string, error) {
	out, err := t.runner().Output(ctx, "mkcert", "-CAROOT")
	if err != nil {
		return "", fmt.Errorf("mkcert -CAROOT: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Install installs the local root CA into the system + NSS trust stores
// (`mkcert -install`). Requires privileges; a failure carries mkcert's output.
func (t *Trust) Install(ctx context.Context) error {
	if !t.Available() {
		return errMkcertMissing()
	}
	if err := t.runner().Run(ctx, "mkcert", "-install"); err != nil {
		return fmt.Errorf("mkcert -install (try sudo; ensure libnss3-tools/certutil on Linux): %w", err)
	}
	return nil
}

// Uninstall removes the local root CA from the trust stores (`mkcert -uninstall`).
func (t *Trust) Uninstall(ctx context.Context) error {
	if !t.Available() {
		return errMkcertMissing()
	}
	if err := t.runner().Run(ctx, "mkcert", "-uninstall"); err != nil {
		return fmt.Errorf("mkcert -uninstall: %w", err)
	}
	return nil
}

// Status is a diagnostic snapshot of local-CA readiness (the `trust status` view
// + a doctor probe). Each field has a one-line remediation when not OK.
type Status struct {
	MkcertFound  bool   `json:"mkcertFound"`
	CARoot       string `json:"caRoot,omitempty"`
	CAInstalled  bool   `json:"caInstalled"`  // rootCA.pem exists in CAROOT
	FirefoxTrust bool   `json:"firefoxTrust"` // certutil present (NSS / Firefox)
	WSL          bool   `json:"wsl"`
	Remediation  string `json:"remediation,omitempty"`
}

// Status probes the environment. It never mutates anything (no sudo needed).
func (t *Trust) Status(ctx context.Context) Status {
	s := Status{WSL: xdg.IsWSL2()}
	s.MkcertFound = t.Available()
	if !s.MkcertFound {
		s.Remediation = "install mkcert (https://github.com/FiloSottile/mkcert) then run `devstack trust install`"
		return s
	}
	if root, err := t.CARoot(ctx); err == nil {
		s.CARoot = root
		if root != "" {
			if _, err := os.Stat(filepath.Join(root, "rootCA.pem")); err == nil {
				s.CAInstalled = true
			}
		}
	}
	// certutil backs Firefox/NSS trust; absent on a clean Ubuntu/WSL2.
	_, certutilErr := t.runner().LookPath("certutil")
	s.FirefoxTrust = certutilErr == nil

	switch {
	case !s.CAInstalled:
		s.Remediation = "run `sudo devstack trust install` to create + trust the local CA"
	case !s.FirefoxTrust:
		s.Remediation = "install certutil for Firefox/NSS trust: `apt install libnss3-tools` (Debian/Ubuntu)"
	case s.WSL:
		s.Remediation = "WSL2: also import the CA into the Windows store so browsers-on-Windows trust it (certutil.exe -addstore -user Root <CAROOT>/rootCA.pem)"
	}
	return s
}

// OK reports whether local HTTPS trust is fully ready.
func (s Status) OK() bool { return s.MkcertFound && s.CAInstalled && s.FirefoxTrust }

func errMkcertMissing() error {
	return fmt.Errorf("mkcert not found on PATH — install it (https://github.com/FiloSottile/mkcert)")
}

// execRunner is the production Runner.
type execRunner struct{}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
func (execRunner) LookPath(file string) (string, error) { return exec.LookPath(file) }

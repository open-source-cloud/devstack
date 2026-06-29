package trust

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeRunner struct {
	caroot     string
	have       map[string]bool // binaries "on PATH"
	installErr error
	calls      []string
}

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+join(args))
	if name == "mkcert" && len(args) == 1 && args[0] == "-CAROOT" {
		return []byte(f.caroot + "\n"), nil
	}
	return nil, errors.New("unexpected Output call")
}
func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, name+" "+join(args))
	if name == "mkcert" && len(args) == 1 && args[0] == "-install" {
		return f.installErr
	}
	return nil
}
func (f *fakeRunner) LookPath(file string) (string, error) {
	if f.have[file] {
		return "/usr/bin/" + file, nil
	}
	return "", errors.New("not found")
}

func join(a []string) string {
	out := ""
	for i, s := range a {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

func TestAvailable(t *testing.T) {
	if (&Trust{Runner: &fakeRunner{have: map[string]bool{"mkcert": true}}}).Available() != true {
		t.Error("mkcert present → Available true")
	}
	if (&Trust{Runner: &fakeRunner{have: map[string]bool{}}}).Available() != false {
		t.Error("mkcert absent → Available false")
	}
}

func TestStatusMkcertMissing(t *testing.T) {
	tr := &Trust{Runner: &fakeRunner{have: map[string]bool{}}}
	s := tr.Status(context.Background())
	if s.MkcertFound || s.OK() {
		t.Error("missing mkcert → not found, not OK")
	}
	if s.Remediation == "" {
		t.Error("missing mkcert → a remediation")
	}
}

func TestStatusCANotInstalled(t *testing.T) {
	root := t.TempDir() // CAROOT exists but no rootCA.pem
	tr := &Trust{Runner: &fakeRunner{caroot: root, have: map[string]bool{"mkcert": true}}}
	s := tr.Status(context.Background())
	if !s.MkcertFound || s.CAInstalled {
		t.Errorf("status = %+v, want found + not installed", s)
	}
	if s.OK() {
		t.Error("CA not installed → not OK")
	}
}

func TestStatusFullyReady(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "rootCA.pem"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &Trust{Runner: &fakeRunner{caroot: root, have: map[string]bool{"mkcert": true, "certutil": true}}}
	s := tr.Status(context.Background())
	if !s.CAInstalled || !s.FirefoxTrust {
		t.Errorf("status = %+v, want CA installed + firefox trust", s)
	}
	if !s.OK() {
		t.Error("mkcert + CA + certutil → OK")
	}
}

func TestStatusMissingCertutil(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "rootCA.pem"), []byte("x"), 0o644)
	tr := &Trust{Runner: &fakeRunner{caroot: root, have: map[string]bool{"mkcert": true}}}
	s := tr.Status(context.Background())
	if s.FirefoxTrust {
		t.Error("no certutil → FirefoxTrust false")
	}
	if s.OK() {
		t.Error("no certutil → not OK")
	}
}

func TestInstallUninstall(t *testing.T) {
	fr := &fakeRunner{have: map[string]bool{"mkcert": true}}
	tr := &Trust{Runner: fr}
	if err := tr.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tr.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	sawInstall, sawUninstall := false, false
	for _, c := range fr.calls {
		if c == "mkcert -install" {
			sawInstall = true
		}
		if c == "mkcert -uninstall" {
			sawUninstall = true
		}
	}
	if !sawInstall || !sawUninstall {
		t.Errorf("calls = %v, want install + uninstall", fr.calls)
	}
}

func TestInstallMissingMkcert(t *testing.T) {
	tr := &Trust{Runner: &fakeRunner{have: map[string]bool{}}}
	if err := tr.Install(context.Background()); err == nil {
		t.Error("install without mkcert should error")
	}
}

func TestInstallPropagatesError(t *testing.T) {
	boom := errors.New("permission denied")
	tr := &Trust{Runner: &fakeRunner{have: map[string]bool{"mkcert": true}, installErr: boom}}
	if err := tr.Install(context.Background()); err == nil {
		t.Error("install failure should surface")
	}
}

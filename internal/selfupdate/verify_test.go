package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"
)

// okVerifier is a Verifier that is always available and always accepts — the happy
// path stand-in so download tests never shell real cosign or hit the network.
type okVerifier struct{}

func (okVerifier) Available() bool                                          { return true }
func (okVerifier) VerifyBlob(context.Context, []byte, []byte, []byte) error { return nil }

// recordingVerifier captures its inputs and returns a configurable availability +
// verification result, so a test can assert what was verified and simulate tamper.
type recordingVerifier struct {
	available bool
	err       error
	called    bool
	gotBlob   []byte
	gotSig    []byte
	gotCert   []byte
}

func (r *recordingVerifier) Available() bool { return r.available }
func (r *recordingVerifier) VerifyBlob(_ context.Context, blob, sig, cert []byte) error {
	r.called = true
	r.gotBlob, r.gotSig, r.gotCert = blob, sig, cert
	return r.err
}

// TestSignatureVerifyHappyPath: a good signature over checksums.txt lets the
// download proceed, and the verifier is handed the exact checksums.txt bytes +
// published .sig/.pem.
func TestSignatureVerifyHappyPath(t *testing.T) {
	archive, sum := makeArchive(t, "SIGNED-BINARY")
	_, cleanup := releaseServer(t, "v0.2.0", archive, sum)
	defer cleanup()

	v := &recordingVerifier{available: true}
	bin, err := downloadBinary(context.Background(), "v0.2.0", runtime.GOOS, runtime.GOARCH, v, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(bin) != "SIGNED-BINARY" {
		t.Errorf("downloaded %q", bin)
	}
	if !v.called {
		t.Fatal("verifier was never invoked — signature check was skipped")
	}
	if !strings.Contains(string(v.gotBlob), sum) {
		t.Errorf("verifier got blob %q, want it to contain the checksum %q", v.gotBlob, sum)
	}
	if string(v.gotSig) != "FAKE-SIGNATURE" || string(v.gotCert) != "FAKE-CERT" {
		t.Errorf("verifier got sig=%q cert=%q, want the published .sig/.pem", v.gotSig, v.gotCert)
	}
}

// TestSignatureVerifyTamperDetected: a verifier that rejects the signature aborts
// the update, leaving the download unusable (tamper/unsigned/wrong-identity).
func TestSignatureVerifyTamperDetected(t *testing.T) {
	archive, sum := makeArchive(t, "TAMPERED-BINARY")
	_, cleanup := releaseServer(t, "v0.2.0", archive, sum)
	defer cleanup()

	v := &recordingVerifier{available: true, err: context.Canceled} // any non-nil error
	if _, err := downloadBinary(context.Background(), "v0.2.0", runtime.GOOS, runtime.GOARCH, v, false); err == nil {
		t.Fatal("want a signature-verification error, got nil")
	}
}

// TestSignatureVerifyRunsBeforeChecksum: signature verification is the trust root —
// it must run before (and independently of) the per-archive SHA-256 check.
func TestSignatureVerifyRunsBeforeChecksum(t *testing.T) {
	archive, _ := makeArchive(t, "X")
	// Wrong checksum in the list, but the signature verifier rejects first.
	_, cleanup := releaseServer(t, "v0.2.0", archive, "deadbeef")
	defer cleanup()

	v := &recordingVerifier{available: true, err: context.Canceled}
	_, err := downloadBinary(context.Background(), "v0.2.0", runtime.GOOS, runtime.GOARCH, v, false)
	if err == nil || !v.called {
		t.Fatalf("signature verifier should have run and failed first (called=%v err=%v)", v.called, err)
	}
}

// TestCosignUnavailableAborts: with verification on (default) and no cosign binary,
// the update refuses with a remediation rather than silently proceeding.
func TestCosignUnavailableAborts(t *testing.T) {
	archive, sum := makeArchive(t, "Y")
	_, cleanup := releaseServer(t, "v0.2.0", archive, sum)
	defer cleanup()

	v := &recordingVerifier{available: false}
	_, err := downloadBinary(context.Background(), "v0.2.0", runtime.GOOS, runtime.GOARCH, v, false)
	if err == nil {
		t.Fatal("want an abort when cosign is unavailable, got nil")
	}
	if !strings.Contains(err.Error(), "cosign") || !strings.Contains(err.Error(), "insecure-skip-verify") {
		t.Errorf("error should name cosign + the escape hatch, got: %v", err)
	}
	if v.called {
		t.Error("VerifyBlob must not be called when the backend is unavailable")
	}
}

// TestMissingSignatureAssetAborts: a release without a checksums.txt.sig is
// refused (an unsigned release) when verification is on.
func TestMissingSignatureAssetAborts(t *testing.T) {
	archive, sum := makeArchive(t, "Z")
	_, cleanup := unsignedReleaseServer(t, "v0.2.0", archive, sum)
	defer cleanup()

	v := &recordingVerifier{available: true}
	_, err := downloadBinary(context.Background(), "v0.2.0", runtime.GOOS, runtime.GOARCH, v, false)
	if err == nil {
		t.Fatal("want an abort for a release with no signature asset, got nil")
	}
	if !strings.Contains(err.Error(), sigAssetName) {
		t.Errorf("error should mention the missing %s, got: %v", sigAssetName, err)
	}
}

// TestSkipVerifyBypassesSignature: --insecure-skip-verify skips the cosign check
// entirely (no verifier call, no .sig needed) but still enforces SHA-256.
func TestSkipVerifyBypassesSignature(t *testing.T) {
	archive, sum := makeArchive(t, "UNSIGNED-BUT-OK")
	_, cleanup := unsignedReleaseServer(t, "v0.2.0", archive, sum)
	defer cleanup()

	v := &recordingVerifier{available: true}
	bin, err := downloadBinary(context.Background(), "v0.2.0", runtime.GOOS, runtime.GOARCH, v, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(bin) != "UNSIGNED-BUT-OK" {
		t.Errorf("downloaded %q", bin)
	}
	if v.called {
		t.Error("--insecure-skip-verify must not invoke the verifier")
	}

	// SHA-256 is STILL enforced even with signature verification skipped.
	badArchive, _ := makeArchive(t, "SOMETHING-ELSE")
	_, cleanup2 := unsignedReleaseServer(t, "v0.2.0", badArchive, "deadbeef")
	defer cleanup2()
	if _, err := downloadBinary(context.Background(), "v0.2.0", runtime.GOOS, runtime.GOARCH, v, true); err == nil {
		t.Fatal("SHA-256 must still fail a tampered archive even with --insecure-skip-verify")
	}
}

// unsignedReleaseServer serves a release WITHOUT the .sig/.pem assets.
func unsignedReleaseServer(t *testing.T, tag string, archive []byte, sum string) (*httptest.Server, func()) {
	t.Helper()
	asset := assetName(tag, runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/releases/tags/"+tag, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"assets":[{"name":%q,"url":%q},{"name":"checksums.txt","url":%q}]}`,
			asset, srv.URL+"/asset/archive", srv.URL+"/asset/sums")
	})
	mux.HandleFunc("/asset/archive", func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	mux.HandleFunc("/asset/sums", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "%s  %s\n", sum, asset) })
	old := APIBase
	APIBase = srv.URL
	return srv, func() { APIBase = old; srv.Close() }
}

// TestReleaseWorkflowDeclaresIDToken asserts the release workflow YAML parses and
// grants id-token: write (required for keyless cosign OIDC signing). No network.
func TestReleaseWorkflowDeclaresIDToken(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	data, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release.yml: %v", err)
	}
	var wf struct {
		Permissions map[string]string `yaml:"permissions"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("release.yml is not valid YAML: %v", err)
	}
	if got := wf.Permissions["id-token"]; got != "write" {
		t.Errorf("release.yml permissions.id-token = %q, want %q (keyless cosign needs it)", got, "write")
	}
	if got := wf.Permissions["contents"]; got != "write" {
		t.Errorf("release.yml permissions.contents = %q, want %q", got, "write")
	}
	if !strings.Contains(string(data), "cosign-installer") {
		t.Error("release.yml must install cosign (sigstore/cosign-installer) for signing")
	}
}

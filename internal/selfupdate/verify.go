package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// This file adds cosign signature verification to the self-update flow (spec 14 §
// "Signature verification is mandatory", spec 25). Releases are signed KEYLESS via
// GitHub OIDC (sigstore/cosign) in .github/workflows/release.yml: goreleaser's
// `signs:` block runs `cosign sign-blob` over checksums.txt and publishes
// checksums.txt.sig (signature) + checksums.txt.pem (Fulcio certificate) as release
// assets. checksums.txt is the ROOT of the integrity chain — once its signature is
// verified, the per-archive SHA-256 lines in it are trusted, and downloadBinary's
// existing SHA-256 check ties each archive back to that trusted list.
//
// Verification shells the external `cosign` binary behind the Verifier interface
// (mock-able, like internal/docker / internal/git), so tests never touch the
// network or need cosign installed. Pure-Go verification via sigstore-go was
// considered but shelling cosign keeps the release binary CGO-free and matches the
// repo's "wrap the external tool behind an internal/ interface" rule.

const (
	// CertIdentityRegexp pins the signer identity to THIS repo's release workflow
	// (the SAN of the Fulcio cert cosign issues to the GitHub Actions job). Keyless
	// signing embeds the workflow ref, e.g.
	//   https://github.com/open-source-cloud/devstack/.github/workflows/release.yml@refs/tags/v0.2.0
	// so we anchor on the workflow path and accept any ref suffix.
	CertIdentityRegexp = `^https://github\.com/open-source-cloud/devstack/\.github/workflows/release\.yml@.+$`

	// OIDCIssuer is the GitHub Actions OIDC token issuer — the only issuer we trust
	// for keyless release signatures.
	OIDCIssuer = "https://token.actions.githubusercontent.com"

	// cosignBin is the external binary shelled for verification.
	cosignBin = "cosign"

	// sigAssetName / certAssetName are the goreleaser-published signature + Fulcio
	// certificate for checksums.txt.
	sigAssetName  = "checksums.txt.sig"
	certAssetName = "checksums.txt.pem"
)

// Verifier verifies a keyless cosign signature over a blob. Behind an interface so
// tests inject a fake and the real implementation shells cosign.
type Verifier interface {
	// Available reports whether the verification backend (the cosign binary) is
	// usable on this host. When false, the caller decides whether to abort with a
	// remediation or honor an explicit --insecure-skip-verify.
	Available() bool
	// VerifyBlob returns nil when sig is a valid keyless signature over blob, made
	// by cert, whose identity matches CertIdentityRegexp and whose OIDC issuer is
	// OIDCIssuer. Any other outcome (bad signature, wrong identity, tampered blob)
	// returns a non-nil error.
	VerifyBlob(ctx context.Context, blob, sig, cert []byte) error
}

// defaultVerifier is the cosign-backed Verifier used when Options.Verifier is nil.
var defaultVerifier Verifier = cosignVerifier{}

// cosignVerifier shells `cosign verify-blob` (keyless).
type cosignVerifier struct{}

// Available reports whether the cosign binary is on PATH.
func (cosignVerifier) Available() bool {
	_, err := exec.LookPath(cosignBin)
	return err == nil
}

// VerifyBlob writes blob/sig/cert to temp files and runs `cosign verify-blob`,
// pinning the certificate identity + OIDC issuer to this repo's release workflow.
func (cosignVerifier) VerifyBlob(ctx context.Context, blob, sig, cert []byte) error {
	dir, err := os.MkdirTemp("", "devstack-cosign-")
	if err != nil {
		return fmt.Errorf("create cosign scratch dir: %w", err)
	}
	defer os.RemoveAll(dir)

	blobPath := filepath.Join(dir, "checksums.txt")
	sigPath := filepath.Join(dir, sigAssetName)
	certPath := filepath.Join(dir, certAssetName)
	for path, data := range map[string][]byte{blobPath: blob, sigPath: sig, certPath: cert} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("write cosign input %s: %w", filepath.Base(path), err)
		}
	}

	// #nosec G204 -- args are fixed constants + tool-controlled temp paths.
	cmd := exec.CommandContext(ctx, cosignBin,
		"verify-blob",
		"--certificate-identity-regexp", CertIdentityRegexp,
		"--certificate-oidc-issuer", OIDCIssuer,
		"--signature", sigPath,
		"--certificate", certPath,
		blobPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cosign signature verification failed for checksums.txt — refusing to install (release may be unsigned, tampered, or signed by an untrusted identity): %w: %s",
			err, strings.TrimSpace(string(out)))
	}
	return nil
}

// verifyChecksumsSignature verifies the cosign keyless signature over the raw
// checksums.txt bytes, fetching the .sig + .pem assets from the same release. It is
// the mandatory integrity gate before any archive SHA-256 is trusted. A missing
// verifier binary or missing signature asset aborts (unless the caller passed
// --insecure-skip-verify) with a clear remediation — it never silently downgrades.
func verifyChecksumsSignature(ctx context.Context, v Verifier, assets map[string]asset, sums []byte) error {
	if v == nil {
		v = defaultVerifier
	}
	if !v.Available() {
		return fmt.Errorf("cosign not found: release-signature verification is required but the `cosign` binary is not installed.\n" +
			"  Install it (https://docs.sigstore.dev/system_config/installation/) and retry,\n" +
			"  or re-run with --insecure-skip-verify to bypass signature checks (SHA-256 checksum is still enforced).")
	}
	sigAsset, ok := assets[sigAssetName]
	if !ok {
		return fmt.Errorf("release has no %s — refusing to install an unsigned release (pass --insecure-skip-verify to override; SHA-256 still enforced)", sigAssetName)
	}
	certAsset, ok := assets[certAssetName]
	if !ok {
		return fmt.Errorf("release has no %s — refusing to install without a signing certificate (pass --insecure-skip-verify to override; SHA-256 still enforced)", certAssetName)
	}

	sig, err := downloadAsset(ctx, sigAsset.URL)
	if err != nil {
		return fmt.Errorf("download %s: %w", sigAssetName, err)
	}
	cert, err := downloadAsset(ctx, certAsset.URL)
	if err != nil {
		return fmt.Errorf("download %s: %w", certAssetName, err)
	}
	return v.VerifyBlob(ctx, sums, sig, cert)
}

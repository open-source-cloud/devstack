package registry

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Signature verification (spec 19 §"Supply-chain posture"). Digest verification is
// mandatory and always-on (see Pull); SIGNATURE verification is optional but
// first-class: with a VerifyPolicy configured, `add`/`update`/`verify` require a
// valid cosign signature over the manifest digest before the artifact is trusted.
//
// Consistent with internal/selfupdate (which deliberately shells the external
// `cosign` binary behind an interface to keep the release binary CGO-free rather
// than vendor the heavy sigstore-go tree), the real Verifier shells `cosign
// verify`. The seam is mock-able so tests never touch the network or need cosign.
// The pure-Go sigstore-go path (spec 19 §"no cosign binary required") is scoped
// out for this milestone and tracked in the PR — the seam makes it a drop-in swap.

// VerifyPolicy pins the accepted signer identity for keyless cosign, or a public
// key for keyed cosign. A zero policy means "no signature policy" — the artifact
// is digest-pinned but unsigned (a loud one-line notice is printed on add).
type VerifyPolicy struct {
	// Enabled turns signature verification on. When false, only the digest is
	// verified (mandatory) and a "digest-pinned but unsigned" notice is shown.
	Enabled bool
	// IdentityRegexp pins the keyless signer identity (the Fulcio cert SAN, e.g. a
	// GitHub Actions workflow ref). Required in keyless mode.
	IdentityRegexp string
	// OIDCIssuer pins the keyless OIDC issuer (e.g. GitHub Actions' token issuer).
	// Required in keyless mode.
	OIDCIssuer string
	// KeyPath points at a cosign public key for KEYED (offline) verification. When
	// set, keyless identity/issuer are ignored.
	KeyPath string
}

// IsZero reports whether no signature policy is configured.
func (p VerifyPolicy) IsZero() bool { return !p.Enabled && p.KeyPath == "" }

// Keyless reports whether the policy verifies via keyless (Fulcio/Rekor) rather
// than a distributed public key.
func (p VerifyPolicy) Keyless() bool { return p.KeyPath == "" }

// Verifier verifies a cosign signature over an OCI artifact addressed by its
// digest reference. Behind an interface so tests inject a fake and the real
// implementation shells cosign.
type Verifier interface {
	// Available reports whether the verification backend (the cosign binary) is
	// usable on this host.
	Available() bool
	// Verify returns nil when digestRef carries a valid cosign signature satisfying
	// policy; any other outcome (unsigned, wrong identity, tampered) is an error.
	Verify(ctx context.Context, digestRef string, policy VerifyPolicy) error
}

// DefaultVerifier is the cosign-backed Verifier used when none is injected.
var DefaultVerifier Verifier = cosignVerifier{}

// cosignVerifier shells `cosign verify`.
type cosignVerifier struct{}

const cosignBin = "cosign"

func (cosignVerifier) Available() bool {
	_, err := exec.LookPath(cosignBin)
	return err == nil
}

func (cosignVerifier) Verify(ctx context.Context, digestRef string, policy VerifyPolicy) error {
	args := []string{"verify"}
	if policy.Keyless() {
		if policy.IdentityRegexp == "" || policy.OIDCIssuer == "" {
			return fmt.Errorf("keyless cosign policy requires both an identity regexp and an OIDC issuer")
		}
		args = append(args,
			"--certificate-identity-regexp", policy.IdentityRegexp,
			"--certificate-oidc-issuer", policy.OIDCIssuer,
		)
	} else {
		args = append(args, "--key", policy.KeyPath)
	}
	args = append(args, digestRef)

	// #nosec G204 -- args are fixed flags + a validated digest reference.
	cmd := exec.CommandContext(ctx, cosignBin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cosign signature verification failed for %s — refusing to trust the artifact (unsigned, tampered, or signed by an untrusted identity): %w: %s",
			digestRef, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Signer signs an OCI artifact addressed by its digest reference. Behind an
// interface for symmetry with Verifier; the real implementation shells `cosign
// sign` (keyless by default). Scoped for manual/CI use — see the package doc.
type Signer interface {
	Available() bool
	Sign(ctx context.Context, digestRef string, keyPath string) error
}

// DefaultSigner is the cosign-backed Signer.
var DefaultSigner Signer = cosignSigner{}

type cosignSigner struct{}

func (cosignSigner) Available() bool {
	_, err := exec.LookPath(cosignBin)
	return err == nil
}

func (cosignSigner) Sign(ctx context.Context, digestRef, keyPath string) error {
	args := []string{"sign", "--yes"}
	if keyPath != "" {
		args = append(args, "--key", keyPath)
	}
	args = append(args, digestRef)
	// #nosec G204 -- fixed flags + a validated digest reference + operator key path.
	cmd := exec.CommandContext(ctx, cosignBin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cosign sign failed for %s: %w: %s", digestRef, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SignArtifact signs desc with s (or DefaultSigner). A missing cosign binary is a
// clear error with a remediation.
func SignArtifact(ctx context.Context, s Signer, desc Descriptor, keyPath string) error {
	if s == nil {
		s = DefaultSigner
	}
	if !s.Available() {
		return fmt.Errorf("cosign not found: --sign requires the `cosign` binary (https://docs.sigstore.dev/system_config/installation/)")
	}
	return s.Sign(ctx, desc.Repository+"@"+desc.Digest, keyPath)
}

// VerifySignature verifies the artifact at desc.Ref against policy using v (or the
// DefaultVerifier when nil). A no-op (nil) when the policy is zero — the caller is
// responsible for printing the unsigned notice. A missing cosign binary with an
// enabled policy is a hard error with a remediation (never a silent downgrade).
func VerifySignature(ctx context.Context, v Verifier, desc Descriptor, policy VerifyPolicy) error {
	if policy.IsZero() {
		return nil
	}
	if v == nil {
		v = DefaultVerifier
	}
	if !v.Available() {
		return fmt.Errorf("cosign not found: template signature verification is required by policy but the `cosign` binary is not installed.\n" +
			"  Install it (https://docs.sigstore.dev/system_config/installation/) and retry,\n" +
			"  or remove the verify policy to trust the digest pin alone (digest is still enforced).")
	}
	digestRef := desc.Repository + "@" + desc.Digest
	return v.Verify(ctx, digestRef, policy)
}

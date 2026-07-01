package registry

import (
	"context"
	"errors"
	"testing"
)

// fakeVerifier is a mock Verifier: it records what it was asked to verify and
// returns a configurable availability + result, so tests exercise the trust gate
// without shelling cosign or touching the network.
type fakeVerifier struct {
	available bool
	err       error
	called    bool
	gotRef    string
	gotPolicy VerifyPolicy
}

func (f *fakeVerifier) Available() bool { return f.available }
func (f *fakeVerifier) Verify(_ context.Context, digestRef string, policy VerifyPolicy) error {
	f.called = true
	f.gotRef = digestRef
	f.gotPolicy = policy
	return f.err
}

var keylessPolicy = VerifyPolicy{
	Enabled:        true,
	IdentityRegexp: `^https://github\.com/acme/.+$`,
	OIDCIssuer:     "https://token.actions.githubusercontent.com",
}

func desc() Descriptor {
	return Descriptor{Repository: "ghcr.io/acme/postgres", Digest: "sha256:abc"}
}

func TestVerifyGoodSignatureAccepted(t *testing.T) {
	v := &fakeVerifier{available: true}
	if err := VerifySignature(context.Background(), v, desc(), keylessPolicy); err != nil {
		t.Fatalf("good signature should be accepted: %v", err)
	}
	if !v.called {
		t.Fatal("verifier was never invoked")
	}
	if v.gotRef != "ghcr.io/acme/postgres@sha256:abc" {
		t.Errorf("verified %q, want the digest ref", v.gotRef)
	}
	if v.gotPolicy.IdentityRegexp != keylessPolicy.IdentityRegexp {
		t.Errorf("policy identity not propagated: %+v", v.gotPolicy)
	}
}

func TestVerifyTamperedSignatureRejected(t *testing.T) {
	v := &fakeVerifier{available: true, err: errors.New("signature mismatch")}
	if err := VerifySignature(context.Background(), v, desc(), keylessPolicy); err == nil {
		t.Fatal("a tampered/invalid signature must be rejected")
	}
}

func TestVerifyZeroPolicyIsNoop(t *testing.T) {
	v := &fakeVerifier{available: false} // would fail if consulted
	if err := VerifySignature(context.Background(), v, desc(), VerifyPolicy{}); err != nil {
		t.Fatalf("a zero policy verifies nothing (digest-pinned only): %v", err)
	}
	if v.called {
		t.Error("no verification should happen without a policy")
	}
}

func TestVerifyUnavailableCosignAborts(t *testing.T) {
	v := &fakeVerifier{available: false}
	err := VerifySignature(context.Background(), v, desc(), keylessPolicy)
	if err == nil {
		t.Fatal("an enabled policy with no cosign backend must abort")
	}
	if v.called {
		t.Error("Verify must not be called when the backend is unavailable")
	}
}

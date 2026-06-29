package secrets

import (
	"fmt"

	"filippo.io/age"
)

// This file is the age keypair generator behind `secrets keygen` (spec 04) — the
// onboarding companion to the SOPS+age provider (S2): it lets a developer create
// the local age identity that SOPS_AGE_KEY_FILE points at, with no account and
// fully offline. Pure-Go (filippo.io/age), so it works in a static binary.

// AgeKey is a generated age keypair: the secret identity (AGE-SECRET-KEY-…, kept
// 0600) and its public recipient (age1…, shared / used as a SOPS recipient).
type AgeKey struct {
	Identity  string // AGE-SECRET-KEY-...
	Recipient string // age1...
}

// GenerateAgeKey creates a fresh X25519 age identity.
func GenerateAgeKey() (AgeKey, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return AgeKey{}, fmt.Errorf("generate age identity: %w", err)
	}
	return AgeKey{Identity: id.String(), Recipient: id.Recipient().String()}, nil
}

// AgeKeyFileContents renders the standard age key file body: a comment with the
// public recipient (so the file is self-describing) followed by the secret key,
// matching what `age-keygen` writes.
func (k AgeKey) AgeKeyFileContents() string {
	return fmt.Sprintf("# public key: %s\n%s\n", k.Recipient, k.Identity)
}

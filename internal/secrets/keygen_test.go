package secrets

import (
	"strings"
	"testing"

	"filippo.io/age"
)

func TestGenerateAgeKey(t *testing.T) {
	k, err := GenerateAgeKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.Identity, "AGE-SECRET-KEY-1") {
		t.Errorf("identity = %q, want an AGE-SECRET-KEY-1… secret", k.Identity)
	}
	if !strings.HasPrefix(k.Recipient, "age1") {
		t.Errorf("recipient = %q, want an age1… public key", k.Recipient)
	}
	// The generated identity must round-trip through age's parser and yield the
	// same recipient.
	id, err := age.ParseX25519Identity(k.Identity)
	if err != nil {
		t.Fatalf("generated identity does not parse: %v", err)
	}
	if id.Recipient().String() != k.Recipient {
		t.Errorf("recipient mismatch: parsed %q vs reported %q", id.Recipient(), k.Recipient)
	}
	// Two calls produce distinct keys.
	k2, _ := GenerateAgeKey()
	if k2.Identity == k.Identity {
		t.Error("two GenerateAgeKey calls produced the same identity")
	}
}

func TestAgeKeyFileContents(t *testing.T) {
	k := AgeKey{Identity: "AGE-SECRET-KEY-1XXXX", Recipient: "age1yyyy"}
	body := k.AgeKeyFileContents()
	if !strings.Contains(body, "# public key: age1yyyy") || !strings.Contains(body, "AGE-SECRET-KEY-1XXXX") {
		t.Errorf("key file body = %q", body)
	}
	if !strings.HasSuffix(body, "\n") {
		t.Error("key file should end with a newline")
	}
}

package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// This file is the credential generator behind the `generated` resource
// credential policy (spec 27 §Credential surfacing) — distinct from
// GenerateAgeKey (age/SOPS key material). It produces a random, URL-safe secret
// via crypto/rand (pure-Go, no new dependency) that a provisioner pushes to a
// secrets provider (the Pusher) and injects as a valueless env key; the value is
// never written to a generated file.

// RandomPassword returns a cryptographically-random, URL-safe password with at
// least n characters (n must be positive). It draws from crypto/rand and encodes
// with base64 raw-url (no padding), so the result is safe in a DSN and free of
// shell-special characters.
func RandomPassword(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("RandomPassword: length %d must be positive", n)
	}
	// base64 raw-url yields ~4 chars per 3 bytes; request enough bytes to cover n.
	nbytes := (n*3 + 3) / 4
	buf := make([]byte, nbytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("RandomPassword: read random bytes: %w", err)
	}
	s := base64.RawURLEncoding.EncodeToString(buf)
	return s[:n], nil
}

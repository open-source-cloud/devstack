package secrets

import (
	"crypto/rand"
	"fmt"
)

// This file is the credential generator behind the `generated` resource
// credential policy (spec 27 §Credential surfacing) — distinct from
// GenerateAgeKey (age/SOPS key material). It produces a random, ALPHANUMERIC
// secret via crypto/rand (pure-Go, no new dependency) that a provisioner routes
// through a secrets Pusher and injects as a valueless env key; the value is never
// written to a generated file.

// pwAlphabet is the alphanumeric alphabet (62 symbols) RandomPassword draws from.
// It is deliberately free of shell/DSN-hostile characters (no +/-_=), so a value
// is safe unquoted in a DSN, a URL, or an env var.
const pwAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// RandomPassword returns a cryptographically-random, alphanumeric password of
// exactly n characters (n must be positive). It draws from crypto/rand with
// rejection sampling so every symbol is uniform (no modulo bias).
func RandomPassword(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("RandomPassword: length %d must be positive", n)
	}
	// Reject bytes at/above the largest multiple of len(alphabet) so the modulo
	// mapping is unbiased (256 % 62 != 0 would otherwise favour the first symbols).
	const maxUnbiased = 256 - (256 % len(pwAlphabet))
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("RandomPassword: read random bytes: %w", err)
		}
		for _, b := range buf {
			if int(b) >= maxUnbiased {
				continue // reject to keep the distribution uniform
			}
			out = append(out, pwAlphabet[int(b)%len(pwAlphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

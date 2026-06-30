// Package envingest owns the `.env` ingestion pipeline (spec 24): parse an
// existing .env, classify each key secret-vs-config (default-deny), route the
// secret half into a SOPS+age file or a remote provider (never plaintext on
// disk), rewrite the target devstack.yaml env block in place (comment/order
// preserving via the goccy AST), scaffold the destination secrets provider into
// workspace.yaml when absent, prove every new ref round-trips, fence .env in
// .gitignore, and delete it. The secret-vs-config policy and the YAML rewrite
// live here; the provider boundary stays in internal/secrets.
package envingest

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Class is the secret-vs-config verdict for one .env key.
type Class int

const (
	// ClassSecret routes the value into the encrypted/remote destination as a
	// secret:// ref. ClassConfig inlines the value (or ${env.KEY}) in config.
	ClassSecret Class = iota
	ClassConfig
)

// String renders a Class for reports.
func (c Class) String() string {
	if c == ClassConfig {
		return "config"
	}
	return "secret"
}

// Decision is the classification + emission plan for one key, emitted in
// sorted-key order for determinism.
type Decision struct {
	Key      string `json:"key"`
	Class    string `json:"class"`  // "secret" | "config"
	Reason   string `json:"reason"` // human reason for the verdict
	Ref      string `json:"ref"`    // emitted value: secret:// ref OR inline literal OR ${env.KEY}
	HostFrom bool   `json:"hostFrom"`
	Service  string `json:"service"`
	value    string // plaintext (never serialized; used to assemble the payload)
}

// IsSecret reports whether the decision routes to the secret destination.
func (d Decision) IsSecret() bool { return d.Class == ClassSecret.String() }

// Value returns the plaintext for this key (internal use by the assembler).
func (d Decision) Value() string { return d.value }

// secretNameTokens are case-insensitive substrings whose presence in a KEY name
// marks it a secret. Extends the internal/generate secretAttrs seed
// (password/secretkey/secret/token).
var secretNameTokens = []string{
	"password", "passwd", "pwd",
	"secret", "secretkey", "secret_key",
	"token", "apikey", "api_key",
	"accesskey", "access_key", "privatekey", "private_key",
	"credential", "credentials", "passphrase", "auth_token",
}

var (
	// credentialedURL matches scheme://user:pass@host (embedded credentials).
	credentialedURL = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://[^/@\s:]+:[^/@\s]+@`)
	// plainURL matches a scheme://host URL WITHOUT embedded credentials.
	plainURL = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)
	// tokenShape matches an opaque high-entropy token (no spaces/scheme chars).
	tokenShape = regexp.MustCompile(`^[A-Za-z0-9+/_=.-]{20,}$`)
	// jwtShape matches a JWT (three base64url segments; header starts eyJ).
	jwtShape = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
)

// Classify applies the policy ladder to each key and returns Decisions in
// sorted-key order. globs are shell-style (path.Match). The ladder, in order:
// explicit --secret glob → --public glob → name-pattern → value heuristics →
// benign-config recognition → default-deny (secret). Config keys matching a
// --from-host glob are marked HostFrom (emitted as ${env.KEY}).
func Classify(vars map[string]string, secretGlobs, publicGlobs, fromHostGlobs []string, service string) ([]Decision, error) {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]Decision, 0, len(keys))
	for _, k := range keys {
		v := vars[k]
		class, reason := classifyOne(k, v, secretGlobs, publicGlobs)
		d := Decision{Key: k, Class: class.String(), Reason: reason, Service: service, value: v}
		if class == ClassConfig && matchAnyGlob(fromHostGlobs, k) {
			d.HostFrom = true
		}
		out = append(out, d)
	}
	return out, nil
}

func classifyOne(key, value string, secretGlobs, publicGlobs []string) (Class, string) {
	if g, ok := matchGlob(secretGlobs, key); ok {
		return ClassSecret, fmt.Sprintf("--secret override (%s)", g)
	}
	if g, ok := matchGlob(publicGlobs, key); ok {
		return ClassConfig, fmt.Sprintf("--public override (%s)", g)
	}
	if tok, ok := nameSignalsSecret(key); ok {
		return ClassSecret, fmt.Sprintf("name matches *%s*", tok)
	}
	if reason, ok := valueSignalsSecret(value); ok {
		return ClassSecret, reason
	}
	if looksBenign(value) {
		return ClassConfig, "no secret signal"
	}
	return ClassSecret, "default-deny (unknown key)"
}

// nameSignalsSecret reports whether the KEY name contains a secret token.
func nameSignalsSecret(key string) (string, bool) {
	lk := strings.ToLower(key)
	for _, t := range secretNameTokens {
		if strings.Contains(lk, t) {
			return strings.ToUpper(t), true
		}
	}
	return "", false
}

// valueSignalsSecret reports whether the VALUE shape looks like a secret.
func valueSignalsSecret(value string) (string, bool) {
	switch {
	case credentialedURL.MatchString(value):
		return "credentialed URL", true
	case strings.HasPrefix(value, "-----BEGIN "):
		return "PEM-encoded material", true
	case jwtShape.MatchString(value):
		return "JWT-shaped value", true
	case tokenShape.MatchString(value) && entropyBits(value) >= 3.0:
		return "high-entropy value", true
	}
	return "", false
}

// looksBenign reports whether the value is a recognizable non-secret: empty, a
// number/boolean, a short simple word, or a credential-free URL (DNS alias).
func looksBenign(value string) bool {
	if value == "" {
		return true
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return true
	}
	switch strings.ToLower(value) {
	case "true", "false", "yes", "no", "on", "off", "null", "nil", "none":
		return true
	}
	if plainURL.MatchString(value) && !credentialedURL.MatchString(value) {
		return true
	}
	if len(value) < 20 && !tokenShape.MatchString(value) {
		return true
	}
	return false
}

// entropyBits returns the Shannon entropy (bits/char) of s.
func entropyBits(s string) float64 {
	if s == "" {
		return 0
	}
	freq := map[rune]float64{}
	for _, r := range s {
		freq[r]++
	}
	n := float64(len([]rune(s)))
	var h float64
	for _, c := range freq {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

// matchGlob returns the first glob that matches key (path.Match semantics) and
// the pattern itself.
func matchGlob(globs []string, key string) (string, bool) {
	for _, g := range globs {
		if ok, _ := path.Match(g, key); ok {
			return g, true
		}
	}
	return "", false
}

func matchAnyGlob(globs []string, key string) bool {
	_, ok := matchGlob(globs, key)
	return ok
}

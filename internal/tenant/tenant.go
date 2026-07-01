// Package tenant resolves the per-user tenant identity for a shared backend
// (spec 21 §per-user isolation, Q-REMOTE-TENANT). On a LOCAL backend there is no
// tenant: names stay <project>, exactly as today (backward compatible). On a
// REMOTE team cluster, two developers both running a project named `app` would
// collide and read each other's data — so every provisioned resource is
// namespaced by the developer's tenant identity (role u_<user>_<project>, db
// <user>_<project>, bucket <user>-<project>-…). This package owns ONLY resolving
// and sanitizing that identity; each engine provisioner composes the namespaced
// name with its own separator (spec 27: a provisioner owns its identifier map).
package tenant

import (
	"os"
	"os/user"
	"strings"
)

// maxLen bounds a sanitized tenant fragment so a namespaced identifier stays
// within the strictest engine limit (S3 bucket names are 63 chars, shared with a
// project + suffix — keep the tenant portion short).
const maxLen = 32

// Identity is the resolved tenant for a backend. An empty Name means LOCAL (no
// namespacing); a non-empty Name is the sanitized per-user identity on a team
// backend.
type Identity struct {
	Name string
}

// IsTenant reports whether namespacing applies (a remote team backend).
func (id Identity) IsTenant() bool { return id.Name != "" }

// Qualify namespaces a resource name with the tenant using the engine's
// separator. Local (empty tenant) returns name unchanged — the existing
// <project> naming, so nothing on a single-user machine shifts. On a team
// backend it returns <tenant><sep><name>.
func (id Identity) Qualify(name, sep string) string {
	if id.Name == "" {
		return name
	}
	return id.Name + sep + name
}

// Deps injects the environment + OS-user lookups so Resolve is unit-testable
// without touching the real environment.
type Deps struct {
	Getenv func(string) string
	OSUser func() (string, error)
}

// DefaultDeps wires the real os.Getenv + the OS username.
func DefaultDeps() Deps {
	return Deps{
		Getenv: os.Getenv,
		OSUser: osUsername,
	}
}

// Resolve derives the tenant identity for a backend. LOCAL backends have no
// tenant (empty). For a REMOTE team backend the precedence (Q-REMOTE-TENANT) is:
//
//	DEVSTACK_TENANT env  →  the workspace-configured identity  →  the OS username
//
// falling back to "user" so even a nameless account still isolates. The result
// is sanitized to a safe, stable identifier fragment (the same user always maps
// to the same tenant across runs and machines).
func Resolve(remote bool, configured string, deps Deps) Identity {
	if !remote {
		return Identity{}
	}
	if deps.Getenv == nil {
		deps.Getenv = os.Getenv
	}
	if deps.OSUser == nil {
		deps.OSUser = osUsername
	}
	raw := deps.Getenv("DEVSTACK_TENANT")
	if raw == "" {
		raw = configured
	}
	if raw == "" {
		if u, err := deps.OSUser(); err == nil {
			raw = u
		}
	}
	name := Sanitize(raw)
	if name == "" {
		name = "user"
	}
	return Identity{Name: name}
}

// Sanitize maps an arbitrary identity to a safe, stable identifier fragment that
// is valid across every engine's namespace (the strictest being S3 buckets:
// lowercase, alphanumeric + hyphen). It lowercases, replaces each run of
// non-alphanumeric characters with a single hyphen, trims leading/trailing
// hyphens, and caps the length. Deterministic.
func Sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxLen {
		out = strings.Trim(out[:maxLen], "-")
	}
	return out
}

// osUsername returns the current OS user's username (lowercased happens in
// Sanitize). Separated so tests inject a fake.
func osUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

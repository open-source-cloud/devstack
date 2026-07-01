// Package registry is devstack's OCI template-registry seam (spec 19). It packages
// a template bundle (template.yaml + optional build/ tree + golden.yaml) as a
// deterministic OCI artifact, pushes/pulls it addressed by name:tag and pinned by
// the manifest digest, and verifies its signature — all behind a small interface
// so the CLI never touches oras directly and tests round-trip through an in-memory
// store with no network.
//
// The pure-Go client is oras.land/oras-go/v2 (Apache-2.0, CGO-free), preserving
// the single-static-binary constraint. Registry auth rides the existing docker /
// ORAS credential store (~/.docker/config.json, OS helpers, GITHUB_TOKEN) — we do
// NOT invent a devstack token store (spec 19 §"Verified constraints").
//
// Determinism is the whole point: a render is reproducible across a team only if
// template content is addressed by DIGEST, not a moving tag. Pull verifies the
// pulled manifest's computed digest against the pinned digest before anything is
// unpacked, and the bundle tar is byte-deterministic (see bundle.go).
package registry

import (
	"fmt"
	"strings"

	"github.com/opencontainers/go-digest"
)

// Scheme is the optional URI scheme accepted on a template ref.
const Scheme = "oci://"

// Reference is a parsed OCI reference: a registry host, a repository path, and
// EITHER a tag OR a digest (a ref may carry both — `repo:tag@sha256:…` — in which
// case both are populated and the digest wins for fetching).
type Reference struct {
	// Registry is the host[:port] (e.g. "ghcr.io", "localhost:5000").
	Registry string
	// Repository is the path under the registry (e.g. "acme/templates").
	Repository string
	// Tag is the human-facing tag (e.g. "1.4.0"), possibly empty when a bare
	// digest ref was given.
	Tag string
	// Digest is the pinned manifest digest (e.g. "sha256:9c8b…"), possibly empty
	// when only a tag was given (it is resolved+pinned at `template add`).
	Digest string
}

// ParseReference parses an `oci://host/repo:tag`, `host/repo:tag`,
// `host/repo@sha256:…`, or `host/repo:tag@sha256:…` reference. The `oci://`
// scheme is optional and stripped. Registry and repository are required.
func ParseReference(ref string) (Reference, error) {
	orig := ref
	ref = strings.TrimPrefix(ref, Scheme)
	if ref == "" {
		return Reference{}, fmt.Errorf("empty registry reference")
	}

	var r Reference
	// Split off an optional @digest first (it always trails).
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		dig := ref[at+1:]
		if err := digest.Digest(dig).Validate(); err != nil {
			return Reference{}, fmt.Errorf("invalid digest %q in %q: %w", dig, orig, err)
		}
		r.Digest = dig
		ref = ref[:at]
	}

	// The registry host is the first path segment and MUST look like a host
	// (contain a "." or ":" or be "localhost") so `repo:tag` is not mistaken for
	// `host/repo`.
	slash := strings.IndexByte(ref, '/')
	if slash < 0 {
		return Reference{}, fmt.Errorf("reference %q is missing a registry host (want host/repo:tag)", orig)
	}
	r.Registry = ref[:slash]
	rest := ref[slash+1:]
	if !looksLikeHost(r.Registry) {
		return Reference{}, fmt.Errorf("reference %q has no registry host — %q is not a hostname (want e.g. ghcr.io/owner/name:tag)", orig, r.Registry)
	}

	// A tag, if present, trails the LAST colon in the remaining path (repo paths
	// never contain colons; ports were already consumed by the registry host).
	if colon := strings.LastIndex(rest, ":"); colon >= 0 {
		r.Tag = rest[colon+1:]
		rest = rest[:colon]
	}
	r.Repository = rest
	if r.Repository == "" {
		return Reference{}, fmt.Errorf("reference %q is missing a repository path", orig)
	}
	if r.Tag == "" && r.Digest == "" {
		return Reference{}, fmt.Errorf("reference %q must carry a tag or a digest", orig)
	}
	return r, nil
}

// ParseRepository parses a bare `oci://host/repo` (or `host/repo`) with NO tag or
// digest — the form recorded in the lockfile's `source:` field. `update`/`diff`/
// `verify` parse it and then re-attach the recorded tag/digest before fetching.
func ParseRepository(s string) (Reference, error) {
	s = strings.TrimPrefix(s, Scheme)
	slash := strings.IndexByte(s, '/')
	if slash < 0 {
		return Reference{}, fmt.Errorf("repository %q is missing a registry host (want host/repo)", s)
	}
	r := Reference{Registry: s[:slash], Repository: s[slash+1:]}
	if !looksLikeHost(r.Registry) || r.Repository == "" {
		return Reference{}, fmt.Errorf("invalid repository %q (want e.g. ghcr.io/owner/name)", s)
	}
	return r, nil
}

// looksLikeHost reports whether s is plausibly a registry host: it contains a dot
// or a port colon, or is exactly "localhost".
func looksLikeHost(s string) bool {
	return s == "localhost" || strings.ContainsAny(s, ".:")
}

// IsFloatingTag reports whether the tag is a mutable "floating" tag (":latest")
// that breaks determinism when fetched at generation. `template add` rejects it
// without --allow-floating (spec 19 §"A moving tag breaks determinism").
func (r Reference) IsFloatingTag() bool {
	return r.Tag == "latest"
}

// Name is the "registry/repository" identity without any tag or digest — the key
// used to build an oras Target for the repository.
func (r Reference) Name() string {
	return r.Registry + "/" + r.Repository
}

// TagRef renders "registry/repository:tag" (used as the oras copy destination
// reference on push and the resolve reference on pull-by-tag).
func (r Reference) TagRef() string {
	return r.Name() + ":" + r.Tag
}

// DigestRef renders "registry/repository@digest" (the pinned pull reference).
func (r Reference) DigestRef() string {
	return r.Name() + "@" + r.Digest
}

// FetchRef returns the reference oras should resolve: the digest when pinned
// (reproducible), else the tag. Generation always pins a digest; `add`/`diff`
// may resolve a bare tag.
func (r Reference) FetchRef() string {
	if r.Digest != "" {
		return r.DigestRef()
	}
	return r.TagRef()
}

// shortRef is the tag-or-digest oras resolves against a repository Target (the
// registry/repo is implied by the Target). A bare tag resolves uniformly across
// remote registries and the local memory/OCI stores tests use; a bare digest is
// prefixed with "@" so a live registry treats it as a digest. Pull always has a
// tag (a pin is minted FROM a tag), so it resolves by tag then asserts the digest
// matches the pin — never depending on a store resolving a raw digest.
func (r Reference) shortRef() string {
	if r.Tag != "" {
		return r.Tag
	}
	return "@" + r.Digest
}

// String renders the canonical `oci://` form for display and lockfile provenance.
func (r Reference) String() string {
	out := Scheme + r.Name()
	if r.Tag != "" {
		out += ":" + r.Tag
	}
	if r.Digest != "" {
		out += "@" + r.Digest
	}
	return out
}

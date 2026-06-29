// Package secrets resolves `secret://` references from pluggable backends and
// feeds them into containers WITHOUT writing plaintext to disk and WITHOUT
// putting secrets in committed config (spec 04, ARCHITECTURE §7.5).
//
// This file is the core (S1): the reference grammar + parser, the tiny Provider
// interface (batch Resolve), the kind→Factory registry with lazily-built named
// instances, the batched cross-provider Resolve orchestration, the ref collector
// used by the post-render pass, and a log redactor. Concrete providers (SOPS+age,
// AWS, Infisical, keyring) register against this registry (S2–S5).
package secrets

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Scheme is the reference URI scheme.
const Scheme = "secret://"

// Ref is a parsed secret reference. Raw is the original string and is the key
// used for substitution and for Resolve results (Ref itself is not a valid map
// key — Opts is a map — so results are keyed by Raw).
//
//	secret://<provider>/<path>#<key>?opt=val&opt2=val2
type Ref struct {
	Raw      string            // the exact original "secret://…" string
	Provider string            // named provider instance (workspace secrets.providers[].name)
	Path     string            // backend path/identifier
	Key      string            // optional sub-key (#key); "" if absent
	Opts     map[string]string // optional ?query options
}

// IsRef reports whether s is a secret reference.
func IsRef(s string) bool { return strings.HasPrefix(s, Scheme) }

// ParseRef parses a secret:// reference. The order is scheme, then provider, then
// path, with an optional #key and ?opts tail (fragment-before-query, per spec 04).
func ParseRef(s string) (Ref, error) {
	if !IsRef(s) {
		return Ref{}, fmt.Errorf("not a secret reference (must start with %q): %q", Scheme, s)
	}
	ref := Ref{Raw: s}
	body := strings.TrimPrefix(s, Scheme)

	// Split the ?query tail first, then the #key fragment.
	if before, after, found := strings.Cut(body, "?"); found {
		ref.Opts = parseOpts(after)
		body = before
	}
	if before, after, found := strings.Cut(body, "#"); found {
		ref.Key = after
		body = before
	}
	// body is now "<provider>/<path>".
	provider, path, found := strings.Cut(body, "/")
	if !found {
		return Ref{}, fmt.Errorf("secret ref %q is missing a provider/path separator", s)
	}
	ref.Provider = provider
	ref.Path = path
	if ref.Provider == "" {
		return Ref{}, fmt.Errorf("secret ref %q has an empty provider", s)
	}
	if ref.Path == "" {
		return Ref{}, fmt.Errorf("secret ref %q has an empty path", s)
	}
	return ref, nil
}

func parseOpts(q string) map[string]string {
	if q == "" {
		return nil
	}
	out := map[string]string{}
	for kv := range strings.SplitSeq(q, "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// Provider is a secrets backend. The single method is a BATCH Resolve — every
// named backend is naturally batched (Infisical List, SSM GetParameters,
// SOPS whole-file decrypt); single-key providers loop internally. The result is
// keyed by each Ref's Raw string.
type Provider interface {
	Name() string
	Resolve(ctx context.Context, refs []Ref) (map[string]string, error)
}

// ProviderConfig is the typed declaration of one named provider instance
// (workspace secrets.providers[]). Kind selects the registered Factory.
type ProviderConfig struct {
	Name      string
	Kind      string // sops | aws-sm | aws-ssm | infisical | …
	Env       string
	ProjectID string
	Region    string
	Opts      map[string]string
}

// Factory builds a Provider from its config. Concrete backends register one per
// kind via Registry.RegisterFactory at init.
type Factory func(cfg ProviderConfig) (Provider, error)

// Registry maps provider KINDS to factories and provider NAMES to their declared
// config, building (and caching) each named Provider lazily on first use.
type Registry struct {
	factories map[string]Factory
	configs   map[string]ProviderConfig
	built     map[string]Provider
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: map[string]Factory{},
		configs:   map[string]ProviderConfig{},
		built:     map[string]Provider{},
	}
}

// RegisterFactory registers a backend kind's factory (idempotent overwrite).
func (r *Registry) RegisterFactory(kind string, f Factory) { r.factories[kind] = f }

// Configure declares a named provider instance (from workspace.yaml).
func (r *Registry) Configure(cfg ProviderConfig) { r.configs[cfg.Name] = cfg }

// Provider returns the named provider, building it from its config's kind on
// first use. Errors clearly when the name is undeclared or its kind unregistered.
func (r *Registry) Provider(name string) (Provider, error) {
	if p, ok := r.built[name]; ok {
		return p, nil
	}
	cfg, ok := r.configs[name]
	if !ok {
		return nil, fmt.Errorf("secret provider %q is not declared in this workspace", name)
	}
	f, ok := r.factories[cfg.Kind]
	if !ok {
		return nil, fmt.Errorf("secret provider %q uses unknown kind %q (no backend registered)", name, cfg.Kind)
	}
	p, err := f(cfg)
	if err != nil {
		return nil, fmt.Errorf("build secret provider %q (kind %s): %w", name, cfg.Kind, err)
	}
	r.built[name] = p
	return p, nil
}

// Resolve resolves every ref by grouping them per provider and calling each
// provider's batch Resolve exactly once over its UNIQUE refs (N refs to one
// provider → one call). The result is keyed by each ref's Raw string. A provider
// that omits a requested ref yields an error (a missing secret must never pass
// silently as empty).
func Resolve(ctx context.Context, reg *Registry, refs []Ref) (map[string]string, error) {
	byProvider := map[string][]Ref{}
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref.Raw] {
			continue
		}
		seen[ref.Raw] = true
		byProvider[ref.Provider] = append(byProvider[ref.Provider], ref)
	}

	out := map[string]string{}
	for _, name := range sortedKeys(byProvider) {
		p, err := reg.Provider(name)
		if err != nil {
			return nil, err
		}
		got, err := p.Resolve(ctx, byProvider[name])
		if err != nil {
			return nil, fmt.Errorf("provider %q resolve: %w", name, err)
		}
		for _, ref := range byProvider[name] {
			v, ok := got[ref.Raw]
			if !ok {
				return nil, fmt.Errorf("provider %q did not resolve %q", name, ref.Raw)
			}
			out[ref.Raw] = v
		}
	}
	return out, nil
}

// Collect scans the given strings and returns every distinct, valid secret
// reference found (the post-render pass feeds this to Resolve). An invalid
// secret:// string is returned as an error so a typo never silently no-ops.
func Collect(values ...string) ([]Ref, error) {
	var out []Ref
	seen := map[string]bool{}
	for _, v := range values {
		if !IsRef(v) || seen[v] {
			continue
		}
		ref, err := ParseRef(v)
		if err != nil {
			return nil, err
		}
		seen[v] = true
		out = append(out, ref)
	}
	return out, nil
}

// Redact replaces every resolved secret value in s with "***" so debug output
// never leaks a secret (the redaction middleware, spec 04). Empty/short values
// are skipped to avoid mangling unrelated text.
func Redact(s string, values map[string]string) string {
	for _, v := range values {
		if len(v) < 4 {
			continue
		}
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

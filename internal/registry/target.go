package registry

import (
	"context"
	"fmt"
	"os"

	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// refWithDigest returns a copy of ref with its Digest set (Tag preserved).
func refWithDigest(ref Reference, dig string) Reference {
	ref.Digest = dig
	return ref
}

// wrapAuth maps an oras authorization failure to a one-line remediation (the
// ARCHITECTURE §7.6 error contract) and otherwise annotates the failing step.
func wrapAuth(err error, ref Reference, step string) error {
	if err == nil {
		return nil
	}
	if isAuthMessage(err) {
		return fmt.Errorf("not authorized for %s (%s): log in with `docker login %s` (or set GITHUB_TOKEN for ghcr.io) — devstack reads ~/.docker/config.json: %w",
			ref.Name(), step, ref.Registry, err)
	}
	return fmt.Errorf("%s %s: %w", step, ref.Name(), err)
}

// isAuthMessage catches auth failures that some registries surface without the
// typed errdef.ErrUnauthorized (e.g. a 403 on push to a repo you can read).
func isAuthMessage(err error) bool {
	msg := err.Error()
	for _, s := range []string{"401", "403", "unauthorized", "denied", "forbidden"} {
		if containsFold(msg, s) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	// tiny ASCII-fold contains to avoid pulling strings for two call sites
	ls, lsub := toLowerASCII(s), toLowerASCII(sub)
	return len(lsub) == 0 || indexOf(ls, lsub) >= 0
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func indexOf(s, sub string) int {
	n, m := len(s), len(sub)
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}

// defaultTargetResolver builds authenticated *remote.Repository targets, reading
// credentials from the docker/ORAS credential store (~/.docker/config.json + OS
// helpers). A GITHUB_TOKEN in the environment is used for ghcr.io when the docker
// store has no entry, so CI publishes without a `docker login` step.
func defaultTargetResolver() (TargetResolver, error) {
	credStore, err := credentials.NewStoreFromDocker(credentials.StoreOptions{
		AllowPlaintextPut:        false,
		DetectDefaultNativeStore: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open docker credential store: %w", err)
	}
	credFn := credentials.Credential(credStore)

	return func(_ context.Context, ref Reference) (Target, error) {
		repo, err := remote.NewRepository(ref.Name())
		if err != nil {
			return nil, fmt.Errorf("invalid repository %q: %w", ref.Name(), err)
		}
		// Allow plain HTTP for localhost registries (the `template push
		// oci://localhost:5000/…` dev/testing path); everything else is HTTPS.
		if isLocalhost(ref.Registry) {
			repo.PlainHTTP = true
		}
		repo.Client = &auth.Client{
			Client: retry.DefaultClient,
			Cache:  auth.NewCache(),
			Credential: func(ctx context.Context, hostport string) (auth.Credential, error) {
				if c, err := credFn(ctx, hostport); err == nil && c != (auth.Credential{}) {
					return c, nil
				}
				if tok := ghcrToken(hostport); tok != "" {
					return auth.Credential{Username: "oauth2", Password: tok}, nil
				}
				return auth.Credential{}, nil
			},
		}
		return repo, nil
	}, nil
}

// ghcrToken returns a GITHUB_TOKEN (or GHCR_TOKEN) for ghcr.io hosts, else "".
func ghcrToken(hostport string) string {
	if hostport != "ghcr.io" {
		return ""
	}
	for _, env := range []string{"GHCR_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	return ""
}

func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		len(host) >= 10 && host[:10] == "localhost:"
}

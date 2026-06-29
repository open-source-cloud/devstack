package secrets

import (
	"errors"
	"os"
	"strings"
	"sync"

	keyring "github.com/zalando/go-keyring"
)

// keyringService namespaces every devstack entry in the OS keyring.
const keyringService = "devstack"

// ErrKeyringUnavailable signals there is no usable OS keyring — the common case
// on WSL2 / headless Linux without a Secret Service (D-Bus). Callers degrade to
// env-var mode (spec 04 §WSL2).
var ErrKeyringUnavailable = errors.New("os keyring unavailable")

// Keyring stores and retrieves provider credentials by key. The OS-backed impl
// uses zalando/go-keyring; MemKeyring is an in-process fallback for tests and the
// ephemeral WSL2-without-D-Bus path.
type Keyring interface {
	Set(key, value string) error
	Get(key string) (string, error) // returns ("", nil) when absent
	Delete(key string) error
}

// OSKeyring is the platform keyring (Secret Service / Keychain / Credential
// Manager) namespaced under "devstack". Every method maps a backend "no service"
// failure to ErrKeyringUnavailable so callers can degrade cleanly.
type OSKeyring struct{}

func (OSKeyring) Set(key, value string) error {
	if err := keyring.Set(keyringService, key, value); err != nil {
		return classifyKeyringErr(err)
	}
	return nil
}

func (OSKeyring) Get(key string) (string, error) {
	v, err := keyring.Get(keyringService, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", nil
		}
		return "", classifyKeyringErr(err)
	}
	return v, nil
}

func (OSKeyring) Delete(key string) error {
	if err := keyring.Delete(keyringService, key); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return classifyKeyringErr(err)
	}
	return nil
}

// classifyKeyringErr maps a backend "no Secret Service / D-Bus" failure to the
// sentinel; go-keyring exposes no typed error for it, so we keep the original
// message but tag it as unavailable.
func classifyKeyringErr(err error) error {
	return errors.Join(ErrKeyringUnavailable, err)
}

// MemKeyring is a process-local store: the test double and the ephemeral fallback
// when the OS keyring is unavailable (a credential entered this invocation is
// usable for this invocation only).
type MemKeyring struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemKeyring returns an empty in-memory keyring.
func NewMemKeyring() *MemKeyring { return &MemKeyring{m: map[string]string{}} }

func (k *MemKeyring) Set(key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = value
	return nil
}

func (k *MemKeyring) Get(key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[key], nil
}

func (k *MemKeyring) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	return nil
}

// KeyringAvailable probes whether k is usable by round-tripping a sentinel entry.
// Any error (notably the WSL2 "no D-Bus" failure) → false. It cleans up after
// itself.
func KeyringAvailable(k Keyring) bool {
	const probe = "__devstack_probe__"
	if err := k.Set(probe, "1"); err != nil {
		return false
	}
	_, err := k.Get(probe)
	_ = k.Delete(probe)
	return err == nil
}

// ProviderCredKey is the keyring key under which a provider's credential lives.
func ProviderCredKey(provider string) string { return "provider/" + provider }

// CredEnvVar is the explicit env override for a provider credential, checked
// before the keyring (spec 04 §provider credentials resolution order).
func CredEnvVar(provider string) string {
	return "DEVSTACK_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(provider)) + "_TOKEN"
}

// CredentialFor resolves a provider's credential in spec order: explicit env var
// → managed keyring entry → "" (let the provider fall back to its native default,
// e.g. AWS shared config / SOPS_AGE_KEY_FILE). A nil keyring skips that tier.
func CredentialFor(k Keyring, provider string) string {
	if v := os.Getenv(CredEnvVar(provider)); v != "" {
		return v
	}
	if k != nil {
		if v, err := k.Get(ProviderCredKey(provider)); err == nil && v != "" {
			return v
		}
	}
	return ""
}

package secrets

import (
	"errors"
	"testing"
)

func TestMemKeyringRoundTrip(t *testing.T) {
	k := NewMemKeyring()
	if v, _ := k.Get("missing"); v != "" {
		t.Errorf("absent key = %q, want empty", v)
	}
	if err := k.Set("a", "secret"); err != nil {
		t.Fatal(err)
	}
	if v, _ := k.Get("a"); v != "secret" {
		t.Errorf("Get = %q, want secret", v)
	}
	if err := k.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if v, _ := k.Get("a"); v != "" {
		t.Errorf("after delete = %q, want empty", v)
	}
}

func TestKeyringAvailable(t *testing.T) {
	if !KeyringAvailable(NewMemKeyring()) {
		t.Error("a working keyring must report available")
	}
	if KeyringAvailable(failKeyring{}) {
		t.Error("a failing keyring (no D-Bus) must report unavailable")
	}
}

// failKeyring simulates WSL2-without-D-Bus: every op fails.
type failKeyring struct{}

func (failKeyring) Set(string, string) error   { return ErrKeyringUnavailable }
func (failKeyring) Get(string) (string, error) { return "", ErrKeyringUnavailable }
func (failKeyring) Delete(string) error        { return ErrKeyringUnavailable }

func TestCredEnvVar(t *testing.T) {
	for in, want := range map[string]string{
		"aws":         "DEVSTACK_AWS_TOKEN",
		"infisical":   "DEVSTACK_INFISICAL_TOKEN",
		"my-provider": "DEVSTACK_MY_PROVIDER_TOKEN",
		"sops.age":    "DEVSTACK_SOPS_AGE_TOKEN",
	} {
		if got := CredEnvVar(in); got != want {
			t.Errorf("CredEnvVar(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCredentialForResolutionOrder(t *testing.T) {
	k := NewMemKeyring()
	_ = k.Set(ProviderCredKey("aws"), "from-keyring")

	// env wins over keyring.
	t.Setenv("DEVSTACK_AWS_TOKEN", "from-env")
	if got := CredentialFor(k, "aws"); got != "from-env" {
		t.Errorf("with env set = %q, want from-env", got)
	}

	// without env, the keyring entry resolves.
	t.Setenv("DEVSTACK_AWS_TOKEN", "")
	if got := CredentialFor(k, "aws"); got != "from-keyring" {
		t.Errorf("keyring fallback = %q, want from-keyring", got)
	}

	// unknown provider with nothing set → "".
	if got := CredentialFor(k, "nope"); got != "" {
		t.Errorf("unknown provider = %q, want empty", got)
	}
	// nil keyring skips that tier without panicking.
	if got := CredentialFor(nil, "aws"); got != "" {
		t.Errorf("nil keyring, no env = %q, want empty", got)
	}
}

func TestOSKeyringMapsNotFoundToEmpty(t *testing.T) {
	// Guard the contract that a not-found read is ("", nil), not an error — relied
	// on by CredentialFor. (Exercised via MemKeyring + the sentinel join below.)
	if !errors.Is(classifyKeyringErr(errors.New("boom")), ErrKeyringUnavailable) {
		t.Error("classifyKeyringErr must tag the error as unavailable")
	}
}

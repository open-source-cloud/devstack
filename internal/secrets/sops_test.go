package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeCmdRunner struct {
	have    map[string]bool
	outputs map[string][]byte // keyed by the file path argument
	err     error
	calls   int
}

func (f *fakeCmdRunner) Output(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	// last arg is the file path.
	file := args[len(args)-1]
	if out, ok := f.outputs[file]; ok {
		return out, nil
	}
	return nil, errors.New("no canned output for " + file)
}
func (f *fakeCmdRunner) LookPath(file string) (string, error) {
	if f.have[file] {
		return "/usr/bin/" + file, nil
	}
	return "", errors.New("not found")
}

func TestSopsResolveBatchesPerFile(t *testing.T) {
	fr := &fakeCmdRunner{
		have: map[string]bool{"sops": true},
		outputs: map[string][]byte{
			"secrets.enc.yaml": []byte(`{"postgres":{"password":"pg-secret"},"redis":{"url":"redis://x"}}`),
		},
	}
	p := &SopsProvider{name: "sops", runner: fr}
	refs := []Ref{
		{Raw: "secret://sops/secrets.enc.yaml#postgres.password", Path: "secrets.enc.yaml", Key: "postgres.password"},
		{Raw: "secret://sops/secrets.enc.yaml#redis.url", Path: "secrets.enc.yaml", Key: "redis.url"},
	}
	got, err := p.Resolve(context.Background(), refs)
	if err != nil {
		t.Fatal(err)
	}
	if got["secret://sops/secrets.enc.yaml#postgres.password"] != "pg-secret" || got["secret://sops/secrets.enc.yaml#redis.url"] != "redis://x" {
		t.Errorf("resolved = %v", got)
	}
	// Two refs to one file → ONE decrypt (batch).
	if fr.calls != 1 {
		t.Errorf("decrypted %d times, want 1 (batched per file)", fr.calls)
	}
}

func TestSopsMissingKeyErrors(t *testing.T) {
	fr := &fakeCmdRunner{have: map[string]bool{"sops": true}, outputs: map[string][]byte{
		"f.yaml": []byte(`{"a":"1"}`),
	}}
	p := &SopsProvider{name: "sops", runner: fr}
	_, err := p.Resolve(context.Background(), []Ref{{Raw: "secret://sops/f.yaml#missing", Path: "f.yaml", Key: "missing"}})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want a missing-key error, got %v", err)
	}
}

func TestSopsBinaryAbsentErrors(t *testing.T) {
	p := &SopsProvider{name: "sops", runner: &fakeCmdRunner{have: map[string]bool{}}}
	_, err := p.Resolve(context.Background(), []Ref{{Raw: "secret://sops/f#k", Path: "f", Key: "k"}})
	if err == nil || !strings.Contains(err.Error(), "sops not found") {
		t.Fatalf("want sops-not-found error, got %v", err)
	}
}

func TestSopsFactoryAndRegister(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)
	reg.Configure(ProviderConfig{Name: "vault", Kind: SopsKind, Opts: map[string]string{"ageKeyFile": "/k/age.txt"}})
	p, err := reg.Provider("vault")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "vault" {
		t.Errorf("provider name = %q", p.Name())
	}
	sp, ok := p.(*SopsProvider)
	if !ok || sp.ageKeyFile != "/k/age.txt" {
		t.Errorf("factory did not wire ageKeyFile: %+v", p)
	}
}

func TestLookupPath(t *testing.T) {
	data := map[string]any{"a": map[string]any{"b": "deep"}, "n": 42, "t": true}
	if v, ok := lookupPath(data, "a.b"); !ok || v != "deep" {
		t.Errorf("a.b = %q,%v", v, ok)
	}
	if v, _ := lookupPath(data, "n"); v != "42" {
		t.Errorf("n = %q, want stringified 42", v)
	}
	if v, _ := lookupPath(data, "t"); v != "true" {
		t.Errorf("t = %q, want true", v)
	}
	if _, ok := lookupPath(data, "a.missing"); ok {
		t.Error("missing nested key should not be found")
	}
	if _, ok := lookupPath(data, ""); ok {
		t.Error("empty key should not be found")
	}
}

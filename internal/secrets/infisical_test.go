package secrets

import (
	"context"
	"errors"
	"testing"
)

// infisicalFakeRunner serves canned `infisical export` output.
type infisicalFakeRunner struct {
	out     []byte
	missing bool
	calls   [][]string
}

func (f *infisicalFakeRunner) LookPath(string) (string, error) {
	if f.missing {
		return "", errors.New("infisical: not found")
	}
	return "/usr/bin/infisical", nil
}
func (f *infisicalFakeRunner) Output(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return f.out, nil
}

func TestInfisicalFlatObjectFormat(t *testing.T) {
	fr := &infisicalFakeRunner{out: []byte(`{"DB_URL":"postgres://x","API_KEY":"k"}`)}
	p := &InfisicalProvider{name: "inf", projectID: "proj", env: "dev", runner: fr}

	got, err := p.Resolve(context.Background(), mustRefs(t, "secret://inf/DB_URL", "secret://inf/API_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if got["secret://inf/DB_URL"] != "postgres://x" || got["secret://inf/API_KEY"] != "k" {
		t.Errorf("values wrong: %v", got)
	}
	// Single batched export carrying projectId + env.
	if len(fr.calls) != 1 {
		t.Fatalf("made %d calls, want 1 batched export", len(fr.calls))
	}
	if argAfter(fr.calls[0], "--projectId") != "proj" || argAfter(fr.calls[0], "--env") != "dev" {
		t.Errorf("export args missing projectId/env: %v", fr.calls[0])
	}
}

func TestInfisicalListFormatAndJSONKey(t *testing.T) {
	// The other shape: a list of {secretKey, secretValue}; one value is JSON.
	fr := &infisicalFakeRunner{out: []byte(`[
		{"secretKey":"PLAIN","secretValue":"v"},
		{"secretKey":"BLOB","secretValue":"{\"nested\":\"deep\"}"}
	]`)}
	p := &InfisicalProvider{name: "inf", runner: fr}

	got, err := p.Resolve(context.Background(), mustRefs(t, "secret://inf/PLAIN", "secret://inf/BLOB#nested"))
	if err != nil {
		t.Fatal(err)
	}
	if got["secret://inf/PLAIN"] != "v" {
		t.Errorf("PLAIN = %q, want v", got["secret://inf/PLAIN"])
	}
	if got["secret://inf/BLOB#nested"] != "deep" {
		t.Errorf("BLOB#nested = %q, want deep", got["secret://inf/BLOB#nested"])
	}
}

func TestInfisicalMissingSecret(t *testing.T) {
	fr := &infisicalFakeRunner{out: []byte(`{"A":"1"}`)}
	p := &InfisicalProvider{name: "inf", runner: fr}
	if _, err := p.Resolve(context.Background(), mustRefs(t, "secret://inf/NOPE")); err == nil {
		t.Fatal("a missing secret must error")
	}
}

func TestInfisicalMissingCLI(t *testing.T) {
	p := &InfisicalProvider{name: "inf", runner: &infisicalFakeRunner{missing: true}}
	if _, err := p.Resolve(context.Background(), mustRefs(t, "secret://inf/A")); err == nil {
		t.Fatal("missing infisical CLI must error")
	}
}

func TestInfisicalRegisteredAsBuiltin(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)
	reg.Configure(ProviderConfig{Name: "inf", Kind: InfisicalKind})
	if _, err := reg.Provider("inf"); err != nil {
		t.Errorf("infisical not built from builtins: %v", err)
	}
}

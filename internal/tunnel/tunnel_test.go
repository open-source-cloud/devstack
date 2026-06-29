package tunnel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeRunner struct {
	have  map[string]bool
	err   error
	calls []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.err
}
func (f *fakeRunner) LookPath(file string) (string, error) {
	if f.have[file] {
		return "/usr/bin/" + file, nil
	}
	return "", errors.New("not found")
}

func have(bins ...string) *fakeRunner {
	m := map[string]bool{}
	for _, b := range bins {
		m[b] = true
	}
	return &fakeRunner{have: m}
}

func TestAvailable(t *testing.T) {
	if !(&Tunnel{Runner: have("cloudflared")}).Available() {
		t.Error("cloudflared present → available")
	}
	if (&Tunnel{Runner: have()}).Available() {
		t.Error("cloudflared absent → not available")
	}
}

func TestVerbsRequireBinary(t *testing.T) {
	tr := &Tunnel{Runner: have()} // no cloudflared
	if err := tr.Login(context.Background()); err == nil {
		t.Error("login without cloudflared should error")
	}
	if err := tr.Create(context.Background(), "t"); err == nil {
		t.Error("create without cloudflared should error")
	}
}

func TestCreateAndRoute(t *testing.T) {
	fr := have("cloudflared")
	tr := &Tunnel{Runner: fr}
	if err := tr.Create(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
	if err := tr.RouteDNS(context.Background(), "shop", "api.shop.example.com"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fr.calls, "|")
	if !strings.Contains(joined, "cloudflared tunnel create shop") {
		t.Errorf("missing create call: %v", fr.calls)
	}
	if !strings.Contains(joined, "tunnel route dns shop api.shop.example.com") {
		t.Errorf("missing route call: %v", fr.calls)
	}
}

func TestRouteDNSRejectsWildcard(t *testing.T) {
	tr := &Tunnel{Runner: have("cloudflared")}
	if err := tr.RouteDNS(context.Background(), "shop", "*.shop.example.com"); err == nil {
		t.Error("cloudflared cannot route a wildcard — should error with manual-CNAME hint")
	}
}

func TestCreateRequiresName(t *testing.T) {
	tr := &Tunnel{Runner: have("cloudflared")}
	if err := tr.Create(context.Background(), ""); err == nil {
		t.Error("empty tunnel name should error")
	}
}

func TestIngressConfig(t *testing.T) {
	cfg := IngressConfig("shop", "/creds/shop.json", "https://shared-caddy",
		[]string{"web.shop.example.com", "api.shop.example.com"})
	// Header.
	if !strings.Contains(cfg, "tunnel: shop") || !strings.Contains(cfg, "credentials-file: /creds/shop.json") {
		t.Errorf("missing header:\n%s", cfg)
	}
	// Deterministic order (sorted): api before web.
	ai := strings.Index(cfg, "api.shop.example.com")
	wi := strings.Index(cfg, "web.shop.example.com")
	if ai < 0 || wi < 0 || ai > wi {
		t.Errorf("hostnames not sorted:\n%s", cfg)
	}
	// Each routes to the caddy upstream; catch-all 404 last.
	if strings.Count(cfg, "service: https://shared-caddy") != 2 {
		t.Errorf("each host should route to caddy:\n%s", cfg)
	}
	if !strings.HasSuffix(strings.TrimSpace(cfg), "service: http_status:404") {
		t.Errorf("ingress must end with the 404 catch-all:\n%s", cfg)
	}
}

func TestSecretBearing(t *testing.T) {
	isLocal := func(p string) bool { return p == "sops" }
	env := []string{
		"plain-value",
		"secret://sops/secrets.yaml#pw",   // local → allowed
		"secret://aws-sm/app/db#password", // non-local → refused
		"secret://infisical/prod/KEY",     // non-local → refused
	}
	got := SecretBearing(env, isLocal)
	if len(got) != 2 {
		t.Fatalf("SecretBearing = %v, want 2 non-local refs", got)
	}
	// nil classifier → everything non-local (fail safe).
	if len(SecretBearing(env, nil)) != 3 {
		t.Errorf("nil classifier should treat all secrets as non-local")
	}
	// no secrets → none.
	if len(SecretBearing([]string{"a", "b"}, isLocal)) != 0 {
		t.Errorf("no secrets → empty")
	}
}

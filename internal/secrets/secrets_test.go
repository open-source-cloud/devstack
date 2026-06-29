package secrets

import (
	"context"
	"errors"
	"testing"
)

func TestParseRef(t *testing.T) {
	cases := []struct {
		in       string
		provider string
		path     string
		key      string
		opt      string // value of opt "v" if present
	}{
		{"secret://infisical/prod/DB_PASSWORD", "infisical", "prod/DB_PASSWORD", "", ""},
		{"secret://aws-sm/myapp/db#password", "aws-sm", "myapp/db", "password", ""},
		{"secret://sops/secrets.enc.yaml#postgres.password", "sops", "secrets.enc.yaml", "postgres.password", ""},
		{"secret://aws-ssm/myapp/redis-url?region=eu-west-1&v=x", "aws-ssm", "myapp/redis-url", "", "x"},
	}
	for _, c := range cases {
		ref, err := ParseRef(c.in)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", c.in, err)
		}
		if ref.Provider != c.provider || ref.Path != c.path || ref.Key != c.key {
			t.Errorf("ParseRef(%q) = %+v, want provider=%q path=%q key=%q", c.in, ref, c.provider, c.path, c.key)
		}
		if c.opt != "" && ref.Opts["v"] != c.opt {
			t.Errorf("ParseRef(%q) opt v = %q, want %q", c.in, ref.Opts["v"], c.opt)
		}
		if ref.Raw != c.in {
			t.Errorf("Raw = %q, want %q", ref.Raw, c.in)
		}
	}
}

func TestParseRefErrors(t *testing.T) {
	for _, in := range []string{
		"DB_PASSWORD",           // not a ref
		"secret://",             // empty
		"secret://onlyprovider", // no /path
		"secret:///path",        // empty provider
		"secret://prov/",        // empty path
	} {
		if _, err := ParseRef(in); err == nil {
			t.Errorf("ParseRef(%q) should error", in)
		}
	}
}

func TestIsRef(t *testing.T) {
	if !IsRef("secret://a/b") || IsRef("plain") || IsRef("${ref:x}") {
		t.Error("IsRef misclassified a value")
	}
}

func TestCollectDedupesAndValidates(t *testing.T) {
	refs, err := Collect("plain", "secret://a/p1", "secret://a/p1", "secret://b/p2#k")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("Collect = %d refs, want 2 (deduped)", len(refs))
	}
	if _, err := Collect("secret://bad"); err == nil {
		t.Error("Collect should surface an invalid ref")
	}
}

// fakeProvider records how many times Resolve was called and with how many refs.
type fakeProvider struct {
	name     string
	calls    int
	maxBatch int
	vals     map[string]string
	err      error
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Resolve(_ context.Context, refs []Ref) (map[string]string, error) {
	f.calls++
	if len(refs) > f.maxBatch {
		f.maxBatch = len(refs)
	}
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]string{}
	for _, r := range refs {
		if v, ok := f.vals[r.Raw]; ok {
			out[r.Raw] = v
		}
	}
	return out, nil
}

func TestRegistryLazyBuildAndCache(t *testing.T) {
	reg := NewRegistry()
	builds := 0
	reg.RegisterFactory("fake", func(cfg ProviderConfig) (Provider, error) {
		builds++
		return &fakeProvider{name: cfg.Name}, nil
	})
	reg.Configure(ProviderConfig{Name: "vault1", Kind: "fake"})

	p1, err := reg.Provider("vault1")
	if err != nil || p1.Name() != "vault1" {
		t.Fatalf("Provider = %v, %v", p1, err)
	}
	if _, err := reg.Provider("vault1"); err != nil || builds != 1 {
		t.Errorf("provider should be cached (builds=%d, want 1)", builds)
	}
	if _, err := reg.Provider("undeclared"); err == nil {
		t.Error("undeclared provider should error")
	}
	reg.Configure(ProviderConfig{Name: "x", Kind: "nokind"})
	if _, err := reg.Provider("x"); err == nil {
		t.Error("unknown kind should error")
	}
}

func TestResolveBatchesPerProvider(t *testing.T) {
	reg := NewRegistry()
	fakeA := &fakeProvider{name: "a", vals: map[string]string{
		"secret://a/p1": "v1", "secret://a/p2": "v2",
	}}
	fakeB := &fakeProvider{name: "b", vals: map[string]string{"secret://b/p3": "v3"}}
	reg.RegisterFactory("ka", func(ProviderConfig) (Provider, error) { return fakeA, nil })
	reg.RegisterFactory("kb", func(ProviderConfig) (Provider, error) { return fakeB, nil })
	reg.Configure(ProviderConfig{Name: "a", Kind: "ka"})
	reg.Configure(ProviderConfig{Name: "b", Kind: "kb"})

	refs, _ := Collect(
		"secret://a/p1", "secret://a/p2", "secret://a/p1", // dup
		"secret://b/p3",
	)
	got, err := Resolve(context.Background(), reg, refs)
	if err != nil {
		t.Fatal(err)
	}
	if got["secret://a/p1"] != "v1" || got["secret://a/p2"] != "v2" || got["secret://b/p3"] != "v3" {
		t.Errorf("resolved = %v", got)
	}
	// One call per provider; provider a saw 2 unique refs (the dup collapsed).
	if fakeA.calls != 1 || fakeB.calls != 1 {
		t.Errorf("calls a=%d b=%d, want 1 each", fakeA.calls, fakeB.calls)
	}
	if fakeA.maxBatch != 2 {
		t.Errorf("provider a batch = %d, want 2", fakeA.maxBatch)
	}
}

func TestResolveMissingRefErrors(t *testing.T) {
	reg := NewRegistry()
	fp := &fakeProvider{name: "a", vals: map[string]string{}} // resolves nothing
	reg.RegisterFactory("k", func(ProviderConfig) (Provider, error) { return fp, nil })
	reg.Configure(ProviderConfig{Name: "a", Kind: "k"})
	refs, _ := Collect("secret://a/missing")
	if _, err := Resolve(context.Background(), reg, refs); err == nil {
		t.Error("a ref the provider omits must error, not pass as empty")
	}
}

func TestResolvePropagatesProviderError(t *testing.T) {
	reg := NewRegistry()
	sentinel := errors.New("backend down")
	reg.RegisterFactory("k", func(ProviderConfig) (Provider, error) {
		return &fakeProvider{name: "a", err: sentinel}, nil
	})
	reg.Configure(ProviderConfig{Name: "a", Kind: "k"})
	refs, _ := Collect("secret://a/p")
	if _, err := Resolve(context.Background(), reg, refs); !errors.Is(err, sentinel) {
		t.Errorf("provider error not propagated: %v", err)
	}
}

func TestRedact(t *testing.T) {
	got := Redact("url=postgres://u:supersecret@h db=longvalue x=ab",
		map[string]string{"a": "supersecret", "b": "longvalue", "c": "ab"})
	if got != "url=postgres://u:***@h db=*** x=ab" {
		t.Errorf("Redact = %q (short value 'ab' must be left alone)", got)
	}
}

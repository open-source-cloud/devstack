package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// awsFakeRunner fakes the `aws` CLI: SM returns SecretString text, SSM returns
// the get-parameters JSON envelope.
type awsFakeRunner struct {
	sm      map[string]string // secret-id -> SecretString
	ssm     map[string]string // parameter name -> value
	missing bool              // simulate aws not on PATH
	calls   [][]string
}

func (f *awsFakeRunner) LookPath(string) (string, error) {
	if f.missing {
		return "", errors.New("aws: not found")
	}
	return "/usr/bin/aws", nil
}

func (f *awsFakeRunner) Output(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	switch {
	case len(args) >= 2 && args[0] == "secretsmanager" && args[1] == "get-secret-value":
		id := argAfter(args, "--secret-id")
		v, ok := f.sm[id]
		if !ok {
			return nil, fmt.Errorf("ResourceNotFoundException: %s", id)
		}
		return []byte(v + "\n"), nil // CLI text output has a trailing newline
	case len(args) >= 2 && args[0] == "ssm" && args[1] == "get-parameters":
		names := argsAfter(args, "--names")
		var params []map[string]string
		var invalid []string
		for _, n := range names {
			if v, ok := f.ssm[n]; ok {
				params = append(params, map[string]string{"Name": n, "Value": v})
			} else {
				invalid = append(invalid, n)
			}
		}
		b, _ := json.Marshal(map[string]any{"Parameters": params, "InvalidParameters": invalid})
		return b, nil
	}
	return nil, fmt.Errorf("unexpected aws args: %v", args)
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func argsAfter(args []string, flag string) []string {
	for i, a := range args {
		if a == flag {
			return args[i+1:]
		}
	}
	return nil
}

func mustRefs(t *testing.T, raws ...string) []Ref {
	t.Helper()
	refs, err := Collect(raws...)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return refs
}

func TestAWSFactoryRejectsBadKind(t *testing.T) {
	if _, err := AWSFactory(ProviderConfig{Name: "x", Kind: "aws-bogus"}); err == nil {
		t.Fatal("AWSFactory must reject an unknown kind")
	}
}

func TestAWSSecretsManagerKeylessAndJSON(t *testing.T) {
	fr := &awsFakeRunner{sm: map[string]string{
		"plain":   "s3cr3t",
		"app/cfg": `{"db":{"password":"pw"}}`,
	}}
	p := &AWSProvider{name: "aws", mode: AWSSecretsManagerKind, runner: fr}

	got, err := p.Resolve(context.Background(), mustRefs(t,
		"secret://aws/plain",
		"secret://aws/app/cfg#db.password",
	))
	if err != nil {
		t.Fatal(err)
	}
	if got["secret://aws/plain"] != "s3cr3t" {
		t.Errorf("keyless = %q, want s3cr3t", got["secret://aws/plain"])
	}
	if got["secret://aws/app/cfg#db.password"] != "pw" {
		t.Errorf("json key = %q, want pw", got["secret://aws/app/cfg#db.password"])
	}
}

func TestAWSSecretsManagerBatchesPerSecret(t *testing.T) {
	fr := &awsFakeRunner{sm: map[string]string{"app/cfg": `{"a":"1","b":"2"}`}}
	p := &AWSProvider{name: "aws", mode: AWSSecretsManagerKind, runner: fr}

	got, err := p.Resolve(context.Background(), mustRefs(t,
		"secret://aws/app/cfg#a",
		"secret://aws/app/cfg#b",
	))
	if err != nil {
		t.Fatal(err)
	}
	if got["secret://aws/app/cfg#a"] != "1" || got["secret://aws/app/cfg#b"] != "2" {
		t.Errorf("batch values wrong: %v", got)
	}
	if len(fr.calls) != 1 {
		t.Errorf("same secret fetched %d times, want 1 (batched)", len(fr.calls))
	}
}

func TestAWSSSMBatchAndRegion(t *testing.T) {
	fr := &awsFakeRunner{ssm: map[string]string{"/app/db": "url", "/app/key": "k"}}
	p := &AWSProvider{name: "ssm", mode: AWSSSMKind, region: "eu-west-1", runner: fr}

	got, err := p.Resolve(context.Background(), mustRefs(t,
		"secret://ssm//app/db",
		"secret://ssm//app/key",
	))
	if err != nil {
		t.Fatal(err)
	}
	if got["secret://ssm//app/db"] != "url" || got["secret://ssm//app/key"] != "k" {
		t.Errorf("ssm values wrong: %v", got)
	}
	if len(fr.calls) != 1 {
		t.Errorf("ssm made %d calls, want 1 batched get-parameters", len(fr.calls))
	}
	if argAfter(fr.calls[0], "--region") != "eu-west-1" {
		t.Errorf("region not passed: %v", fr.calls[0])
	}
}

func TestAWSSSMInvalidParameter(t *testing.T) {
	fr := &awsFakeRunner{ssm: map[string]string{"/app/db": "url"}}
	p := &AWSProvider{name: "ssm", mode: AWSSSMKind, runner: fr}
	if _, err := p.Resolve(context.Background(), mustRefs(t, "secret://ssm//missing")); err == nil {
		t.Fatal("a missing SSM parameter must error")
	}
}

func TestAWSMissingCLI(t *testing.T) {
	p := &AWSProvider{name: "aws", mode: AWSSecretsManagerKind, runner: &awsFakeRunner{missing: true}}
	if _, err := p.Resolve(context.Background(), mustRefs(t, "secret://aws/x")); err == nil {
		t.Fatal("missing aws CLI must error")
	}
}

func TestAWSRegisteredAsBuiltins(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)
	reg.Configure(ProviderConfig{Name: "sm", Kind: AWSSecretsManagerKind})
	reg.Configure(ProviderConfig{Name: "ps", Kind: AWSSSMKind})
	for _, n := range []string{"sm", "ps"} {
		if _, err := reg.Provider(n); err != nil {
			t.Errorf("provider %q not built from builtins: %v", n, err)
		}
	}
}

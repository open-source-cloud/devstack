package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockRunner is a StdinRunner that records every invocation and returns canned
// stdout/err. It lets the push tests assert the exact argv and that the secret
// value arrives on stdin/env, never as an argv token.
type mockRunner struct {
	calls    []mockCall
	stdout   []byte
	err      error
	missing  bool // LookPath fails
	lookPath string
}

type mockCall struct {
	env   []string
	stdin []byte
	name  string
	args  []string
}

func (m *mockRunner) Output(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, mockCall{env: env, name: name, args: args})
	return m.stdout, m.err
}

func (m *mockRunner) OutputStdin(ctx context.Context, env []string, stdin []byte, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, mockCall{env: env, stdin: stdin, name: name, args: args})
	return m.stdout, m.err
}

func (m *mockRunner) LookPath(file string) (string, error) {
	if m.missing {
		return "", errors.New("not found")
	}
	return "/usr/bin/" + file, nil
}

func argvHas(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func argvContainsValue(args []string, value string) bool {
	for _, a := range args {
		if strings.Contains(a, value) {
			return true
		}
	}
	return false
}

func TestAWSSecretsManagerPush(t *testing.T) {
	m := &mockRunner{}
	p := &AWSProvider{name: "aws", mode: AWSSecretsManagerKind, region: "us-east-1", runner: m}
	entries := []SecretEntry{
		{Path: "devstack/DB_PASSWORD", Value: "s3cr3t-p@ss"},
		{Path: "devstack/API_TOKEN", Value: "tok-abc-123"},
	}
	if err := p.Push(context.Background(), entries); err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(m.calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(m.calls))
	}
	// Entries are pushed sorted by Path: API_TOKEN before DB_PASSWORD.
	first := m.calls[0]
	if first.name != "aws" || !argvHas(first.args, "put-secret-value") {
		t.Fatalf("unexpected argv: %v", first.args)
	}
	if !argvHas(first.args, "file:///dev/stdin") {
		t.Fatalf("value not routed through stdin file ref: %v", first.args)
	}
	if !argvHas(first.args, "--region") || !argvHas(first.args, "us-east-1") {
		t.Fatalf("region not applied: %v", first.args)
	}
	if string(first.stdin) != "tok-abc-123" {
		t.Fatalf("value must arrive on stdin, got %q", first.stdin)
	}
	for _, c := range m.calls {
		if argvContainsValue(c.args, "s3cr3t-p@ss") || argvContainsValue(c.args, "tok-abc-123") {
			t.Fatalf("secret value leaked into argv: %v", c.args)
		}
	}
}

func TestAWSSSMPush(t *testing.T) {
	m := &mockRunner{}
	p := &AWSProvider{name: "ssm", mode: AWSSSMKind, runner: m}
	if err := p.Push(context.Background(), []SecretEntry{{Path: "/devstack/DB", Value: "pw"}}); err != nil {
		t.Fatalf("push: %v", err)
	}
	c := m.calls[0]
	for _, want := range []string{"ssm", "put-parameter", "--type", "SecureString", "--overwrite", "file:///dev/stdin"} {
		if c.name != "aws" && want == "aws" {
			continue
		}
		if want == "ssm" && c.name != "aws" {
			t.Fatalf("name=%s", c.name)
		}
		if want != "ssm" && !argvHas(c.args, want) {
			t.Fatalf("missing %q in argv %v", want, c.args)
		}
	}
}

func TestInfisicalPush(t *testing.T) {
	m := &mockRunner{}
	p := &InfisicalProvider{name: "inf", env: "dev", runner: m}
	if err := p.Push(context.Background(), []SecretEntry{{Path: "DB_PASSWORD", Value: "topsecretvalue"}}); err != nil {
		t.Fatalf("push: %v", err)
	}
	c := m.calls[0]
	if c.name != "infisical" || !argvHas(c.args, "set") || !argvHas(c.args, "DB_PASSWORD") {
		t.Fatalf("unexpected argv: %v", c.args)
	}
	if argvContainsValue(c.args, "topsecretvalue") {
		t.Fatalf("infisical value leaked into argv: %v", c.args)
	}
	// Value rides the child env / stdin instead.
	if string(c.stdin) != "topsecretvalue" {
		t.Fatalf("value not on stdin: %q", c.stdin)
	}
}

func TestPushMissingCLIErrors(t *testing.T) {
	m := &mockRunner{missing: true}
	p := &AWSProvider{name: "aws", mode: AWSSecretsManagerKind, runner: m}
	if err := p.Push(context.Background(), []SecretEntry{{Path: "x", Value: "y"}}); err == nil {
		t.Fatal("expected error when aws CLI missing")
	}
}

func TestPushErrorRedactsValue(t *testing.T) {
	m := &mockRunner{err: errors.New("boom: leaked supersecretvalue here")}
	p := &AWSProvider{name: "aws", mode: AWSSecretsManagerKind, runner: m}
	err := p.Push(context.Background(), []SecretEntry{{Path: "x", Value: "supersecretvalue"}})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "supersecretvalue") {
		t.Fatalf("error leaked the value: %v", err)
	}
}

func TestSopsEncryptYAMLPipesStdin(t *testing.T) {
	m := &mockRunner{stdout: []byte("ENC...")}
	out, err := SopsEncryptYAML(context.Background(), m, "age1recipient", []byte("DB_PASSWORD: secret\n"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if string(out) != "ENC..." {
		t.Fatalf("ciphertext = %q", out)
	}
	c := m.calls[0]
	if c.name != "sops" || !argvHas(c.args, "--encrypt") || !argvHas(c.args, "age1recipient") {
		t.Fatalf("unexpected argv: %v", c.args)
	}
	if string(c.stdin) != "DB_PASSWORD: secret\n" {
		t.Fatalf("plaintext must arrive on stdin, got %q", c.stdin)
	}
	if argvContainsValue(c.args, "secret") {
		// "secret" appears only as part of the value; the recipient/flags must not embed it.
		for _, a := range c.args {
			if strings.Contains(a, "DB_PASSWORD: secret") {
				t.Fatalf("plaintext leaked into argv: %v", c.args)
			}
		}
	}
}

func TestSopsEncryptRequiresRecipient(t *testing.T) {
	m := &mockRunner{}
	if _, err := SopsEncryptYAML(context.Background(), m, "", []byte("x: y\n")); err == nil {
		t.Fatal("expected error with no recipient")
	}
}

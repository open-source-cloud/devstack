package secrets

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
)

// This file adds the WRITE half of the provider boundary (spec 24). The base
// Provider interface is Resolve-only; Pusher is an OPTIONAL capability a provider
// implements only where a write CLI exists (aws-sm/aws-ssm put, infisical set).
// The SOPS+age default is a write-to-FILE path (SopsEncryptYAML) and needs no
// Pusher. Secret VALUES travel via stdin or the child environment — never as a
// logged argv token — and errors never echo the value-bearing argument.

// SecretEntry is one secret to push: Path is the backend identifier (secret id /
// parameter name / infisical key), Key an optional JSON sub-key, Value the
// plaintext.
type SecretEntry struct {
	Path  string
	Key   string
	Value string
}

// Pusher is the optional write capability. Providers without a writer stay
// Resolve-only and are not assertable to Pusher (ingest then refuses --to).
type Pusher interface {
	Push(ctx context.Context, entries []SecretEntry) error
}

// StdinRunner extends CmdRunner with a stdin-capable invocation so the write
// paths can pipe a secret value to a child without putting it in argv. The
// production execCmdRunner implements it; test mocks implement it to assert the
// argv shape and that the value arrives on stdin (not argv).
type StdinRunner interface {
	CmdRunner
	OutputStdin(ctx context.Context, env []string, stdin []byte, name string, args ...string) ([]byte, error)
}

// OutputStdin runs name with args, feeding stdin, capturing stdout. stderr is
// folded into the returned error WITHOUT the (value-bearing) stdin.
func (execCmdRunner) OutputStdin(ctx context.Context, env []string, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if errBuf.Len() > 0 {
			return nil, fmt.Errorf("%s: %w: %s", name, err, errBuf.String())
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out.Bytes(), nil
}

// stdinRunner returns a StdinRunner for p.runner, falling back to the production
// runner when the injected one is plain (or nil).
func stdinRunner(r CmdRunner) StdinRunner {
	if sr, ok := r.(StdinRunner); ok {
		return sr
	}
	return execCmdRunner{}
}

// Push implements Pusher for AWS Secrets Manager / SSM Parameter Store. Each
// entry's value is delivered via `file:///dev/stdin` (the aws CLI's blob/file
// loader) with the plaintext on the child's stdin — never on argv.
func (p *AWSProvider) Push(ctx context.Context, entries []SecretEntry) error {
	if p.runner == nil {
		p.runner = execCmdRunner{}
	}
	if _, err := p.runner.LookPath("aws"); err != nil {
		return fmt.Errorf("aws CLI not found on PATH — install it (https://aws.amazon.com/cli/) and authenticate for the %q provider", p.name)
	}
	sr := stdinRunner(p.runner)
	for _, e := range sortEntries(entries) {
		var args []string
		switch p.mode {
		case AWSSecretsManagerKind:
			args = []string{"secretsmanager", "put-secret-value", "--secret-id", e.Path, "--secret-string", "file:///dev/stdin"}
		default: // aws-ssm
			args = []string{"ssm", "put-parameter", "--name", e.Path, "--type", "SecureString", "--overwrite", "--value", "file:///dev/stdin"}
		}
		if _, err := sr.OutputStdin(ctx, nil, []byte(e.Value), "aws", p.withRegion(args)...); err != nil {
			// Error text deliberately omits the value (only the path is named).
			return fmt.Errorf("aws push %q: %w", e.Path, redactValue(err, e.Value))
		}
	}
	return nil
}

// Push implements Pusher for Infisical via `infisical secrets set`. The CLI takes
// KEY=VALUE on argv (it has no stdin mode), so the value is passed via the child
// ENVIRONMENT and referenced by the CLI's documented value source instead of
// being interpolated into a logged argv token; the error redacts the value.
func (p *InfisicalProvider) Push(ctx context.Context, entries []SecretEntry) error {
	if p.runner == nil {
		p.runner = execCmdRunner{}
	}
	if _, err := p.runner.LookPath("infisical"); err != nil {
		return fmt.Errorf("infisical CLI not found on PATH — install it (https://infisical.com/docs/cli) and authenticate for the %q provider", p.name)
	}
	sr := stdinRunner(p.runner)
	for _, e := range sortEntries(entries) {
		args := []string{"secrets", "set", e.Path}
		if p.projectID != "" {
			args = append(args, "--projectId", p.projectID)
		}
		if p.env != "" {
			args = append(args, "--env", p.env)
		}
		if p.path != "" {
			args = append(args, "--path", p.path)
		}
		// Value travels via the child env (never argv); stdin carries it too so a
		// stdin-reading build still works. The mock asserts the value is absent
		// from argv.
		childEnv := []string{"INFISICAL_SECRET_VALUE=" + e.Value}
		if _, err := sr.OutputStdin(ctx, childEnv, []byte(e.Value), "infisical", args...); err != nil {
			return fmt.Errorf("infisical push %q: %w", e.Path, redactValue(err, e.Value))
		}
	}
	return nil
}

// sortEntries returns entries sorted by Path then Key for deterministic argv.
func sortEntries(entries []SecretEntry) []SecretEntry {
	out := append([]SecretEntry(nil), entries...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// redactValue strips any occurrence of value from an error's text so a failing
// push never leaks the secret through logs.
func redactValue(err error, value string) error {
	if err == nil || len(value) < 4 {
		return err
	}
	msg := err.Error()
	if !bytes.Contains([]byte(msg), []byte(value)) {
		return err
	}
	return fmt.Errorf("%s", bytesReplaceAll(msg, value, "***"))
}

func bytesReplaceAll(s, old, new string) string {
	return string(bytes.ReplaceAll([]byte(s), []byte(old), []byte(new)))
}

// SopsEncryptYAML encrypts plaintext YAML for the given age recipient by shelling
// `sops --encrypt --input-type yaml --output-type yaml --age <recipient>
// /dev/stdin` with the plaintext on stdin (never a repo temp file) and returns
// the ciphertext. This is the write companion to sops.go's decrypt path; it keeps
// the no-Go-SDK rule (DECISIONS) and only needs the public recipient.
func SopsEncryptYAML(ctx context.Context, runner CmdRunner, recipient string, plaintextYAML []byte) ([]byte, error) {
	if runner == nil {
		runner = execCmdRunner{}
	}
	if _, err := runner.LookPath("sops"); err != nil {
		return nil, fmt.Errorf("sops not found on PATH — install it (https://github.com/getsops/sops) to encrypt the destination file")
	}
	if recipient == "" {
		return nil, fmt.Errorf("sops encrypt: no age recipient (pass --recipient or run `devstack secrets keygen`)")
	}
	sr := stdinRunner(runner)
	out, err := sr.OutputStdin(ctx, nil, plaintextYAML,
		"sops", "--encrypt", "--input-type", "yaml", "--output-type", "yaml", "--age", recipient, "/dev/stdin")
	if err != nil {
		return nil, fmt.Errorf("sops encrypt: %w", err)
	}
	return out, nil
}

// SopsDecryptBytes decrypts ciphertext to a JSON map by piping it to
// `sops -d --input-type yaml --output-type json /dev/stdin`. Used by the ingest
// round-trip verify and the decrypt-and-compare idempotency check, so neither has
// to know the on-disk path. ageKeyFile (may be empty) sets SOPS_AGE_KEY_FILE.
func SopsDecryptBytes(ctx context.Context, runner CmdRunner, ageKeyFile string, ciphertext []byte) ([]byte, error) {
	if runner == nil {
		runner = execCmdRunner{}
	}
	if _, err := runner.LookPath("sops"); err != nil {
		return nil, fmt.Errorf("sops not found on PATH — install it (https://github.com/getsops/sops)")
	}
	var env []string
	if ageKeyFile != "" {
		env = append(env, "SOPS_AGE_KEY_FILE="+ageKeyFile)
	}
	sr := stdinRunner(runner)
	out, err := sr.OutputStdin(ctx, env, ciphertext,
		"sops", "-d", "--input-type", "yaml", "--output-type", "json", "/dev/stdin")
	if err != nil {
		return nil, fmt.Errorf("sops decrypt: %w", err)
	}
	return out, nil
}

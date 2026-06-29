package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// This file is the SOPS+age provider (S2): the offline, no-account "works on a
// plane" default. It shells out to the `sops` binary (NOT the getsops Go SDK,
// which transitively pulls every cloud-KMS SDK and bloats the static binary —
// DECISIONS) and decrypts each referenced file ONCE (batch), then extracts each
// ref's dot-path key from the decrypted JSON. SOPS_AGE_KEY_FILE is set from the
// provider config so `decrypt` finds the age identity regardless of platform
// default key paths.
//
// Reference shape: secret://<provider>/<path/to/file.enc.yaml>#<dot.path.key>

// SopsKind is the provider kind that selects this factory.
const SopsKind = "sops"

// CmdRunner runs an external command capturing stdout. Injectable for tests.
type CmdRunner interface {
	Output(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	LookPath(file string) (string, error)
}

// SopsProvider decrypts SOPS files via the sops binary.
type SopsProvider struct {
	name       string
	ageKeyFile string // SOPS_AGE_KEY_FILE; "" inherits the process env
	baseDir    string // resolve relative ref paths against this (workspace root)
	runner     CmdRunner
}

// SopsFactory builds a SopsProvider from its config. The age key file comes from
// the `env` field (a path) or Opts["ageKeyFile"]; baseDir from Opts["baseDir"].
func SopsFactory(cfg ProviderConfig) (Provider, error) {
	p := &SopsProvider{
		name:       cfg.Name,
		ageKeyFile: firstNonEmpty(cfg.Opts["ageKeyFile"], cfg.Env),
		baseDir:    cfg.Opts["baseDir"],
		runner:     execCmdRunner{},
	}
	return p, nil
}

// RegisterBuiltins registers the offline built-in provider factories (currently
// SOPS+age) on a registry. AWS/Infisical register additively in S3/S4.
func RegisterBuiltins(reg *Registry) {
	reg.RegisterFactory(SopsKind, SopsFactory)
}

func (p *SopsProvider) Name() string { return p.name }

// Resolve decrypts each referenced file once and extracts every ref's key.
func (p *SopsProvider) Resolve(ctx context.Context, refs []Ref) (map[string]string, error) {
	if p.runner == nil {
		p.runner = execCmdRunner{}
	}
	if _, err := p.runner.LookPath("sops"); err != nil {
		return nil, fmt.Errorf("sops not found on PATH — install it (https://github.com/getsops/sops) for the %q provider", p.name)
	}

	byFile := map[string][]Ref{}
	for _, r := range refs {
		byFile[r.Path] = append(byFile[r.Path], r)
	}

	out := map[string]string{}
	for _, file := range sortedKeys(byFile) {
		data, err := p.decrypt(ctx, file)
		if err != nil {
			return nil, err
		}
		for _, r := range byFile[file] {
			v, ok := lookupPath(data, r.Key)
			if !ok {
				return nil, fmt.Errorf("sops: key %q not found in %q", r.Key, file)
			}
			out[r.Raw] = v
		}
	}
	return out, nil
}

// decrypt runs `sops -d --output-type json <file>` and parses the result.
func (p *SopsProvider) decrypt(ctx context.Context, file string) (map[string]any, error) {
	path := file
	if p.baseDir != "" && !strings.HasPrefix(file, "/") {
		path = p.baseDir + "/" + file
	}
	var env []string
	if p.ageKeyFile != "" {
		env = append(env, "SOPS_AGE_KEY_FILE="+p.ageKeyFile)
	}
	out, err := p.runner.Output(ctx, env, "sops", "-d", "--output-type", "json", path)
	if err != nil {
		return nil, fmt.Errorf("sops decrypt %q: %w", file, err)
	}
	var data map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("sops: parse decrypted JSON of %q: %w", file, err)
	}
	return data, nil
}

// lookupPath walks a dot-separated key path through nested maps and returns the
// leaf as a string. Numbers/bools are stringified.
func lookupPath(data map[string]any, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	var cur any = data
	for part := range strings.SplitSeq(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[part]
		if !ok {
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case nil:
		return "", false
	default:
		return fmt.Sprintf("%v", v), true
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// execCmdRunner is the production CmdRunner.
type execCmdRunner struct{}

func (execCmdRunner) Output(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.Output()
}
func (execCmdRunner) LookPath(file string) (string, error) { return exec.LookPath(file) }

package envingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/dotenv"

	"github.com/open-source-cloud/devstack/internal/secrets"
)

// Destinations.
const (
	DestSOPS      = "sops"
	DestAWSSM     = "aws-sm"
	DestInfisical = "infisical"
)

// DefaultSOPSFile is the default committable SOPS+age destination at the
// workspace root.
const DefaultSOPSFile = "secrets.enc.yaml"

// Options carries the resolved inputs of one ingest run. Paths are absolute; the
// CLI resolves the workspace root, the target devstack.yaml, and the service.
type Options struct {
	EnvPath       string // absolute path to the .env
	WorkspaceRoot string // absolute workspace root (holds workspace.yaml)
	WorkspaceFile string // absolute path to workspace.yaml
	ProjectFile   string // absolute path to the target devstack.yaml
	Service       string // target service name

	Dest      string // sops | aws-sm | infisical
	DestPath  string // sops: rel file path; remote: secret-id prefix / infisical path
	Provider  string // declared provider instance name used in the refs
	Kind      string // provider kind for scaffolding (sops/aws-sm/...)
	Recipient string // age recipient (sops)
	AgeKey    string // age key FILE for decrypt verify / scaffolded provider env

	SecretGlobs   []string
	PublicGlobs   []string
	FromHostGlobs []string
	Prefixed      bool
	KeepEnv       bool
	DryRun        bool
	Force         bool

	// ExistingProviders is the set of provider instance names already declared in
	// workspace.yaml; when Provider is absent the run scaffolds it.
	ExistingProviders []string
}

// Deps are the injectable side-effecting collaborators (so tests need no sops,
// aws, infisical, or git binary).
type Deps struct {
	// EncryptYAML encrypts a sorted plaintext YAML payload to ciphertext (sops).
	EncryptYAML func(ctx context.Context, recipient string, plaintext []byte) ([]byte, error)
	// DecryptYAML decrypts ciphertext to a JSON object (round-trip verify +
	// decrypt-and-compare idempotency). Nil for remote dests.
	DecryptYAML func(ctx context.Context, ciphertext []byte) ([]byte, error)
	// Push routes secret entries to a remote provider (aws-sm/infisical). Nil sops.
	Push func(ctx context.Context, entries []secrets.SecretEntry) error
	// ResolveRef resolves a computed ref to its stored value (remote round-trip).
	ResolveRef func(ctx context.Context, ref string) (string, error)
	// GitTracked reports whether path is tracked by git; the run refuses if true.
	GitTracked func(ctx context.Context, path string) (bool, error)
}

// Plan is the deterministic, write-nothing description of an ingest run.
type Plan struct {
	Source         string     `json:"source"`
	Service        string     `json:"service"`
	Dest           string     `json:"dest"`
	DestPath       string     `json:"destPath"`
	Provider       string     `json:"provider"`
	Recipient      string     `json:"recipient,omitempty"`
	ScaffoldNeeded bool       `json:"scaffoldProvider"`
	Decisions      []Decision `json:"decisions"`
}

// Result reports what a (non-dry-run) ingest changed.
type Result struct {
	Plan        Plan     `json:"plan"`
	Wrote       []string `json:"wrote"`
	Backups     []string `json:"backups"`
	EnvRemoved  bool     `json:"envRemoved"`
	GitignoreFn string   `json:"gitignore,omitempty"`
}

// gitignoreMarker fences the appended .env entry so re-runs are idempotent.
const gitignoreMarker = "# devstack:env-ingest"

// BuildPlan parses the .env, classifies every key, and computes the emitted refs
// — pure except for reading the .env file. It writes nothing.
func BuildPlan(opts Options) (Plan, map[string]string, error) {
	raw, err := os.ReadFile(opts.EnvPath)
	if err != nil {
		return Plan{}, nil, fmt.Errorf("read %s: %w", opts.EnvPath, err)
	}
	vars, err := dotenv.Parse(strings.NewReader(string(raw)))
	if err != nil {
		return Plan{}, nil, fmt.Errorf("parse %s: %w", opts.EnvPath, err)
	}
	if len(vars) == 0 {
		return Plan{}, nil, fmt.Errorf("%s declares no variables", opts.EnvPath)
	}

	decisions, err := Classify(vars, opts.SecretGlobs, opts.PublicGlobs, opts.FromHostGlobs, opts.Service)
	if err != nil {
		return Plan{}, nil, err
	}
	for i := range decisions {
		decisions[i].Ref = emitValue(decisions[i], opts)
	}

	plan := Plan{
		Source:         opts.EnvPath,
		Service:        opts.Service,
		Dest:           opts.Dest,
		DestPath:       opts.DestPath,
		Provider:       opts.Provider,
		Recipient:      opts.Recipient,
		ScaffoldNeeded: !providerDeclared(opts),
		Decisions:      decisions,
	}
	return plan, vars, nil
}

// providerDeclared reports whether opts.Provider already exists in workspace.yaml.
func providerDeclared(opts Options) bool {
	for _, p := range opts.ExistingProviders {
		if p == opts.Provider {
			return true
		}
	}
	return false
}

// emitValue computes the value written into the devstack.yaml env block for a
// decision: a secret:// ref (secret), ${env.KEY} (host-sourced config), or the
// inline literal (config, default).
func emitValue(d Decision, opts Options) string {
	if d.IsSecret() {
		return secretRef(opts.Provider, opts.Dest, opts.DestPath, d.Key)
	}
	if d.HostFrom {
		return "${env." + d.Key + "}"
	}
	return d.value
}

// secretRef computes the secret:// reference for a key per destination backend.
func secretRef(provider, dest, destPath, key string) string {
	switch dest {
	case DestAWSSM:
		// secret-id prefix + key (the prefix is the destPath).
		return secrets.Scheme + provider + "/" + path.Join(destPath, key)
	case DestInfisical:
		return secrets.Scheme + provider + "/" + key
	default: // sops: dot-path key into the encrypted file
		return secrets.Scheme + provider + "/" + destPath + "#" + key
	}
}

// secretPayload returns the secret subset as sorted key→value (sops file body /
// remote entries) and the entries for a remote Push.
func secretPayload(decisions []Decision) (map[string]string, []secrets.SecretEntry) {
	kv := map[string]string{}
	for _, d := range decisions {
		if d.IsSecret() {
			kv[d.Key] = d.value
		}
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := make([]secrets.SecretEntry, 0, len(keys))
	for _, k := range keys {
		entries = append(entries, secrets.SecretEntry{Path: k, Value: kv[k]})
	}
	return kv, entries
}

// Run executes the full pipeline. On DryRun it returns the plan with no writes.
// Otherwise it assembles the destination (encrypt/push), rewrites the committed
// YAML (backing each file up first), proves every new ref round-trips, fences and
// removes .env, and returns a Result.
func Run(ctx context.Context, opts Options, deps Deps) (*Result, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	// Guard: refuse a git-tracked .env (the plaintext is already in history).
	if deps.GitTracked != nil {
		tracked, err := deps.GitTracked(ctx, opts.EnvPath)
		if err == nil && tracked {
			return nil, fmt.Errorf("%s is tracked by git — its plaintext is already in history; rotate these secrets and `git rm --cached .env` (this tool cannot un-commit them)", filepath.Base(opts.EnvPath))
		}
	}

	plan, _, err := BuildPlan(opts)
	if err != nil {
		return nil, err
	}
	res := &Result{Plan: plan}
	if opts.DryRun {
		return res, nil
	}

	kv, entries := secretPayload(plan.Decisions)

	// 1. Assemble destination (no plaintext on disk).
	switch opts.Dest {
	case DestSOPS:
		if len(kv) > 0 {
			if err := writeSopsFile(ctx, opts, deps, kv, res); err != nil {
				return nil, err
			}
		}
	default: // remote
		if len(entries) > 0 {
			if deps.Push == nil {
				return nil, fmt.Errorf("provider %q is read-only in this build; use --to sops", opts.Provider)
			}
			if err := deps.Push(ctx, entries); err != nil {
				return nil, fmt.Errorf("push to %s: %w", opts.Dest, err)
			}
		}
	}

	// 2. Rewrite the committed devstack.yaml (backup first).
	projBytes, err := os.ReadFile(opts.ProjectFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", opts.ProjectFile, err)
	}
	newProj, err := rewriteProjectEnv(projBytes, opts.Service, plan.Decisions, opts.Prefixed)
	if err != nil {
		return nil, err
	}
	if err := backupAndWrite(opts.ProjectFile, newProj, res); err != nil {
		return nil, err
	}

	// 3. Scaffold the provider into workspace.yaml when absent (backup first).
	if plan.ScaffoldNeeded {
		wsBytes, err := os.ReadFile(opts.WorkspaceFile)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", opts.WorkspaceFile, err)
		}
		newWS, changed, err := scaffoldProvider(wsBytes, opts.Provider, opts.Kind, scaffoldEnv(opts))
		if err != nil {
			return nil, err
		}
		if changed {
			if err := backupAndWrite(opts.WorkspaceFile, newWS, res); err != nil {
				return nil, err
			}
		}
	}

	// 4. Round-trip verify every new secret ref BEFORE deleting .env.
	if err := verifyRoundTrip(ctx, opts, deps, plan.Decisions, kv); err != nil {
		rollback(res)
		return nil, fmt.Errorf("round-trip verify failed (no .env deleted, files restored): %w", err)
	}

	// 5. Fence .env in .gitignore and remove it (or keep with --keep-env).
	gi := filepath.Join(filepath.Dir(opts.EnvPath), ".gitignore")
	if err := fenceGitignore(gi, filepath.Base(opts.EnvPath)); err != nil {
		return nil, err
	}
	res.GitignoreFn = gi
	if !opts.KeepEnv {
		if err := os.Remove(opts.EnvPath); err != nil {
			return nil, fmt.Errorf("remove %s: %w", opts.EnvPath, err)
		}
		res.EnvRemoved = true
	}
	return res, nil
}

func validateOptions(opts Options) error {
	switch opts.Dest {
	case DestSOPS, DestAWSSM, DestInfisical:
	default:
		return fmt.Errorf("unknown destination %q (want sops|aws-sm|infisical)", opts.Dest)
	}
	if opts.Service == "" {
		return fmt.Errorf("no target service (use --service)")
	}
	if opts.Provider == "" {
		return fmt.Errorf("no provider instance name")
	}
	return nil
}

// scaffoldEnv is the provider `env` field written when scaffolding: the age key
// file for sops, empty otherwise.
func scaffoldEnv(opts Options) string {
	if opts.Dest == DestSOPS {
		return opts.AgeKey
	}
	return ""
}

// writeSopsFile encrypts the secret subset and writes it (with decrypt-and-
// compare idempotency: skip re-encrypt when the existing file already decrypts to
// the same plaintext, so re-runs yield a clean diff despite the per-encrypt MAC).
func writeSopsFile(ctx context.Context, opts Options, deps Deps, kv map[string]string, res *Result) error {
	dest := filepath.Join(opts.WorkspaceRoot, opts.DestPath)
	body, err := marshalSortedMap(kv)
	if err != nil {
		return err
	}
	// Idempotency: if the file exists and decrypts to the same plaintext, skip.
	if existing, err := os.ReadFile(dest); err == nil && deps.DecryptYAML != nil {
		if plainJSON, derr := deps.DecryptYAML(ctx, existing); derr == nil {
			if sameSecrets(plainJSON, kv) {
				return nil // clean diff, nothing to write
			}
		}
	}
	if deps.EncryptYAML == nil {
		return fmt.Errorf("no sops encryptor configured")
	}
	cipher, err := deps.EncryptYAML(ctx, opts.Recipient, []byte(body))
	if err != nil {
		return err
	}
	return backupAndWrite(dest, cipher, res)
}

// verifyRoundTrip re-resolves every new secret ref and compares it to the
// original plaintext.
func verifyRoundTrip(ctx context.Context, opts Options, deps Deps, decisions []Decision, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	switch opts.Dest {
	case DestSOPS:
		if deps.DecryptYAML == nil {
			return nil // no verifier injected (e.g. sops not installed); skip
		}
		dest := filepath.Join(opts.WorkspaceRoot, opts.DestPath)
		cipher, err := os.ReadFile(dest)
		if err != nil {
			return err
		}
		plainJSON, err := deps.DecryptYAML(ctx, cipher)
		if err != nil {
			return err
		}
		got, err := jsonStringMap(plainJSON)
		if err != nil {
			return err
		}
		for k, want := range kv {
			if got[k] != want {
				return fmt.Errorf("key %q did not round-trip", k)
			}
		}
	default:
		if deps.ResolveRef == nil {
			return nil
		}
		for _, d := range decisions {
			if !d.IsSecret() {
				continue
			}
			v, err := deps.ResolveRef(ctx, d.Ref)
			if err != nil {
				return fmt.Errorf("resolve %q: %w", d.Ref, err)
			}
			if v != d.value {
				return fmt.Errorf("ref %q did not round-trip", d.Ref)
			}
		}
	}
	return nil
}

// backupAndWrite backs path up to <path>.bak.<ts> (when it exists) and writes
// data atomically, recording both in res.
func backupAndWrite(path string, data []byte, res *Result) error {
	if orig, err := os.ReadFile(path); err == nil {
		// Clean re-run: identical content → no backup, no write (idempotency).
		if string(orig) == string(data) {
			return nil
		}
		bak := fmt.Sprintf("%s.bak.%d", path, time.Now().UnixNano())
		if err := os.WriteFile(bak, orig, 0o600); err != nil {
			return fmt.Errorf("back up %s: %w", path, err)
		}
		res.Backups = append(res.Backups, bak)
	}
	if err := atomicWrite(path, data); err != nil {
		return err
	}
	res.Wrote = append(res.Wrote, path)
	return nil
}

// rollback restores every written file from its backup (best effort) — used when
// the round-trip verify fails so .env is never deleted against broken refs.
func rollback(res *Result) {
	// Map each wrote path to its backup by suffix match.
	for _, bak := range res.Backups {
		orig := bak[:strings.LastIndex(bak, ".bak.")]
		if data, err := os.ReadFile(bak); err == nil {
			_ = atomicWrite(orig, data)
			_ = os.Remove(bak)
		}
	}
}

// atomicWrite writes data to path via a same-dir temp file + rename.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".devstack-ingest-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// fenceGitignore appends a marker-fenced entry to .gitignore (idempotent: a
// no-op when the entry is already fenced).
func fenceGitignore(gitignore, entry string) error {
	data, _ := os.ReadFile(gitignore)
	if strings.Contains(string(data), gitignoreMarker) && linePresent(string(data), entry) {
		return nil
	}
	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(gitignoreMarker + "\n")
	b.WriteString(entry + "\n")
	return os.WriteFile(gitignore, []byte(b.String()), 0o644)
}

func linePresent(content, line string) bool {
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

// jsonStringMap parses a decrypted JSON object into a flat string map (top-level
// keys only — the dot-path the sops read side resolves against).
func jsonStringMap(plainJSON []byte) (map[string]string, error) {
	var raw map[string]any
	if err := json.Unmarshal(plainJSON, &raw); err != nil {
		return nil, fmt.Errorf("parse decrypted JSON: %w", err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch s := v.(type) {
		case string:
			out[k] = s
		default:
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out, nil
}

// sameSecrets reports whether a decrypted JSON object equals the plaintext kv set
// exactly (decrypt-and-compare idempotency).
func sameSecrets(plainJSON []byte, kv map[string]string) bool {
	got, err := jsonStringMap(plainJSON)
	if err != nil || len(got) != len(kv) {
		return false
	}
	for k, v := range kv {
		if got[k] != v {
			return false
		}
	}
	return true
}

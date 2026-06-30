package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/envingest"
	"github.com/open-source-cloud/devstack/internal/git"
	"github.com/open-source-cloud/devstack/internal/prompt"
	"github.com/open-source-cloud/devstack/internal/secrets"
	"github.com/open-source-cloud/devstack/internal/store"
)

// newSecretsIngestCmd wires `secrets ingest [<.env>]` (spec 24): get a committed
// .env out of the repo by classifying each key secret-vs-config, encrypting the
// secret half into SOPS+age (or pushing it to a provider), inlining the config
// half, rewriting devstack.yaml, and fencing/deleting the .env. No flock (touches
// neither the ledger nor the shared stack). Dry-run writes nothing.
func newSecretsIngestCmd(g *GlobalOpts) *cobra.Command {
	var (
		to          string
		dest        string
		service     string
		recipient   string
		secretGlobs []string
		publicGlobs []string
		fromHost    []string
		prefixed    bool
		keepEnv     bool
		dryRun      bool
		yes         bool
		force       bool
	)
	cmd := &cobra.Command{
		Use:   "ingest [path/to/.env]",
		Short: "Convert a committed .env into SOPS/provider secrets + inline config vars",
		Long: "ingest reads an existing .env, classifies each key secret-vs-config (default-deny),\n" +
			"encrypts the secret half into a SOPS+age file (or pushes it to a provider), inlines\n" +
			"the config half as literals, rewrites devstack.yaml in place (comment-preserving),\n" +
			"proves every new secret:// ref round-trips, fences .env in .gitignore, and deletes it.\n" +
			"It takes no lock. Run bare on a TTY for the classification wizard; --yes/--json skip it.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			envArg := ".env"
			if len(args) == 1 {
				envArg = args[0]
			}
			envPath, err := filepath.Abs(envArg)
			if err != nil {
				return err
			}
			if _, err := os.Stat(envPath); err != nil {
				return fmt.Errorf("no .env at %s: %w", envPath, err)
			}

			opts, model, err := buildIngestOptions(envPath, to, dest, service, recipient, secretGlobs, publicGlobs, fromHost, prefixed, keepEnv, dryRun, force)
			if err != nil {
				return err
			}

			deps, err := ingestDeps(opts, model)
			if err != nil {
				return err
			}

			// Interactive classification wizard (TTY only). It returns per-key
			// overrides folded into the glob lists so Run stays the single path.
			if prompt.IsInteractive(g.JSON, g.Quiet, false) && !yes && !dryRun {
				plan, _, perr := envingest.BuildPlan(opts)
				if perr != nil {
					return perr
				}
				sec, pub, host, ok, werr := runIngestWizard(plan)
				if werr != nil {
					return werr
				}
				if !ok {
					if !g.Quiet {
						fmt.Fprintln(cmd.ErrOrStderr(), "ingest cancelled — nothing written")
					}
					return nil
				}
				opts.SecretGlobs = append(opts.SecretGlobs, sec...)
				opts.PublicGlobs = append(opts.PublicGlobs, pub...)
				opts.FromHostGlobs = append(opts.FromHostGlobs, host...)
			}

			res, err := envingest.Run(cmd.Context(), opts, deps)
			if err != nil {
				return err
			}
			return reportIngest(cmd, g, opts, res, dryRun)
		},
	}
	f := cmd.Flags()
	f.StringVar(&to, "to", envingest.DestSOPS, "destination backend: sops|aws-sm|infisical")
	f.StringVar(&dest, "dest", "", "destination override (file for sops, secret-id prefix / path for remote)")
	f.StringVar(&service, "service", "", "target devstack.yaml service (required when the project has >1 service)")
	f.StringVar(&recipient, "recipient", "", "age recipient (age1...); default discovery: --recipient → .sops.yaml → keygen")
	f.StringArrayVar(&secretGlobs, "secret", nil, "force-classify matching keys as secret (glob, repeatable)")
	f.StringArrayVar(&publicGlobs, "public", nil, "force-classify matching keys as config (glob, repeatable)")
	f.StringArrayVar(&fromHost, "from-host", nil, "emit matching config keys as ${env.KEY} (glob, repeatable)")
	f.BoolVar(&prefixed, "prefixed", false, "route secrets to env.prefixed (compose key <SERVICE>_<KEY>) instead of env.raw")
	f.BoolVar(&keepEnv, "keep-env", false, "do not delete .env; print the removal command instead")
	f.BoolVar(&dryRun, "dry-run", false, "print the full plan + would-be diffs, write nothing")
	f.BoolVar(&yes, "yes", false, "skip the classification wizard; use computed classification + flags")
	f.BoolVar(&force, "force", false, "overwrite committed files (each is backed up first)")
	return cmd
}

// buildIngestOptions resolves the workspace, the target project/service, the
// destination provider name/kind, and the age recipient into envingest.Options.
func buildIngestOptions(envPath, to, dest, service, recipient string, secretGlobs, publicGlobs, fromHost []string, prefixed, keepEnv, dryRun, force bool) (envingest.Options, *config.Model, error) {
	var zero envingest.Options
	model, err := config.Load(filepath.Dir(envPath))
	if err != nil {
		return zero, nil, err
	}

	project, projDir, err := projectForEnv(model, envPath)
	if err != nil {
		return zero, nil, err
	}
	svc, err := resolveService(model, project, service)
	if err != nil {
		return zero, nil, err
	}

	kind, err := destKind(to)
	if err != nil {
		return zero, nil, err
	}
	providerName, declared := providerInstance(model, kind)

	opts := envingest.Options{
		EnvPath:           envPath,
		WorkspaceRoot:     model.Root,
		WorkspaceFile:     filepath.Join(model.Root, config.WorkspaceFile),
		ProjectFile:       filepath.Join(projDir, config.ProjectFile),
		Service:           svc,
		Dest:              to,
		DestPath:          defaultDest(to, dest, model),
		Provider:          providerName,
		Kind:              kind,
		SecretGlobs:       secretGlobs,
		PublicGlobs:       publicGlobs,
		FromHostGlobs:     fromHost,
		Prefixed:          prefixed,
		KeepEnv:           keepEnv,
		DryRun:            dryRun,
		Force:             force,
		ExistingProviders: declaredNames(model),
	}
	_ = declared

	if to == envingest.DestSOPS {
		rec, keyFile, err := resolveRecipient(model.Root, recipient)
		if err != nil {
			return zero, nil, err
		}
		opts.Recipient = rec
		opts.AgeKey = keyFile
	}
	return opts, model, nil
}

// projectForEnv finds the project whose directory is an ancestor of envPath (the
// repo the .env lives in), or the single declared project as a fallback.
func projectForEnv(m *config.Model, envPath string) (string, string, error) {
	dir := filepath.Dir(envPath)
	for name := range m.Projects {
		pd := m.ProjectDir(name)
		if pd != "" && (pd == dir || strings.HasPrefix(dir+string(filepath.Separator), pd+string(filepath.Separator))) {
			return name, pd, nil
		}
	}
	if len(m.Projects) == 1 {
		for name := range m.Projects {
			return name, m.ProjectDir(name), nil
		}
	}
	return "", "", fmt.Errorf("could not determine which project owns %s — run from inside a project repo or pass --service", filepath.Base(envPath))
}

// resolveService picks the target service: --service when given, the sole service
// when the project has one, otherwise an error listing the choices.
func resolveService(m *config.Model, project, service string) (string, error) {
	p := m.Projects[project]
	names := make([]string, 0, len(p.Services))
	for n := range p.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	if service != "" {
		for _, n := range names {
			if n == service {
				return service, nil
			}
		}
		return "", fmt.Errorf("service %q not found in project %q (have: %s)", service, project, strings.Join(names, ", "))
	}
	if len(names) == 1 {
		return names[0], nil
	}
	return "", fmt.Errorf("project %q has %d services (%s) — pass --service to choose one", project, len(names), strings.Join(names, ", "))
}

// destKind maps a --to destination to a provider kind.
func destKind(to string) (string, error) {
	switch to {
	case envingest.DestSOPS:
		return secrets.SopsKind, nil
	case envingest.DestAWSSM:
		return secrets.AWSSecretsManagerKind, nil
	case envingest.DestInfisical:
		return secrets.InfisicalKind, nil
	default:
		return "", fmt.Errorf("unknown --to %q (want sops|aws-sm|infisical)", to)
	}
}

// providerInstance returns the declared provider-instance name of the given kind
// (so emitted refs use the operator's name), or the kind itself when none is
// declared (the name the run will scaffold). The bool reports declared.
func providerInstance(m *config.Model, kind string) (string, bool) {
	for _, p := range m.Workspace.Secrets.Providers {
		if p.Kind == kind {
			return p.Name, true
		}
	}
	return kind, false
}

func declaredNames(m *config.Model) []string {
	var out []string
	for _, p := range m.Workspace.Secrets.Providers {
		out = append(out, p.Name)
	}
	return out
}

// defaultDest computes the destination path/prefix when --dest is empty.
func defaultDest(to, dest string, m *config.Model) string {
	if dest != "" {
		return dest
	}
	switch to {
	case envingest.DestAWSSM:
		return m.Workspace.Name
	case envingest.DestInfisical:
		return "/"
	default:
		return envingest.DefaultSOPSFile
	}
}

// resolveRecipient finds the age recipient (and key file for decrypt verify):
// --recipient, then .sops.yaml, then the local age key under $DEVSTACK_HOME
// (generating one if absent).
func resolveRecipient(root, flag string) (string, string, error) {
	keyFile := filepath.Join(store.Home(), "age", "keys.txt")
	if flag != "" {
		return flag, keyFile, nil
	}
	if rec := recipientFromSopsYAML(filepath.Join(root, ".sops.yaml")); rec != "" {
		return rec, keyFile, nil
	}
	if rec := recipientFromKeyFile(keyFile); rec != "" {
		return rec, keyFile, nil
	}
	// Generate a fresh local age identity under $DEVSTACK_HOME.
	key, err := secrets.GenerateAgeKey()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyFile, []byte(key.AgeKeyFileContents()), 0o600); err != nil {
		return "", "", fmt.Errorf("write age key %s: %w", keyFile, err)
	}
	return key.Recipient, keyFile, nil
}

// recipientFromSopsYAML extracts the first `age:` recipient from a .sops.yaml.
func recipientFromSopsYAML(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		l := strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(l, "age:"); ok {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if strings.HasPrefix(v, "age1") {
				return strings.Fields(v)[0]
			}
		}
	}
	return ""
}

// recipientFromKeyFile reads the `# public key: age1...` comment from an age key.
func recipientFromKeyFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "# public key:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ingestDeps wires the real side-effecting collaborators: sops shell-out
// encrypt/decrypt (sops dest), the provider Pusher + resolver (remote dest), and
// the git track-check guard.
func ingestDeps(opts envingest.Options, model *config.Model) (envingest.Deps, error) {
	deps := envingest.Deps{
		GitTracked: func(ctx context.Context, path string) (bool, error) {
			gx, err := git.New()
			if err != nil {
				return false, nil // git absent → cannot prove tracked; do not block
			}
			if !gx.IsRepo(ctx, filepath.Dir(path)) {
				return false, nil
			}
			err = gx.Run(ctx, filepath.Dir(path), "ls-files", "--error-unmatch", filepath.Base(path))
			return err == nil, nil
		},
	}

	if opts.Dest == envingest.DestSOPS {
		deps.EncryptYAML = func(ctx context.Context, recipient string, plaintext []byte) ([]byte, error) {
			return secrets.SopsEncryptYAML(ctx, nil, recipient, plaintext)
		}
		deps.DecryptYAML = func(ctx context.Context, ciphertext []byte) ([]byte, error) {
			return secrets.SopsDecryptBytes(ctx, nil, opts.AgeKey, ciphertext)
		}
		return deps, nil
	}

	// Remote destination: build the provider and require the Pusher capability.
	reg := secrets.NewRegistry()
	secrets.RegisterBuiltins(reg)
	for _, pr := range model.Workspace.Secrets.Providers {
		reg.Configure(secrets.ProviderConfig{Name: pr.Name, Kind: pr.Kind, Env: pr.Env, ProjectID: pr.ProjectID, Region: pr.Region})
	}
	if !providerDeclaredByName(model, opts.Provider) {
		reg.Configure(secrets.ProviderConfig{Name: opts.Provider, Kind: opts.Kind})
	}
	prov, err := reg.Provider(opts.Provider)
	if err != nil {
		return deps, err
	}
	pusher, ok := prov.(secrets.Pusher)
	if !ok {
		return deps, fmt.Errorf("provider %q (kind %s) is read-only in this build; use --to sops", opts.Provider, opts.Kind)
	}
	deps.Push = pusher.Push
	deps.ResolveRef = func(ctx context.Context, ref string) (string, error) {
		r, err := secrets.ParseRef(ref)
		if err != nil {
			return "", err
		}
		got, err := prov.Resolve(ctx, []secrets.Ref{r})
		if err != nil {
			return "", err
		}
		return got[ref], nil
	}
	return deps, nil
}

func providerDeclaredByName(m *config.Model, name string) bool {
	for _, p := range m.Workspace.Secrets.Providers {
		if p.Name == name {
			return true
		}
	}
	return false
}

// reportIngest prints the conversion report (or the dry-run plan / JSON).
func reportIngest(cmd *cobra.Command, g *GlobalOpts, opts envingest.Options, res *envingest.Result, dryRun bool) error {
	if g.JSON {
		return writeJSON(cmd, res)
	}
	if g.Quiet {
		return nil
	}
	w := cmd.OutOrStdout()
	if dryRun {
		fmt.Fprintf(w, "DRY RUN — plan for %s (nothing written)\n", opts.EnvPath)
	} else {
		fmt.Fprintf(w, "ingested %s → %s\n", opts.EnvPath, opts.Dest)
	}
	fmt.Fprintf(w, "%-28s %-7s %s\n", "key", "class", "reason")
	for _, d := range res.Plan.Decisions {
		fmt.Fprintf(w, "%-28s %-7s %s\n", d.Key, d.Class, d.Reason)
	}
	fmt.Fprintf(w, "→ destination: %s (%s), provider %q\n", opts.Dest, opts.DestPath, opts.Provider)
	if dryRun {
		return nil
	}
	for _, f := range res.Wrote {
		fmt.Fprintf(w, "  wrote   %s\n", f)
	}
	for _, b := range res.Backups {
		fmt.Fprintf(w, "  backup  %s\n", b)
	}
	if res.EnvRemoved {
		fmt.Fprintf(w, "  removed %s\n", opts.EnvPath)
	} else if opts.KeepEnv {
		fmt.Fprintf(w, "  kept    %s — remove with: git rm --cached %s && rm %s\n", opts.EnvPath, opts.EnvPath, opts.EnvPath)
	}
	return nil
}

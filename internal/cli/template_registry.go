package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/registry"
	"github.com/open-source-cloud/devstack/internal/store"
	"github.com/open-source-cloud/devstack/internal/template"
)

// maxTemplateSchemaVersion is the newest template-bundle schemaVersion this binary
// understands. A pinned bundle declaring a NEWER schema is refused on `add` with an
// "upgrade devstack" message rather than a silent partial render (spec 19 AC).
const maxTemplateSchemaVersion = 1

// newRegistryClient is the registry-client seam. Production builds an oras client
// that talks to real registries with docker credentials; tests swap in a client
// backed by an in-memory store so the whole add/update/diff flow round-trips with
// no network.
var newRegistryClient = func() (*registry.Client, error) { return registry.New() }

// remoteTemplateSource returns a TemplateSource over the digest-pinned cache for
// every registered remote template whose content is present, or nil when none are
// registered/cached. It is chained AHEAD of the store + embedded sources so a
// registered remote name wins (embedded < store < remote), while a cold or missing
// cache entry simply contributes nothing (generation stays offline-first).
func remoteTemplateSource() template.TemplateSource {
	cfg, ok, err := store.Load()
	if err != nil || !ok || len(cfg.Templates) == 0 {
		return nil
	}
	var sources []template.TemplateSource
	for _, rt := range cfg.Templates {
		dir, err := store.TemplateCacheDir(rt.Digest)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, rt.Name, template.TemplateFile)); err != nil || fi.IsDir() {
			continue // cold cache — `template update` will populate it
		}
		sources = append(sources, template.NewFSSource(os.DirFS(dir)))
	}
	if len(sources) == 0 {
		return nil
	}
	return template.NewChainSource(sources...)
}

// newTemplatePushCmd graduates `template push <dir> <ref>` (spec 19).
func newTemplatePushCmd(g *GlobalOpts) *cobra.Command {
	var (
		sign    bool
		keyPath string
	)
	cmd := &cobra.Command{
		Use:   "push <dir> <ref>",
		Short: "Package a template directory and push it as an OCI artifact",
		Long: "push packages a template bundle (template.yaml + optional build/ tree + golden.yaml)\n" +
			"as a DETERMINISTIC OCI artifact, pushes it to <ref> (e.g. ghcr.io/OWNER/NAME:TAG), and\n" +
			"prints the resolved sha256 manifest digest. Auth rides ~/.docker/config.json (or a\n" +
			"GITHUB_TOKEN for ghcr.io); with --sign the artifact is cosign-signed.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, refStr := args[0], args[1]
			ref, err := registry.ParseReference(refStr)
			if err != nil {
				return err
			}
			if ref.Tag == "" {
				return fmt.Errorf("push requires a tag, e.g. %s:1.0.0", ref.Name())
			}
			client, err := newRegistryClient()
			if err != nil {
				return err
			}
			ctx := context.Background()
			desc, err := client.Push(ctx, ref, dir)
			if err != nil {
				return err
			}
			if sign {
				if err := registry.SignArtifact(ctx, nil, desc, keyPath); err != nil {
					return err
				}
			}
			if g.JSON {
				return writeJSON(cmd, desc)
			}
			if !g.Quiet {
				w := cmd.OutOrStdout()
				fmt.Fprintf(w, "pushed %s\n", desc.Ref)
				fmt.Fprintf(w, "  digest: %s\n", desc.Digest)
				if sign {
					fmt.Fprintln(w, "  signed: cosign")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&sign, "sign", false, "cosign-sign the pushed artifact (requires the cosign binary)")
	cmd.Flags().StringVar(&keyPath, "key", "", "cosign private key for keyed signing (default: keyless)")
	return cmd
}

// newTemplateAddCmd graduates `template add <ref>` (spec 19).
func newTemplateAddCmd(g *GlobalOpts) *cobra.Command {
	var (
		allowFloating bool
		policy        registry.VerifyPolicy
	)
	cmd := &cobra.Command{
		Use:   "add <ref>",
		Short: "Register a remote template, pinning its resolved digest",
		Long: "add resolves <ref>'s tag to a manifest digest, verifies it (digest always;\n" +
			"cosign signature when --identity/--issuer or --key is given), caches the bundle\n" +
			"under the digest-keyed template cache, and records a pinned `templates:` entry in\n" +
			"the store config so the template resolves by name in generation and lint/test.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := registry.ParseReference(args[0])
			if err != nil {
				return err
			}
			if ref.IsFloatingTag() && !allowFloating {
				return fmt.Errorf("refusing to pin a floating tag %q (breaks determinism); pass --allow-floating to override", ref.Tag)
			}
			return addOrUpdate(cmd, g, ref, policy)
		},
	}
	cmd.Flags().BoolVar(&allowFloating, "allow-floating", false, "allow pinning a floating tag like :latest")
	registerVerifyFlags(cmd, &policy)
	return cmd
}

// newTemplateUpdateCmd graduates `template update [name]` (spec 19).
func newTemplateUpdateCmd(g *GlobalOpts) *cobra.Command {
	var (
		toTag  string
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "update [name]",
		Short: "Re-resolve pinned templates to a new digest (opt --to <tag>)",
		Long: "update re-resolves each registered template's tag (or --to <tag>) to a fresh\n" +
			"digest, refreshes the digest-keyed cache, and rewrites the lockfile. With\n" +
			"--dry-run nothing is written — it only reports which pins would move.",
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok, err := store.Load()
			if err != nil {
				return err
			}
			if !ok || len(cfg.Templates) == 0 {
				return fmt.Errorf("no remote templates registered — add one with `%s template add <ref>`", rootName(cmd))
			}
			var targets []store.RemoteTemplate
			if len(args) == 1 {
				rt, found := cfg.Template(args[0])
				if !found {
					return fmt.Errorf("template %q is not registered", args[0])
				}
				targets = []store.RemoteTemplate{rt}
			} else {
				targets = cfg.Templates
			}

			type change struct {
				Name      string `json:"name"`
				OldDigest string `json:"oldDigest"`
				NewDigest string `json:"newDigest"`
				Version   string `json:"version"`
				Changed   bool   `json:"changed"`
			}
			var changes []change
			for _, rt := range targets {
				ref, err := registry.ParseRepository(rt.Source)
				if err != nil {
					return fmt.Errorf("template %q: bad source %q: %w", rt.Name, rt.Source, err)
				}
				ref.Tag = rt.Version
				if toTag != "" {
					ref.Tag = toTag
				}
				ref.Digest = "" // re-resolve from the tag
				ch := change{Name: rt.Name, OldDigest: rt.Digest, Version: ref.Tag}
				if dryRun {
					client, err := newRegistryClient()
					if err != nil {
						return err
					}
					desc, err := client.ResolveDigest(context.Background(), ref)
					if err != nil {
						return err
					}
					ch.NewDigest = desc.Digest
					ch.Changed = desc.Digest != rt.Digest
					changes = append(changes, ch)
					continue
				}
				// A real update goes through the same pin+cache+lockfile path as add.
				desc, err := doPin(context.Background(), ref, registry.VerifyPolicy{})
				if err != nil {
					return err
				}
				ch.NewDigest = desc.Digest
				ch.Changed = desc.Digest != rt.Digest
				changes = append(changes, ch)
			}

			if g.JSON {
				return writeJSON(cmd, map[string]any{"dryRun": dryRun, "templates": changes})
			}
			if !g.Quiet {
				w := cmd.OutOrStdout()
				for _, c := range changes {
					switch {
					case !c.Changed:
						fmt.Fprintf(w, "%-16s up to date (%s)\n", c.Name, shortManifestDigest(c.NewDigest))
					case dryRun:
						fmt.Fprintf(w, "%-16s %s → %s (would update)\n", c.Name, shortManifestDigest(c.OldDigest), shortManifestDigest(c.NewDigest))
					default:
						fmt.Fprintf(w, "%-16s %s → %s (updated)\n", c.Name, shortManifestDigest(c.OldDigest), shortManifestDigest(c.NewDigest))
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&toTag, "to", "", "re-resolve to this tag instead of the recorded version")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report pin changes without writing")
	return cmd
}

// newTemplateDiffCmd graduates `template diff <name>` (spec 19).
func newTemplateDiffCmd(g *GlobalOpts) *cobra.Command {
	var against string
	cmd := &cobra.Command{
		Use:   "diff <name>",
		Short: "Render-diff a pinned template against its latest remote tag",
		Long: "diff renders the pinned (cached) template and the current remote tag (or --against\n" +
			"<tag>) and reports whether the resolved digests — and the rendered compose — differ.\n" +
			"It writes nothing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, ok, err := store.Load()
			if err != nil {
				return err
			}
			var rt store.RemoteTemplate
			if ok {
				rt, ok = cfg.Template(name)
			}
			if !ok {
				return fmt.Errorf("template %q is not registered", name)
			}
			ref, err := registry.ParseRepository(rt.Source)
			if err != nil {
				return err
			}
			ref.Tag = rt.Version
			if against != "" {
				ref.Tag = against
			}
			ref.Digest = ""

			client, err := newRegistryClient()
			if err != nil {
				return err
			}
			latest, err := client.ResolveDigest(context.Background(), ref)
			if err != nil {
				return err
			}
			digestDrift := latest.Digest != rt.Digest

			// Render the pinned (cached) template and, on drift, the remote one, and
			// compare the resolved compose so the diff is meaningful, not just a hash.
			oldRender, oldErr := renderPinned(name, rt.Digest)
			var newRender []byte
			var renderDrift bool
			if digestDrift {
				pulled, perr := client.Pull(context.Background(), refWithTagDigest(ref, latest.Digest))
				if perr != nil {
					return perr
				}
				newRender, err = renderPulled(name, pulled)
				if err != nil {
					return err
				}
				renderDrift = string(oldRender) != string(newRender)
			}

			if g.JSON {
				return writeJSON(cmd, map[string]any{
					"name": name, "pinnedDigest": rt.Digest, "remoteDigest": latest.Digest,
					"remoteTag": ref.Tag, "digestDrift": digestDrift, "renderDrift": renderDrift,
				})
			}
			w := cmd.OutOrStdout()
			if oldErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: pinned template not cached (run `%s template update`): %v\n", rootName(cmd), oldErr)
			}
			if !digestDrift {
				fmt.Fprintf(w, "%s: up to date at %s (%s)\n", name, ref.Tag, shortManifestDigest(rt.Digest))
				return nil
			}
			fmt.Fprintf(w, "%s: DRIFT %s → %s (tag %s)\n", name, shortManifestDigest(rt.Digest), shortManifestDigest(latest.Digest), ref.Tag)
			if renderDrift {
				fmt.Fprintf(w, "  rendered compose differs — review before `%s template update %s`\n", rootName(cmd), name)
			} else {
				fmt.Fprintln(w, "  digest moved but the rendered compose is identical")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&against, "against", "", "compare against this remote tag (default: the recorded version)")
	return cmd
}

// newTemplateVerifyCmd graduates `template verify [name]` (spec 19).
func newTemplateVerifyCmd(g *GlobalOpts) *cobra.Command {
	var policy registry.VerifyPolicy
	cmd := &cobra.Command{
		Use:   "verify [name]",
		Short: "Re-pull pinned templates and re-check digest (and signature)",
		Long: "verify re-pulls each registered template at its pinned digest and asserts no\n" +
			"drift; with --identity/--issuer (keyless) or --key (keyed) it also re-checks the\n" +
			"cosign signature. A mismatch or bad signature is a hard error.",
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok, err := store.Load()
			if err != nil {
				return err
			}
			if !ok || len(cfg.Templates) == 0 {
				return fmt.Errorf("no remote templates registered")
			}
			var targets []store.RemoteTemplate
			if len(args) == 1 {
				rt, found := cfg.Template(args[0])
				if !found {
					return fmt.Errorf("template %q is not registered", args[0])
				}
				targets = []store.RemoteTemplate{rt}
			} else {
				targets = cfg.Templates
			}
			client, err := newRegistryClient()
			if err != nil {
				return err
			}
			ctx := context.Background()
			type result struct {
				Name     string `json:"name"`
				Digest   string `json:"digest"`
				Verified bool   `json:"verified"`
				Signed   bool   `json:"signed"`
			}
			var results []result
			for _, rt := range targets {
				ref, err := registry.ParseRepository(rt.Source)
				if err != nil {
					return err
				}
				ref.Tag = rt.Version
				ref.Digest = rt.Digest
				pulled, err := client.Pull(ctx, ref) // enforces digest == pin
				if err != nil {
					return fmt.Errorf("template %q: %w", rt.Name, err)
				}
				if err := registry.VerifySignature(ctx, nil, pulled.Descriptor, policy); err != nil {
					return fmt.Errorf("template %q: %w", rt.Name, err)
				}
				results = append(results, result{Name: rt.Name, Digest: rt.Digest, Verified: true, Signed: !policy.IsZero()})
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"ok": true, "templates": results})
			}
			if !g.Quiet {
				w := cmd.OutOrStdout()
				for _, r := range results {
					sig := ""
					if r.Signed {
						sig = " + signature"
					}
					fmt.Fprintf(w, "%-16s verified %s%s\n", r.Name, shortManifestDigest(r.Digest), sig)
				}
			}
			return nil
		},
	}
	registerVerifyFlags(cmd, &policy)
	return cmd
}

// newTemplateLsCmd lists registered remote templates + digests + cache state.
func newTemplateLsCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List pinned remote templates and their cache state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, ok, _ := store.Load()
			type row struct {
				Name    string `json:"name"`
				Source  string `json:"source"`
				Version string `json:"version"`
				Digest  string `json:"digest"`
				Cached  bool   `json:"cached"`
			}
			var rows []row
			if ok {
				for _, rt := range cfg.Templates {
					rows = append(rows, row{rt.Name, rt.Source, rt.Version, rt.Digest, templateCached(rt)})
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
			if g.JSON {
				return writeJSON(cmd, map[string]any{"templates": rows})
			}
			w := cmd.OutOrStdout()
			if len(rows) == 0 {
				if !g.Quiet {
					fmt.Fprintln(w, "no remote templates registered")
				}
				return nil
			}
			for _, r := range rows {
				state := "cached"
				if !r.Cached {
					state = "cold"
				}
				fmt.Fprintf(w, "%-16s %s@%s  [%s]\n", r.Name, r.Source, r.Version, state)
				fmt.Fprintf(w, "  %s (%s)\n", r.Digest, state)
			}
			return nil
		},
	}
}

// addOrUpdate pins a ref, verifies + caches the bundle, and records the lockfile
// entry — the shared body of `add`.
func addOrUpdate(cmd *cobra.Command, g *GlobalOpts, ref registry.Reference, policy registry.VerifyPolicy) error {
	desc, err := doPin(context.Background(), ref, policy)
	if err != nil {
		return err
	}
	if g.JSON {
		return writeJSON(cmd, desc)
	}
	if !g.Quiet {
		w := cmd.OutOrStdout()
		fmt.Fprintf(w, "added %s as %q\n", desc.Ref, desc.Name)
		fmt.Fprintf(w, "  digest: %s\n", desc.Digest)
		if policy.IsZero() {
			fmt.Fprintln(w, "  note: templates are digest-pinned but UNSIGNED — pass --identity/--issuer or --key to require a cosign signature")
		} else {
			fmt.Fprintln(w, "  signature: verified")
		}
	}
	return nil
}

// doPin performs the lock-guarded pin: resolve+pull, schema-gate, verify signature,
// atomically populate the digest cache, and upsert the store lockfile entry. It is
// the single writer of both the cache and the lockfile (spec 19 §"add/update/verify
// take the flock around the cache write").
func doPin(ctx context.Context, ref registry.Reference, policy registry.VerifyPolicy) (registry.Descriptor, error) {
	client, err := newRegistryClient()
	if err != nil {
		return registry.Descriptor{}, err
	}
	pulled, err := client.Pull(ctx, ref)
	if err != nil {
		return registry.Descriptor{}, err
	}
	desc := pulled.Descriptor

	// Signature gate before anything is trusted / unpacked.
	if err := registry.VerifySignature(ctx, nil, desc, policy); err != nil {
		return registry.Descriptor{}, err
	}

	var result registry.Descriptor
	lockErr := lock.WithLock(ctx, lockPath(), func() error {
		cacheDir, err := store.TemplateCacheDir(desc.Digest)
		if err != nil {
			return err
		}
		name, err := materializeBundle(pulled.Tar, cacheDir)
		if err != nil {
			return err
		}
		// Schema-version gate: refuse a bundle newer than this binary understands.
		sv, err := bundleSchemaVersion(cacheDir, name)
		if err != nil {
			return err
		}
		if sv > maxTemplateSchemaVersion {
			return fmt.Errorf("template %q declares schemaVersion %d but this devstack understands up to %d — upgrade devstack to use this template", name, sv, maxTemplateSchemaVersion)
		}

		cfg, ok, err := store.Load()
		if err != nil {
			return err
		}
		if !ok {
			c := store.DefaultConfig()
			cfg = &c
		}
		cfg.UpsertTemplate(store.RemoteTemplate{
			Name:          name,
			Source:        registry.Scheme + ref.Registry + "/" + ref.Repository,
			Version:       ref.Tag,
			Digest:        desc.Digest,
			SchemaVersion: sv,
		})
		if err := cfg.Save(); err != nil {
			return err
		}
		result = registry.Descriptor{
			Ref:           registry.Scheme + ref.Registry + "/" + ref.Repository + "@" + desc.Digest,
			Repository:    ref.Name(),
			Tag:           ref.Tag,
			Digest:        desc.Digest,
			SchemaVersion: sv,
			Name:          name,
			Size:          desc.Size,
		}
		return nil
	})
	if lockErr != nil {
		return registry.Descriptor{}, lockErr
	}
	return result, nil
}

// registerVerifyFlags wires the cosign policy flags onto a command. Enabled is
// derived in a PreRunE: any of the three flags being set turns verification on
// (an explicit signature policy), otherwise the digest pin stands alone.
func registerVerifyFlags(cmd *cobra.Command, p *registry.VerifyPolicy) {
	cmd.Flags().StringVar(&p.IdentityRegexp, "identity", "", "require a keyless cosign signature whose identity matches this regexp")
	cmd.Flags().StringVar(&p.OIDCIssuer, "issuer", "", "require this keyless cosign OIDC issuer")
	cmd.Flags().StringVar(&p.KeyPath, "key", "", "require a keyed cosign signature verifiable with this public key")
	orig := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		p.Enabled = p.IdentityRegexp != "" || p.OIDCIssuer != "" || p.KeyPath != ""
		if orig != nil {
			return orig(c, args)
		}
		return nil
	}
}

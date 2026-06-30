package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/prompt"
	"github.com/open-source-cloud/devstack/internal/store"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/template/scaffold"
)

// errTemplateNewCancelled signals the wizard was dismissed; new prints a notice and
// exits 0.
var errTemplateNewCancelled = errors.New("template new cancelled")

// newTemplateNewCmd wires `devstack template new` — the interactive sibling of
// `template init` (spec 23). It authors a complete template bundle (template.yaml +
// optional build/ tree + golden) through ONE deterministic builder (scaffold.Build)
// fed by two front-ends: a Bubble Tea wizard on a TTY, and a flag/`--from` path that
// CI, `--json`, `--quiet`, and non-TTY always take. It writes only store files; it
// takes no lock, touches no ledger, and starts no Docker.
func newTemplateNewCmd(g *GlobalOpts) *cobra.Command {
	var (
		kind        string
		name        string
		from        string
		extends     string
		description string
		baseImage   string
		params      []string
		port        int
		provides    string
		exports     []string
		entrypoint  bool
		golden      bool
		noGolden    bool
		regold      string
		dir         string
		dryRun      bool
		force       bool
		printSpec   bool
		extraBuild  []string
		noInput     bool
	)
	cmd := &cobra.Command{
		Use:   "new [name]",
		Short: "Author a service template interactively (or from flags/--from)",
		Long: "new scaffolds a complete template bundle — template.yaml + an optional build/\n" +
			"tree (Dockerfile, entrypoint) + an optional golden fixture — through one\n" +
			"deterministic builder. Run it bare in a terminal for the wizard; run it with\n" +
			"flags or --from for a scriptable, byte-stable result. It writes only store\n" +
			"templates; it takes no lock, touches no ledger, and starts no Docker.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && name == "" {
				name = args[0]
			}
			if dir == "" {
				dir = store.TemplatesPath()
			}

			// --regold: re-render an existing template's golden.yaml and stop.
			if regold != "" {
				return regoldTemplate(cmd, g, regold)
			}

			// Resolve the Spec from one of three faces: --from file, the TUI wizard, or
			// flags. Only a bare TTY invocation with no input flags enters bubbletea.
			hasFlags := kind != "" || name != "" || from != "" || extends != "" ||
				baseImage != "" || provides != "" || len(params) > 0 || len(exports) > 0 ||
				len(extraBuild) > 0 || port != 0 || entrypoint
			var spec scaffold.Spec
			var err error
			switch {
			case from != "":
				spec, err = specFromFile(from)
			case prompt.IsInteractive(g.JSON, g.Quiet, noInput) && !hasFlags:
				spec, err = runTemplateNewWizard(builtinSource())
				if errors.Is(err, errTemplateNewCancelled) {
					if !g.Quiet {
						fmt.Fprintln(cmd.ErrOrStderr(), "template new cancelled — nothing written")
					}
					return nil
				}
			default:
				spec, err = specFromFlags(specFlags{
					kind: kind, name: name, extends: extends, description: description,
					baseImage: baseImage, params: params, port: port, provides: provides,
					exports: exports, entrypoint: entrypoint, extraBuild: extraBuild,
					golden: golden && !noGolden,
				})
			}
			if err != nil {
				return err
			}
			// Apply golden default to the --from / wizard paths too unless suppressed.
			if noGolden {
				spec.Golden = false
			}

			if !template.ValidRef(spec.Name) {
				return fmt.Errorf("invalid template name %q: must be a single path segment (dots ok, no slash/.., not absolute)", spec.Name)
			}

			if printSpec {
				return printResolvedSpec(cmd, spec)
			}

			// Refuse to clobber an existing template name (store or built-in) unless --force.
			if !force {
				if builtinSource().Has(spec.Name) {
					return fmt.Errorf("template %q already resolves (store or built-in); pass --force to author over it", spec.Name)
				}
			}

			bundle, err := scaffold.Build(spec)
			if err != nil {
				return err
			}

			// Live preview through the REAL render + validation path. A render or
			// compose-go error blocks the write (you cannot author an ungeneratable
			// bundle). Authoring-lint warnings are surfaced; a meta-templating action is
			// a hard error from scaffold.Lint.
			compose, warnings, err := previewBundle(spec.Name, bundle)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				if !g.Quiet {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
				}
			}
			if spec.Golden {
				bundle["golden.yaml"] = compose
			}

			if dryRun {
				return templateNewDryRun(cmd, g, dir, spec.Name, bundle)
			}

			written, err := scaffold.WriteBundle(dir, spec.Name, bundle, force)
			if err != nil {
				return err
			}

			if g.JSON {
				return writeJSON(cmd, templateNewSummary(spec.Name, filepath.Join(dir, spec.Name), bundle, true))
			}
			if !g.Quiet {
				w := cmd.OutOrStdout()
				target := filepath.Join(dir, spec.Name)
				fmt.Fprintf(w, "wrote template %q (%d files) at %s\n", spec.Name, len(written), target)
				fmt.Fprintf(w, "next: %s template lint %s\n", rootName(cmd), target)
				fmt.Fprintf(w, "      %s template test %s\n", rootName(cmd), target)
				fmt.Fprintf(w, "      reference it from a repo: services: { app: { template: %s } }\n", spec.Name)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&kind, "kind", "", "template kind: app (buildable) | engine (image-based shared service)")
	f.StringVar(&name, "name", "", "template name (single path segment; dots ok, no slash/..)")
	f.StringVar(&from, "from", "", "read a scaffold.Spec (YAML) instead of prompting/flags")
	f.StringVar(&extends, "extends", "", "parent template to extend")
	f.StringVar(&description, "description", "", "human description")
	f.StringVar(&baseImage, "base-image", "", "app: Dockerfile FROM; engine: image:")
	f.StringArrayVar(&params, "param", nil, "param name:type[:default][:required], repeatable")
	f.IntVar(&port, "port", 0, "engine: defaultPort; app: published-port hint (advisory)")
	f.StringVar(&provides, "provides", "", "engine only: capability provided (e.g. postgres)")
	f.StringSliceVar(&exports, "exports", nil, "engine only: importable attrs (csv: host,port,user)")
	f.BoolVar(&entrypoint, "entrypoint", false, "app: also scaffold build/entrypoint.sh")
	f.StringArrayVar(&extraBuild, "extra-build", nil, "app: extra build/<name> file to seed, repeatable")
	f.BoolVar(&golden, "golden", true, "scaffold <name>/golden.yaml")
	f.BoolVar(&noGolden, "no-golden", false, "do not scaffold golden.yaml")
	f.StringVar(&regold, "regold", "", "re-render the golden for an existing template dir, write nothing else")
	f.StringVar(&dir, "dir", "", "parent dir for the new template (default: the store templates dir)")
	f.BoolVar(&dryRun, "dry-run", false, "print the would-be bundle as a tree, write nothing")
	f.BoolVar(&force, "force", false, "overwrite an existing template dir (backs up first)")
	f.BoolVar(&printSpec, "print-spec", false, "emit the resolved scaffold.Spec as YAML and exit")
	f.BoolVar(&noInput, "no-input", false, "never launch the wizard; use flags only (implied by --json/--quiet/non-TTY/CI)")
	return cmd
}

// specFlags carries the raw flag surface into the parser.
type specFlags struct {
	kind, name, extends, description, baseImage, provides string
	params, exports, extraBuild                           []string
	port                                                  int
	entrypoint, golden                                    bool
}

// specFromFlags assembles a scaffold.Spec from the flag surface. --kind and --name
// are required on this path (there is no TTY to fill them).
func specFromFlags(f specFlags) (scaffold.Spec, error) {
	if f.kind == "" {
		return scaffold.Spec{}, fmt.Errorf("no TTY; pass --kind app|engine (or --name / --from), or use `template init`")
	}
	if f.name == "" {
		return scaffold.Spec{}, fmt.Errorf("no TTY; pass --name (or --from), or use `template init`")
	}
	parsed, err := parseParamFlags(f.params)
	if err != nil {
		return scaffold.Spec{}, err
	}
	return scaffold.Spec{
		Kind:        scaffold.Kind(f.kind),
		Name:        f.name,
		Extends:     f.extends,
		Description: f.description,
		BaseImage:   f.baseImage,
		Params:      parsed,
		Provides:    f.provides,
		Exports:     f.exports,
		DefaultPort: f.port,
		Entrypoint:  f.entrypoint,
		ExtraBuild:  f.extraBuild,
		Golden:      f.golden,
	}, nil
}

// parseParamFlags parses repeatable `name:type[:default][:required]` entries.
func parseParamFlags(raw []string) ([]scaffold.Param, error) {
	var out []scaffold.Param
	for _, r := range raw {
		parts := strings.Split(r, ":")
		if parts[0] == "" {
			return nil, fmt.Errorf("--param %q: empty name", r)
		}
		p := scaffold.Param{Name: parts[0]}
		if len(parts) > 1 {
			p.Type = parts[1]
		}
		if len(parts) > 2 {
			p.Default = parts[2]
		}
		if len(parts) > 3 {
			req, err := strconv.ParseBool(parts[3])
			if err != nil {
				return nil, fmt.Errorf("--param %q: required must be true/false, got %q", r, parts[3])
			}
			p.Required = req
		}
		if len(parts) > 4 {
			return nil, fmt.Errorf("--param %q: expected name:type[:default][:required]", r)
		}
		out = append(out, p)
	}
	return out, nil
}

// specFromFile reads a scaffold.Spec from a YAML file (the --from round-trip).
func specFromFile(path string) (scaffold.Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return scaffold.Spec{}, err
	}
	var spec scaffold.Spec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return scaffold.Spec{}, fmt.Errorf("parse spec %s: %w", path, err)
	}
	if spec.Name == "" || spec.Kind == "" {
		return scaffold.Spec{}, fmt.Errorf("spec %s: kind and name are required", path)
	}
	return spec, nil
}

// printResolvedSpec emits the resolved Spec as deterministic YAML (Spec has no maps,
// so struct field order is stable) for the --print-spec ⇒ --from round-trip.
func printResolvedSpec(cmd *cobra.Command, spec scaffold.Spec) error {
	out, err := yaml.Marshal(spec)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(out)
	return err
}

// previewBundle renders the authored bundle through the production path
// (template.Resolve → generate.LintResolved) and runs the authoring lints. A
// render/validation error or a meta-templating hard error blocks the write.
func previewBundle(name string, bundle scaffold.Bundle) ([]byte, []string, error) {
	lr, err := scaffold.Lint(bundle["template.yaml"], scaffold.BuildFilesOf(bundle))
	if err != nil {
		return nil, nil, err
	}
	src := scaffold.PreviewSource(name, bundle, builtinSource())
	res, err := template.Resolve(src, name, nil)
	if err != nil {
		return nil, lr.Warnings, err
	}
	compose, err := generate.LintResolved(name, res)
	if err != nil {
		return nil, lr.Warnings, err
	}
	return compose, lr.Warnings, nil
}

// regoldTemplate re-renders <dir>/golden.yaml from the live template, writing
// nothing else.
func regoldTemplate(cmd *cobra.Command, g *GlobalOpts, dir string) error {
	compose, name, _, err := lintTemplateDir(dir)
	if err != nil {
		return err
	}
	golden := filepath.Join(dir, "golden.yaml")
	if err := scaffold.WriteFile(golden, compose); err != nil {
		return err
	}
	if g.JSON {
		return writeJSON(cmd, map[string]any{"template": name, "golden": golden, "wrote": true})
	}
	if !g.Quiet {
		fmt.Fprintf(cmd.OutOrStdout(), "rewrote %s\n", golden)
	}
	return nil
}

// templateNewDryRun prints the would-be bundle as a sorted file tree, writing
// nothing.
func templateNewDryRun(cmd *cobra.Command, g *GlobalOpts, dir, name string, bundle scaffold.Bundle) error {
	target := filepath.Join(dir, name)
	if g.JSON {
		return writeJSON(cmd, templateNewSummary(name, target, bundle, false))
	}
	if g.Quiet {
		return nil
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%s/\n", target)
	for _, rel := range sortedBundleKeys(bundle) {
		fmt.Fprintf(w, "  %s  (%d bytes)\n", rel, len(bundle[rel]))
	}
	fmt.Fprintln(w, "(dry-run: validates ok, nothing written)")
	return nil
}

type tmplNewSummary struct {
	Template string   `json:"template"`
	Path     string   `json:"path"`
	Files    []string `json:"files"`
	Wrote    bool     `json:"wrote"`
}

func templateNewSummary(name, path string, bundle scaffold.Bundle, wrote bool) tmplNewSummary {
	return tmplNewSummary{Template: name, Path: path, Files: sortedBundleKeys(bundle), Wrote: wrote}
}

func sortedBundleKeys(b scaffold.Bundle) []string {
	out := make([]string, 0, len(b))
	for k := range b {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// optionalInt validates an optional integer text field (empty is allowed).
func optionalInt(s string) error {
	if s == "" {
		return nil
	}
	if _, err := strconv.Atoi(s); err != nil {
		return fmt.Errorf("must be a number")
	}
	return nil
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// splitCSV splits a comma-separated list, trimming spaces and dropping empties.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

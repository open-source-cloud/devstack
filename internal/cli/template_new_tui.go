package cli

import (
	"errors"
	"fmt"
	"sort"

	huh "charm.land/huh/v2"

	"github.com/open-source-cloud/devstack/internal/prompt"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/template/scaffold"
)

// runTemplateNewWizard drives the interactive `template new` flow (huh on Bubble
// Tea v2) and returns the same scaffold.Spec the flag path produces — the spec-23
// "two faces, one builder" seam. Kind/name/extends/base-image/description, then a
// params loop, then a live preview rendered through the REAL Resolve +
// LintResolved path and a confirm. It never runs unless prompt.IsInteractive
// already cleared a real TTY (the caller gates this).
func runTemplateNewWizard(src template.TemplateSource) (scaffold.Spec, error) {
	spec := scaffold.Spec{Golden: true}

	kind := string(scaffold.KindApp)
	parents := extendsOptions(src)

	main := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Kind").
				Description("app = buildable (Dockerfile) · engine = image-based shared service").
				Options(
					huh.NewOption("app (buildable)", string(scaffold.KindApp)),
					huh.NewOption("engine (shared service)", string(scaffold.KindEngine)),
				).Value(&kind),
			huh.NewInput().Title("Name").
				Description("single path segment; dots ok (php.laravel.nginx), no slash/..").
				Value(&spec.Name).
				Validate(func(s string) error {
					if !template.ValidRef(s) {
						return fmt.Errorf("must be a single path segment (dots ok, no slash/.., not absolute)")
					}
					return nil
				}),
			huh.NewSelect[string]().
				Title("Extends (optional)").
				Options(parents...).Value(&spec.Extends),
			huh.NewInput().Title("Base image").
				Description("app: Dockerfile FROM · engine: image:").
				Value(&spec.BaseImage).Validate(huh.ValidateNotEmpty()),
			huh.NewInput().Title("Description (optional)").Value(&spec.Description),
		),
	).WithTheme(prompt.Theme())
	if err := main.Run(); err != nil {
		return scaffold.Spec{}, templateNewErr(err)
	}
	spec.Kind = scaffold.Kind(kind)

	// Engine specifics.
	if spec.Kind == scaffold.KindEngine {
		var exportsCSV, portStr string
		eng := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().Title("Provides (capability)").
					Description("e.g. postgres, redis").Value(&spec.Provides),
				huh.NewInput().Title("Exports (comma-separated)").
					Description("importable attrs: host,port,user,password").Value(&exportsCSV),
				huh.NewInput().Title("Default port").Value(&portStr).
					Validate(optionalInt),
			),
		).WithTheme(prompt.Theme())
		if err := eng.Run(); err != nil {
			return scaffold.Spec{}, templateNewErr(err)
		}
		spec.Exports = splitCSV(exportsCSV)
		spec.DefaultPort = atoiOrZero(portStr)
	} else {
		addEntry := false
		ef := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().Title("Scaffold build/entrypoint.sh?").Value(&addEntry),
			),
		).WithTheme(prompt.Theme())
		if err := ef.Run(); err != nil {
			return scaffold.Spec{}, templateNewErr(err)
		}
		spec.Entrypoint = addEntry
	}

	// Params loop: keep offering "add a parameter" until the author declines.
	for {
		more := false
		ask := huh.NewForm(
			huh.NewGroup(huh.NewConfirm().Title("Add a parameter?").Value(&more)),
		).WithTheme(prompt.Theme())
		if err := ask.Run(); err != nil {
			return scaffold.Spec{}, templateNewErr(err)
		}
		if !more {
			break
		}
		p, err := askParam()
		if err != nil {
			return scaffold.Spec{}, err
		}
		spec.Params = append(spec.Params, p)
	}

	// Build + preview through the production path, then confirm.
	preview := "(preview unavailable)"
	if bundle, err := scaffold.Build(spec); err == nil {
		if compose, _, perr := previewBundle(spec.Name, bundle); perr == nil {
			preview = string(bundle["template.yaml"]) + "\n--- compose ---\n" + string(compose)
		} else {
			preview = string(bundle["template.yaml"]) + "\n--- preview error ---\n" + perr.Error()
		}
	} else {
		preview = "build error: " + err.Error()
	}

	confirm := true
	cf := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Write this template?").
				Description(prompt.PreviewBox(preview)).
				Value(&confirm),
		),
	).WithTheme(prompt.Theme())
	if err := cf.Run(); err != nil {
		return scaffold.Spec{}, templateNewErr(err)
	}
	if !confirm {
		return scaffold.Spec{}, errTemplateNewCancelled
	}
	return spec, nil
}

// askParam runs a single param-entry sub-form.
func askParam() (scaffold.Param, error) {
	p := scaffold.Param{Type: "string"}
	f := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Param name").Value(&p.Name).Validate(huh.ValidateNotEmpty()),
			huh.NewSelect[string]().Title("Type").
				Options(
					huh.NewOption("string", "string"),
					huh.NewOption("int", "int"),
					huh.NewOption("bool", "bool"),
				).Value(&p.Type),
			huh.NewInput().Title("Default (optional)").Value(&p.Default),
			huh.NewConfirm().Title("Required?").Value(&p.Required),
		).Title("Parameter"),
	).WithTheme(prompt.Theme())
	if err := f.Run(); err != nil {
		return scaffold.Param{}, templateNewErr(err)
	}
	return p, nil
}

// extendsOptions builds the extends-parent picker, "(none)" first.
func extendsOptions(src template.TemplateSource) []huh.Option[string] {
	opts := []huh.Option[string]{huh.NewOption("(none)", "")}
	names := append([]string(nil), src.List()...)
	sort.Strings(names)
	for _, n := range names {
		label := n
		if d, err := template.Describe(src, n); err == nil {
			if d.Provides != "" {
				label = fmt.Sprintf("%s — provides %s", n, d.Provides)
			} else if d.Description != "" {
				label = fmt.Sprintf("%s — %s", n, d.Description)
			}
		}
		opts = append(opts, huh.NewOption(label, n))
	}
	return opts
}

// templateNewErr maps a huh user-abort (ctrl+c / esc) to errTemplateNewCancelled.
func templateNewErr(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return errTemplateNewCancelled
	}
	return err
}

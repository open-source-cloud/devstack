package cli

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	huh "charm.land/huh/v2"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/prompt"
	"github.com/open-source-cloud/devstack/internal/scaffold"
	"github.com/open-source-cloud/devstack/internal/template"
)

// errInitCancelled signals the user dismissed the wizard; init prints a notice and
// exits 0 (a correct outcome, not a failure).
var errInitCancelled = errors.New("init cancelled")

var wizardNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// runInitWizard drives the interactive init flow (huh on Bubble Tea v2) and returns
// the same scaffold.Inputs the flag path produces — the spec-22 "two faces, one
// builder" seam. Engines default-select the store seed; per-engine params are
// pre-filled with their template defaults; a final confirm previews the YAML.
func runInitWizard(src template.TemplateSource, seed map[string]config.SharedSvc, defaultName string) (scaffold.Inputs, error) {
	engines, descByName, err := sharedEngines(src)
	if err != nil {
		return scaffold.Inputs{}, err
	}
	if len(engines) == 0 {
		return scaffold.Inputs{}, fmt.Errorf("no shared engines available in the template source")
	}

	name := defaultName
	profile := "dev"
	var chosen []string

	opts := make([]huh.Option[string], 0, len(engines))
	for _, e := range engines {
		d := descByName[e]
		label := e
		if d.DefaultPort > 0 {
			label = fmt.Sprintf("%s — %s :%d", e, d.Provides, d.DefaultPort)
		}
		o := huh.NewOption(label, e)
		if _, seeded := seed[e]; seeded {
			o = o.Selected(true)
		}
		opts = append(opts, o)
	}

	main := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Workspace name").Value(&name).
				Validate(func(s string) error {
					if !wizardNameRE.MatchString(s) {
						return fmt.Errorf("lowercase letters/digits/-/_, starting with a letter")
					}
					return nil
				}),
			huh.NewInput().Title("Default profile").Value(&profile).Validate(huh.ValidateNotEmpty()),
			huh.NewMultiSelect[string]().
				Title("Shared services").
				Description("space to toggle · enter to continue").
				Options(opts...).Value(&chosen),
		),
	).WithTheme(prompt.Theme())
	if err := main.Run(); err != nil {
		return scaffold.Inputs{}, wizardErr(err)
	}

	var services []scaffold.ServiceInput
	for _, eng := range chosen {
		d := descByName[eng]
		si := scaffold.ServiceInput{Engine: eng, Params: map[string]string{}}
		if len(d.Params) > 0 {
			vals := map[string]*string{}
			fields := make([]huh.Field, 0, len(d.Params))
			for _, pk := range sortedParamNames(d.Params) {
				ps := d.Params[pk]
				def := ""
				if ps.Default != nil {
					def = fmt.Sprint(ps.Default)
				}
				v := def
				vals[pk] = &v
				in := huh.NewInput().Title(eng + "." + pk).Value(vals[pk])
				if ps.Description != "" {
					in = in.Description(ps.Description)
				}
				if ps.Required {
					in = in.Validate(huh.ValidateNotEmpty())
				}
				fields = append(fields, in)
			}
			f := huh.NewForm(huh.NewGroup(fields...).Title(eng + " parameters")).WithTheme(prompt.Theme())
			if err := f.Run(); err != nil {
				return scaffold.Inputs{}, wizardErr(err)
			}
			for pk, vp := range vals {
				si.Params[pk] = *vp
			}
		}
		services = append(services, si)
	}

	in := scaffold.Inputs{Name: name, Profile: profile, Services: services}

	// Build + preview, then confirm. A build error (e.g. a bad required param) is
	// surfaced before the confirm rather than after writing.
	ws, err := scaffold.BuildWorkspace(src, in)
	if err != nil {
		return scaffold.Inputs{}, err
	}
	body, err := scaffold.EmitWorkspaceYAML(ws)
	if err != nil {
		return scaffold.Inputs{}, err
	}
	confirm := true
	cf := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Write this workspace.yaml?").
				Description(prompt.PreviewBox(string(body))).
				Value(&confirm),
		),
	).WithTheme(prompt.Theme())
	if err := cf.Run(); err != nil {
		return scaffold.Inputs{}, wizardErr(err)
	}
	if !confirm {
		return scaffold.Inputs{}, errInitCancelled
	}
	return in, nil
}

// sharedEngines lists the source's shared engines (non-empty Provides), sorted,
// with their descriptions — the picker catalogue.
func sharedEngines(src template.TemplateSource) ([]string, map[string]*template.Description, error) {
	descByName := map[string]*template.Description{}
	var engines []string
	for _, n := range src.List() {
		d, err := template.Describe(src, n)
		if err != nil || d.Provides == "" {
			continue
		}
		engines = append(engines, n)
		descByName[n] = d
	}
	sort.Strings(engines)
	return engines, descByName, nil
}

func sortedParamNames(m map[string]template.ParamSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// wizardErr maps a huh user-abort (ctrl+c / esc) to errInitCancelled.
func wizardErr(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return errInitCancelled
	}
	return err
}

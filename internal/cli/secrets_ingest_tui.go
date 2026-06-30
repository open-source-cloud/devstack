package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	huh "charm.land/huh/v2"

	"github.com/open-source-cloud/devstack/internal/envingest"
	"github.com/open-source-cloud/devstack/internal/prompt"
)

// runIngestWizard drives the interactive classification flow (huh on Bubble Tea
// v2, degradable: it only runs behind prompt.IsInteractive). It pre-populates the
// computed classes, lets the operator toggle which keys are secret and which
// config keys are host-sourced, previews the plan, and returns the per-key
// overrides as exact-key glob lists (secret/public/from-host) plus a confirm bool.
// A ctrl+c/esc abort returns ok=false (caller exits 0, writes nothing).
func runIngestWizard(plan envingest.Plan) (secretKeys, publicKeys, hostKeys []string, ok bool, err error) {
	allKeys := make([]string, 0, len(plan.Decisions))
	computedSecret := map[string]bool{}
	computedHost := map[string]bool{}
	for _, d := range plan.Decisions {
		allKeys = append(allKeys, d.Key)
		if d.IsSecret() {
			computedSecret[d.Key] = true
		}
		if d.HostFrom {
			computedHost[d.Key] = true
		}
	}
	sort.Strings(allKeys)

	// Pre-select the computed secrets; the operator toggles off to make a key config.
	selectedSecrets := make([]string, 0)
	for _, k := range allKeys {
		if computedSecret[k] {
			selectedSecrets = append(selectedSecrets, k)
		}
	}

	secretOpts := make([]huh.Option[string], 0, len(allKeys))
	for _, k := range allKeys {
		secretOpts = append(secretOpts, huh.NewOption(k, k).Selected(computedSecret[k]))
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Secrets").
				Description("selected = secret (encrypted); unselect to inline as config · space toggles").
				Options(secretOpts...).
				Value(&selectedSecrets),
		),
	).WithTheme(prompt.Theme())
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, nil, nil, false, nil
		}
		return nil, nil, nil, false, err
	}

	secretSet := map[string]bool{}
	for _, k := range selectedSecrets {
		secretSet[k] = true
	}
	// Config keys are everything not chosen as secret; offer host-sourcing for them.
	var configKeys []string
	for _, k := range allKeys {
		if !secretSet[k] {
			configKeys = append(configKeys, k)
		}
	}

	selectedHost := make([]string, 0)
	if len(configKeys) > 0 {
		hostOpts := make([]huh.Option[string], 0, len(configKeys))
		for _, k := range configKeys {
			hostOpts = append(hostOpts, huh.NewOption(k, k).Selected(computedHost[k]))
		}
		hostForm := huh.NewForm(
			huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title("Host-sourced config keys").
					Description("selected keys are emitted as ${env.KEY} (supplied by the host/CI), not inlined").
					Options(hostOpts...).
					Value(&selectedHost),
			),
		).WithTheme(prompt.Theme())
		if err := hostForm.Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return nil, nil, nil, false, nil
			}
			return nil, nil, nil, false, err
		}
	}

	// Confirm with a preview of the resulting classification.
	confirm := true
	cf := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Apply this classification?").
				Description(prompt.PreviewBox(previewClassification(allKeys, secretSet, selectedHost, plan))).
				Value(&confirm),
		),
	).WithTheme(prompt.Theme())
	if err := cf.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, nil, nil, false, nil
		}
		return nil, nil, nil, false, err
	}
	if !confirm {
		return nil, nil, nil, false, nil
	}

	for _, k := range allKeys {
		if secretSet[k] {
			secretKeys = append(secretKeys, k)
		} else {
			publicKeys = append(publicKeys, k)
		}
	}
	hostKeys = append(hostKeys, selectedHost...)
	return secretKeys, publicKeys, hostKeys, true, nil
}

func previewClassification(allKeys []string, secretSet map[string]bool, hostKeys []string, plan envingest.Plan) string {
	host := map[string]bool{}
	for _, k := range hostKeys {
		host[k] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "destination: %s (%s)\n", plan.Dest, plan.DestPath)
	for _, k := range allKeys {
		class := "config"
		if secretSet[k] {
			class = "secret"
		} else if host[k] {
			class = "config (${env})"
		}
		fmt.Fprintf(&b, "%-26s %s\n", k, class)
	}
	return strings.TrimRight(b.String(), "\n")
}

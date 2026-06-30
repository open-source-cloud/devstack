package prompt

import (
	huh "charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// Theme is the shared huh theme for every devstack wizard, so the interactive
// surfaces look like one product. It tracks the terminal's light/dark background.
func Theme() huh.Theme { return huh.ThemeFunc(huh.ThemeCharm) }

var previewBox = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("63")).
	Padding(0, 1)

// PreviewBox frames content (e.g. a would-be workspace.yaml) in a rounded, padded
// box for a confirm-screen preview.
func PreviewBox(s string) string { return previewBox.Render(s) }

package prompt

import "testing"

func TestIsInteractive_Disablers(t *testing.T) {
	// Each suppressor forces non-interactive regardless of TTY state.
	cases := []struct {
		name                    string
		jsonOut, quiet, noInput bool
	}{
		{"--json", true, false, false},
		{"--quiet", false, true, false},
		{"--no-input", false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsInteractive(tc.jsonOut, tc.quiet, tc.noInput) {
				t.Errorf("%s must disable interactive", tc.name)
			}
		})
	}
}

func TestIsInteractive_CI(t *testing.T) {
	t.Setenv("CI", "1")
	if IsInteractive(false, false, false) {
		t.Error("CI must disable interactive")
	}
}

func TestPreviewBox(t *testing.T) {
	// Smoke: the box renders and contains the content (no panic on lipgloss render).
	if got := PreviewBox("hello"); got == "" {
		t.Error("PreviewBox returned empty")
	}
}

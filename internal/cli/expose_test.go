package cli

import "testing"

// TestExposePortsRegistered asserts expose/ports are available both at the top
// level (the promotion) and under `shared` (backward-compatible aliases).
func TestExposePortsRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, path := range [][]string{
		{"expose"}, {"ports"},
		{"shared", "expose"}, {"shared", "ports"},
	} {
		c, _, err := root.Find(path)
		if err != nil || c.RunE == nil {
			t.Errorf("%v not registered as a real command: %v", path, err)
		}
	}
	// The top-level expose carries its --off flag.
	c, _, _ := root.Find([]string{"expose"})
	if c.Flags().Lookup("off") == nil {
		t.Error("expose missing --off flag")
	}
}

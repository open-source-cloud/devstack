package cli

import "testing"

func TestTunnelRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, sub := range []string{"login", "create", "route"} {
		c, _, err := root.Find([]string{"tunnel", sub})
		if err != nil || c.Name() != sub || c.RunE == nil {
			t.Errorf("tunnel %s not registered as a real command: %v", sub, err)
		}
	}
}

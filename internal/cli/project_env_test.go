package cli

import "testing"

func TestProjectEnvRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, path := range [][]string{
		{"project", "list"}, {"project", "new"},
		{"env", "list"}, {"env", "set"}, {"env", "unset"},
	} {
		c, _, err := root.Find(path)
		if err != nil || c.RunE == nil {
			t.Errorf("%v not registered as a real command: %v", path, err)
		}
	}
}

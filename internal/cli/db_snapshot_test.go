package cli

import "testing"

func TestDbSnapshotCommandsRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, path := range [][]string{
		{"db", "snapshot"}, {"db", "snapshot", "ls"}, {"db", "restore"},
	} {
		c, _, err := root.Find(path)
		if err != nil || c.RunE == nil {
			t.Fatalf("db %v not registered as a real command: %v", path, err)
		}
	}
}

func TestDbSnapshotFlags(t *testing.T) {
	root := NewRootCmd(Options{})
	snap, _, err := root.Find([]string{"db", "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"project", "db", "instance"} {
		if snap.Flags().Lookup(f) == nil {
			t.Errorf("db snapshot missing --%s", f)
		}
	}
	restore, _, err := root.Find([]string{"db", "restore"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"project", "db", "instance", "force", "yes"} {
		if restore.Flags().Lookup(f) == nil {
			t.Errorf("db restore missing --%s", f)
		}
	}
}

func TestShortDigest(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "-"},
		{"abc", "abc"},
		{"0123456789abcdef", "0123456789ab"},
	} {
		if got := shortDigest(tc.in); got != tc.want {
			t.Errorf("shortDigest(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

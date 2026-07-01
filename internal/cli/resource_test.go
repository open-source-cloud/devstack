package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/provision"
)

// fakeConn records the SQL a provisioner runs (Exists=false → create/drop path).
type fakeConn struct{ execs []string }

func (c *fakeConn) Exec(_ context.Context, sql string, _ ...any) error {
	c.execs = append(c.execs, sql)
	return nil
}
func (c *fakeConn) Exists(context.Context, string, ...any) (bool, error) { return false, nil }

func TestResourceCommandRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, sub := range []string{"list", "show", "create", "rm", "gc"} {
		c, _, err := root.Find([]string{"resource", sub})
		if err != nil || c.Name() != sub || c.RunE == nil {
			t.Fatalf("resource %s not registered as a real command: %v", sub, err)
		}
	}
}

func TestWorkspaceDestroyPurgeDataFlag(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"workspace", "destroy"})
	if err != nil {
		t.Fatalf("find destroy: %v", err)
	}
	if c.Flags().Lookup("purge-data") == nil {
		t.Error("workspace destroy must expose --purge-data")
	}
}

func TestDestroyWorkspacePurgeDropsResources(t *testing.T) {
	d, _ := destroyFixture(t)
	ctx := context.Background()

	// A recording connector so the postgres Drop DDL is observable, daemon-free.
	var conns []*fakeConn
	d.PgConnect = func(context.Context, string) (provision.Conn, func() error, error) {
		c := &fakeConn{}
		conns = append(conns, c)
		return c, func() error { return nil }, nil
	}

	// Seed a provisioned database + role + an un-reapable bucket for the project.
	_ = d.DB.RecordProvisioned("app", "database", "app")
	_ = d.DB.RecordProvisioned("app", "role", "app")

	res := destroyWorkspace(ctx, d, []string{"app"}, true)
	if len(res.Errors) != 0 {
		t.Fatalf("purge destroy errors: %v", res.Errors)
	}
	// The database was dropped and un-tracked.
	if len(res.PurgedResources) == 0 {
		t.Fatalf("expected purged resources, got none")
	}
	if len(conns) == 0 {
		t.Fatal("purge must run Drop DDL against postgres")
	}
	joined := strings.Join(conns[0].execs, " | ")
	if !strings.Contains(joined, "DROP DATABASE") {
		t.Errorf("purge must DROP DATABASE: %s", joined)
	}
	rows, _ := d.DB.ProvisionedFor("app")
	if len(rows) != 0 {
		t.Errorf("purge should clear all provisioned rows, got %v", rows)
	}
}

func TestDestroyWorkspaceDefaultPreservesResources(t *testing.T) {
	d, _ := destroyFixture(t)
	ctx := context.Background()
	d.PgConnect = func(context.Context, string) (provision.Conn, func() error, error) {
		t.Fatal("data-preserving destroy must not connect to drop resources")
		return nil, nil, nil
	}
	_ = d.DB.RecordProvisioned("app", "database", "app")

	res := destroyWorkspace(ctx, d, []string{"app"}, false)
	if len(res.PurgedResources) != 0 {
		t.Errorf("default destroy must not purge, got %v", res.PurgedResources)
	}
	// The provisioned database survives (data-preserving contract).
	rows, _ := d.DB.ProvisionedFor("app")
	if len(rows) != 1 {
		t.Errorf("default destroy must preserve provisioned rows, got %v", rows)
	}
}

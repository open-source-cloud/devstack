package orchestrate

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/resource"
)

func TestCreateResourceImperative(t *testing.T) {
	d, fr, db := upFixture(t)
	rp := &recordingPg{}
	d.PgConnect = rp.connect

	attrs, err := CreateResource(context.Background(), d, resource.Resource{
		Engine: "postgres", Kind: "database", Name: "reports", Owner: "app", CredKind: resource.CredPredictable,
	})
	if err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if attrs["database"] != "reports" {
		t.Errorf("attrs[database] = %q, want reports", attrs["database"])
	}
	// Ledger recorded the database + its role.
	rows, _ := db.ProvisionedFor("app")
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.Kind+":"+r.Name)
	}
	if !slices.Contains(kinds, "database:reports") || !slices.Contains(kinds, "role:reports") {
		t.Errorf("provisioned rows = %v, want database:reports + role:reports", kinds)
	}
	// The shared instance's loopback overlay was applied (compose up on shared stack).
	if !fr.saw("-p "+"devstack-shared", "compose.provision.yaml") {
		t.Errorf("overlay not applied via compose up: %v", fr.cmds)
	}
	// The create ran the guarded DDL on loopback.
	joined := strings.Join(rp.conns[0].execs, " | ")
	if !strings.Contains(joined, "CREATE DATABASE") {
		t.Errorf("create DDL missing: %s", joined)
	}
}

func TestCreateResourceUnknownEngine(t *testing.T) {
	d, _, _ := upFixture(t)
	d.PgConnect = okPgConnect
	_, err := CreateResource(context.Background(), d, resource.Resource{
		Engine: "cassandra", Kind: "keyspace", Name: "x", Owner: "app",
	})
	if err == nil {
		t.Fatal("CreateResource should fail for an engine with no shared instance")
	}
}

func TestDropResourceUntrackVsPurge(t *testing.T) {
	// Un-track only: the ledger row goes, no Drop DDL runs.
	d, _, db := upFixture(t)
	rp := &recordingPg{}
	d.PgConnect = rp.connect
	_ = db.RecordProvisioned("app", "database", "reports")
	_ = db.RecordProvisioned("app", "role", "reports")

	if err := DropResource(context.Background(), d,
		resource.Resource{Engine: "postgres", Kind: "database", Name: "reports", Owner: "app"}, false); err != nil {
		t.Fatalf("DropResource untrack: %v", err)
	}
	if rows, _ := db.ProvisionedFor("app"); len(rows) != 0 {
		t.Errorf("un-track should remove database+role rows, got %v", rows)
	}
	if len(rp.conns) != 0 {
		t.Error("un-track must not connect/drop (bytes preserved)")
	}

	// Purge: Drop DDL runs and the rows go.
	d2, _, db2 := upFixture(t)
	rp2 := &recordingPg{}
	d2.PgConnect = rp2.connect
	_ = db2.RecordProvisioned("app", "database", "reports")
	_ = db2.RecordProvisioned("app", "role", "reports")

	if err := DropResource(context.Background(), d2,
		resource.Resource{Engine: "postgres", Kind: "database", Name: "reports", Owner: "app"}, true); err != nil {
		t.Fatalf("DropResource purge: %v", err)
	}
	joined := strings.Join(rp2.conns[0].execs, " | ")
	if !strings.Contains(joined, "DROP DATABASE") {
		t.Errorf("purge must DROP DATABASE: %s", joined)
	}
	if rows, _ := db2.ProvisionedFor("app"); len(rows) != 0 {
		t.Errorf("purge should remove the rows, got %v", rows)
	}
}

func TestGCResourcesReapsOrphans(t *testing.T) {
	d, _, db := upFixture(t)
	rp := &recordingPg{}
	d.PgConnect = rp.connect
	// A postgres database owned by a project no longer in the workspace.
	_ = db.RecordProvisioned("gone", "database", "gone")
	_ = db.RecordProvisioned("gone", "role", "gone")
	// A bucket whose engine (minio) has no live provisioner → skipped, not dropped.
	_ = db.RecordProvisioned("gone", "bucket", "gone-uploads")

	active := map[string]bool{"app": true} // "gone" is not active
	res, err := GCResources(context.Background(), d, active)
	if err != nil {
		t.Fatalf("GCResources: %v", err)
	}
	// Postgres rows reaped; the DDL dropped the database.
	if len(res.Reaped) == 0 {
		t.Fatalf("expected reaped postgres rows, got %+v", res)
	}
	joined := strings.Join(rp.conns[0].execs, " | ")
	if !strings.Contains(joined, "DROP DATABASE") {
		t.Errorf("gc must DROP DATABASE for orphaned postgres db: %s", joined)
	}
	// The bucket (no live provisioner) was skipped and left in the ledger.
	skippedBucket := false
	for _, s := range res.Skipped {
		if s["name"] == "gone-uploads" {
			skippedBucket = true
		}
	}
	if !skippedBucket {
		t.Errorf("bucket with no provisioner should be skipped, got %+v", res.Skipped)
	}
	rows, _ := db.ProvisionedFor("gone")
	stillBucket := false
	for _, r := range rows {
		if r.Kind == "bucket" {
			stillBucket = true
		}
		if r.Kind == "database" || r.Kind == "role" {
			t.Errorf("orphaned postgres row should be reaped, still present: %v", r)
		}
	}
	if !stillBucket {
		t.Error("un-reapable bucket row must NOT be silently dropped")
	}
}

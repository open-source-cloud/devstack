package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/resource"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/workspace"
	"github.com/open-source-cloud/devstack/templates"
)

// --- minimal in-memory messaging admins (no live broker/endpoint) -------------

type fakeNatsAdmin struct{ streams map[string]bool }

func (f *fakeNatsAdmin) EnsureStream(_ context.Context, s resource.StreamSpec) error {
	f.streams[s.Name] = true
	return nil
}
func (f *fakeNatsAdmin) DeleteStream(_ context.Context, name string) error {
	delete(f.streams, name)
	return nil
}
func (f *fakeNatsAdmin) ListStreams(context.Context) ([]string, error) {
	var out []string
	for n := range f.streams {
		out = append(out, n)
	}
	return out, nil
}
func (f *fakeNatsAdmin) EnsureConsumer(context.Context, string, resource.ConsumerSpec) error {
	return nil
}
func (f *fakeNatsAdmin) DeleteConsumer(context.Context, string, string) error { return nil }
func (f *fakeNatsAdmin) Close() error                                         { return nil }

type fakeKafkaAdmin struct{ topics map[string]bool }

func (f *fakeKafkaAdmin) CreateTopic(_ context.Context, name string, _, _ int) error {
	f.topics[name] = true
	return nil
}
func (f *fakeKafkaAdmin) DeleteTopic(_ context.Context, name string) error {
	delete(f.topics, name)
	return nil
}
func (f *fakeKafkaAdmin) ListTopics(context.Context) ([]string, error) {
	var out []string
	for n := range f.topics {
		out = append(out, n)
	}
	return out, nil
}
func (f *fakeKafkaAdmin) Close() error { return nil }

// msgFixture builds a workspace with nats + kafka shared instances and injects the
// fake admin factories so CreateResource runs daemon-free.
func msgFixture(t *testing.T) (UpDeps, *state.DB, *fakeNatsAdmin, *fakeKafkaAdmin) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("workspace.yaml", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\nshared:\n  nats: { template: nats }\n  kafka: { template: kafka }\nprojects:\n  - { name: web, path: web }\n")
	write("web/devstack.yaml", "apiVersion: devstack/v1\nkind: Project\nname: web\nservices:\n  app:\n    template: node.vite\n    uses: [workspace.shared.nats]\n")

	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	db, err := state.Open(context.Background(), filepath.Join(root, "state"), "ctx")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mc := &docker.MockClient{
		Containers: []docker.Container{
			{ID: "n1", Name: "devstack-shared-nats-1", State: "running", Labels: map[string]string{generate.LabelManaged: "true", generate.LabelShared: "nats"}},
			{ID: "k1", Name: "devstack-shared-kafka-1", State: "running", Labels: map[string]string{generate.LabelManaged: "true", generate.LabelShared: "kafka"}},
		},
		Details: map[string]docker.ContainerDetails{
			"n1": {ID: "n1", State: "running", Running: true, Health: docker.HealthHealthy},
			"k1": {ID: "k1", State: "running", Running: true, Health: docker.HealthHealthy},
		},
	}
	src := template.NewFSSource(templates.FS)
	lockPath := filepath.Join(root, "lock")
	mgr := &workspace.Manager{Model: m, DB: db, Docker: mc, Source: src, LockPath: lockPath}
	fn := &fakeNatsAdmin{streams: map[string]bool{}}
	fk := &fakeKafkaAdmin{topics: map[string]bool{}}
	d := UpDeps{
		Model: m, DB: db, Docker: mc, Manager: mgr, Source: src,
		LockPath: lockPath, Runner: &fakeRunner{}, Env: map[string]string{},
		NatsFactory:  func(context.Context, resource.Target) (resource.NatsAdmin, error) { return fn, nil },
		KafkaFactory: func(context.Context, resource.Target) (resource.KafkaAdmin, error) { return fk, nil },
	}
	return d, db, fn, fk
}

func TestCreateStreamNatsRecordsLedgerKind(t *testing.T) {
	d, db, fn, _ := msgFixture(t)
	_, err := CreateResource(context.Background(), d, resource.Resource{
		Engine: "nats", Kind: "stream", Name: "web-orders", Owner: "web",
		Params: map[string]any{"retention": "168h"}, CredKind: resource.CredPredictable,
	})
	if err != nil {
		t.Fatalf("CreateResource stream: %v", err)
	}
	if !fn.streams["web-orders"] {
		t.Errorf("nats stream not created: %v", fn.streams)
	}
	rows, _ := db.ProvisionedFor("web")
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.Kind+":"+r.Name)
	}
	if !slices.Contains(kinds, "stream:web-orders") {
		t.Errorf("ledger kind=stream not recorded: %v", kinds)
	}
}

func TestCreateTopicKafkaRecordsLedgerKind(t *testing.T) {
	d, db, _, fk := msgFixture(t)
	_, err := CreateResource(context.Background(), d, resource.Resource{
		Engine: "kafka", Kind: "topic", Name: "web-events", Owner: "web",
		Params: map[string]any{"partitions": 6}, CredKind: resource.CredPredictable,
	})
	if err != nil {
		t.Fatalf("CreateResource topic: %v", err)
	}
	if !fk.topics["web-events"] {
		t.Errorf("kafka topic not created: %v", fk.topics)
	}
	rows, _ := db.ProvisionedFor("web")
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.Kind+":"+r.Name)
	}
	if !slices.Contains(kinds, "topic:web-events") {
		t.Errorf("ledger kind=topic not recorded: %v", kinds)
	}
}

func TestMessagingDropUntracksLedgerRow(t *testing.T) {
	d, db, fn, _ := msgFixture(t)
	ctx := context.Background()
	_, err := CreateResource(ctx, d, resource.Resource{
		Engine: "nats", Kind: "queue", Name: "web-jobs", Owner: "web", CredKind: resource.CredPredictable,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := DropResource(ctx, d, resource.Resource{Engine: "nats", Kind: "queue", Name: "web-jobs", Owner: "web"}, true); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if fn.streams["web-jobs"] {
		t.Error("nats stream not dropped on purge")
	}
	rows, _ := db.ProvisionedFor("web")
	for _, r := range rows {
		if r.Kind == "queue" && r.Name == "web-jobs" {
			t.Errorf("queue ledger row not reclaimed: %v", rows)
		}
	}
}

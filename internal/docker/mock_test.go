package docker

import (
	"context"
	"testing"
)

func TestMockEnsureNetwork(t *testing.T) {
	m := &MockClient{}
	ctx := context.Background()
	if ok, _ := m.NetworkExists(ctx, "devstack_shared"); ok {
		t.Fatal("network should not exist yet")
	}
	if err := m.EnsureNetwork(ctx, "devstack_shared", map[string]string{"com.devstack.managed": "true"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.NetworkExists(ctx, "devstack_shared"); !ok {
		t.Error("network should exist after EnsureNetwork")
	}
	// Idempotent.
	if err := m.EnsureNetwork(ctx, "devstack_shared", nil); err != nil {
		t.Fatal(err)
	}
}

func TestMockListManagedFilters(t *testing.T) {
	m := &MockClient{Containers: []Container{
		{ID: "1", Name: "devstack-shared-postgres", State: "running",
			Labels: map[string]string{"com.devstack.managed": "true", "com.devstack.shared": "postgres"}},
		{ID: "2", Name: "unrelated", State: "running",
			Labels: map[string]string{"some.other": "thing"}},
		{ID: "3", Name: "oneoff", State: "exited",
			Labels: map[string]string{"com.devstack.managed": "true", "com.docker.compose.oneoff": "True"}},
	}}
	got, err := m.ListManaged(context.Background(), map[string]string{"com.devstack.managed": "true"})
	if err != nil {
		t.Fatal(err)
	}
	// Only container 1: 2 lacks the label, 3 is a one-off.
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("ListManaged = %+v, want only container 1", got)
	}
	if !got[0].Running() {
		t.Error("container 1 should report running")
	}
}

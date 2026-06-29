package workspace

import (
	"context"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/state"
)

type fakeRunner struct{ cmds [][]string }

func (f *fakeRunner) Run(_ context.Context, _ []string, _, name string, args ...string) error {
	f.cmds = append(f.cmds, append([]string{name}, args...))
	return nil
}
func (f *fakeRunner) Output(_ context.Context, _ []string, _, name string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, append([]string{name}, args...))
	return nil, nil
}
func (f *fakeRunner) saw(sub string) bool {
	for _, c := range f.cmds {
		if strings.Contains(strings.Join(c, " "), sub) {
			return true
		}
	}
	return false
}

func TestRebuildStateFromLabels(t *testing.T) {
	// Live containers: shared postgres+redis, project services api and web.
	mock := &docker.MockClient{Containers: []docker.Container{
		{ID: "s1", State: "running", Labels: map[string]string{generate.LabelManaged: "true", generate.LabelShared: "postgres"}},
		{ID: "s2", State: "running", Labels: map[string]string{generate.LabelManaged: "true", generate.LabelShared: "redis"}},
		{ID: "a1", State: "running", Labels: map[string]string{generate.LabelManaged: "true", generate.LabelProject: "api", generate.LabelService: "api"}},
		{ID: "w1", State: "running", Labels: map[string]string{generate.LabelManaged: "true", generate.LabelProject: "web", generate.LabelService: "web"}},
	}}
	m := newManager(t, mock)

	sum, err := m.RebuildState(context.Background())
	if err != nil {
		t.Fatalf("RebuildState: %v", err)
	}
	// minio has no live container → not re-derived; postgres + redis are.
	if len(sum.Shared) != 2 {
		t.Errorf("rebuilt shared = %v, want 2 (postgres, redis)", sum.Shared)
	}
	// Refs: postgres ← api/api, web/web (2); redis ← api/api (1) = 3.
	if sum.Refs != 3 {
		t.Errorf("rebuilt refs = %d, want 3", sum.Refs)
	}
	if n, _ := m.DB.RefCount(generate.SharedAlias("postgres")); n != 2 {
		t.Errorf("shared-postgres refs = %d, want 2", n)
	}
	if n, _ := m.DB.RefCount(generate.SharedAlias("redis")); n != 1 {
		t.Errorf("shared-redis refs = %d, want 1", n)
	}
	rows, _ := m.DB.ListSharedServices()
	if len(rows) != 2 {
		t.Errorf("shared_service rows = %d, want 2 (minio excluded — not live)", len(rows))
	}
}

func TestGCListsAndStopsZeroRef(t *testing.T) {
	fr := &fakeRunner{}
	m := newManager(t, &docker.MockClient{})
	m.Runner = fr
	// A shared service with no refs (zero-ref candidate).
	if err := m.DB.UpsertSharedService(state.SharedService{
		Name: generate.SharedAlias("postgres"), Engine: "postgres", MajorVersion: "16", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	// Dry run: lists the candidate, stops nothing.
	res, err := m.GC(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 1 || res.Candidates[0] != generate.SharedAlias("postgres") {
		t.Fatalf("candidates = %v, want [shared-postgres]", res.Candidates)
	}
	if len(res.Stopped) != 0 {
		t.Errorf("dry run stopped %v, want none", res.Stopped)
	}
	if len(fr.cmds) != 0 {
		t.Errorf("dry run must not run compose; got %v", fr.cmds)
	}

	// With stop: composes stop the service and marks it stopped.
	res, err = m.GC(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stopped) != 1 {
		t.Fatalf("stopped = %v, want [shared-postgres]", res.Stopped)
	}
	if !fr.saw("stop postgres") {
		t.Errorf("did not compose stop the service; cmds=%v", fr.cmds)
	}
	s, ok, _ := m.DB.GetSharedService("postgres", "16")
	if !ok || s.Status != "stopped" {
		t.Errorf("status after gc = %+v, want stopped", s)
	}
}

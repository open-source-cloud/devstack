package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/task"
)

func TestRunRegistered(t *testing.T) {
	if !findCmd(t, "run") {
		t.Fatal("run must be a real RunE command")
	}
}

func TestRunLayersOrder(t *testing.T) {
	tasks := map[string]config.Task{
		"build": {Run: "host", Command: []string{"sh", "-lc", "echo BUILD"}},
		"lint":  {Run: "host", Command: []string{"sh", "-lc", "echo LINT"}},
		"test":  {Run: "host", Command: []string{"sh", "-lc", "echo TEST"}, Deps: []string{"build"}},
		"ci":    {Run: "host", Command: []string{"sh", "-lc", "echo CI"}, Deps: []string{"test", "lint"}},
	}
	layers, err := task.Plan(tasks, []string{"ci"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	r := &taskExec{out: &buf, projDir: t.TempDir()}
	if err := runLayers(context.Background(), r, tasks, layers, 4); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := buf.String()
	// Dependency order: BUILD before TEST, TEST before CI, LINT before CI.
	for _, pair := range [][2]string{{"BUILD", "TEST"}, {"TEST", "CI"}, {"LINT", "CI"}} {
		if strings.Index(out, pair[0]) >= strings.Index(out, pair[1]) {
			t.Errorf("%s should run before %s\n%s", pair[0], pair[1], out)
		}
	}
}

func TestRunLayersPropagatesFailure(t *testing.T) {
	tasks := map[string]config.Task{
		"boom": {Run: "host", Command: []string{"sh", "-lc", "exit 3"}},
	}
	layers, _ := task.Plan(tasks, []string{"boom"})
	r := &taskExec{out: &bytes.Buffer{}, projDir: t.TempDir()}
	if err := runLayers(context.Background(), r, tasks, layers, 1); err == nil {
		t.Fatal("a failing task must fail the run")
	}
}

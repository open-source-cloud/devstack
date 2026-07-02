package task

import (
	"reflect"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
)

func tasks(m map[string][]string) map[string]config.Task {
	out := map[string]config.Task{}
	for name, deps := range m {
		out[name] = config.Task{Command: []string{"true"}, Deps: deps}
	}
	return out
}

func TestPlanLinear(t *testing.T) {
	// test → build; build has no deps.
	layers, err := Plan(tasks(map[string][]string{"build": nil, "test": {"build"}}), []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"build"}, {"test"}}
	if !reflect.DeepEqual(layers, want) {
		t.Fatalf("layers = %v, want %v", layers, want)
	}
}

func TestPlanDiamond(t *testing.T) {
	// deploy depends on test+lint; both depend on build.
	g := tasks(map[string][]string{
		"build": nil, "test": {"build"}, "lint": {"build"}, "deploy": {"test", "lint"},
	})
	layers, err := Plan(g, []string{"deploy"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"build"}, {"lint", "test"}, {"deploy"}}
	if !reflect.DeepEqual(layers, want) {
		t.Fatalf("layers = %v, want %v", layers, want)
	}
}

func TestPlanOnlyClosure(t *testing.T) {
	// Running "lint" must not pull in unrelated "deploy".
	g := tasks(map[string][]string{"build": nil, "lint": {"build"}, "deploy": {"build"}})
	layers, _ := Plan(g, []string{"lint"})
	if got := Flatten(layers); !reflect.DeepEqual(got, []string{"build", "lint"}) {
		t.Fatalf("closure = %v, want [build lint]", got)
	}
}

func TestPlanCycle(t *testing.T) {
	g := tasks(map[string][]string{"a": {"b"}, "b": {"a"}})
	if _, err := Plan(g, []string{"a"}); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestPlanUnknown(t *testing.T) {
	if _, err := Plan(tasks(map[string][]string{"a": {"ghost"}}), []string{"a"}); err == nil {
		t.Fatal("expected unknown-dep error")
	}
	if _, err := Plan(tasks(map[string][]string{"a": nil}), []string{"nope"}); err == nil {
		t.Fatal("expected unknown-target error")
	}
}

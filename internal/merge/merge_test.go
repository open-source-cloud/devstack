package merge

import (
	"reflect"
	"testing"
)

func TestScalarOverwrite(t *testing.T) {
	got := Merge(
		map[string]any{"image": "postgres:16", "restart": "no"},
		map[string]any{"image": "postgres:18"},
	)
	want := map[string]any{"image": "postgres:18", "restart": "no"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRecursiveMapMerge(t *testing.T) {
	base := map[string]any{
		"environment": map[string]any{"A": "1", "B": "2"},
	}
	over := map[string]any{
		"environment": map[string]any{"B": "20", "C": "3"},
	}
	got := Merge(base, over)
	want := map[string]any{
		"environment": map[string]any{"A": "1", "B": "20", "C": "3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestListsReplaceByDefault(t *testing.T) {
	base := map[string]any{"volumes": []any{"a", "b"}}
	over := map[string]any{"volumes": []any{"c"}}
	got := Merge(base, over)
	want := map[string]any{"volumes": []any{"c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lists must replace by default: got %v, want %v", got, want)
	}
}

func TestAppendDirective(t *testing.T) {
	base := map[string]any{"volumes": []any{"a", "b"}}
	over := map[string]any{"volumes": map[string]any{
		MergeKey: "append",
		ValueKey: []any{"c", "d"},
	}}
	got := Merge(base, over)
	want := map[string]any{"volumes": []any{"a", "b", "c", "d"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("append directive: got %v, want %v", got, want)
	}
}

func TestReplaceDirective(t *testing.T) {
	base := map[string]any{"volumes": []any{"a", "b"}}
	over := map[string]any{"volumes": map[string]any{
		MergeKey: "replace",
		ValueKey: []any{"c"},
	}}
	got := Merge(base, over)
	want := map[string]any{"volumes": []any{"c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("replace directive: got %v, want %v", got, want)
	}
}

// TestAppendDirectiveOnAbsentBaseKey is the regression for the directive-leak
// bug: a $merge directive on a key the base never declared must still resolve
// (yielding just the appended values), not leak the raw {$merge,$value} map.
func TestAppendDirectiveOnAbsentBaseKey(t *testing.T) {
	got := Merge(
		map[string]any{},
		map[string]any{"volumes": map[string]any{MergeKey: "append", ValueKey: []any{"a", "b"}}},
	)
	want := map[string]any{"volumes": []any{"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("directive on absent base key leaked/failed: got %v, want %v", got, want)
	}
	// Also nested: directive under a key the base layer never declared.
	got2 := Merge(
		map[string]any{"service": map[string]any{"image": "x"}},
		map[string]any{"service": map[string]any{"volumes": map[string]any{MergeKey: "append", ValueKey: []any{"v"}}}},
	)
	svc := got2["service"].(map[string]any)
	if !reflect.DeepEqual(svc["volumes"], []any{"v"}) {
		t.Errorf("nested directive on absent key: got %v", svc["volumes"])
	}
	if _, leaked := svc["$merge"]; leaked {
		t.Error("$merge key leaked into output")
	}
}

func TestReplaceDirectiveOnAbsentBaseKey(t *testing.T) {
	got := Merge(
		map[string]any{},
		map[string]any{"k": map[string]any{MergeKey: "replace", ValueKey: []any{"c"}}},
	)
	want := map[string]any{"k": []any{"c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNewKeyAdded(t *testing.T) {
	got := Merge(map[string]any{"a": 1}, map[string]any{"b": 2})
	want := map[string]any{"a": 1, "b": 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestNoAliasing proves the merged tree never shares backing storage with its
// inputs — mutating the result must not corrupt the source (the maps.Merge
// shared-reference hazard, DECISIONS D2 / spec 02).
func TestNoAliasing(t *testing.T) {
	srcList := []any{"x"}
	over := map[string]any{"ports": srcList, "env": map[string]any{"K": "V"}}
	base := map[string]any{}
	got := Merge(base, over)

	gotList := got["ports"].([]any)
	gotList[0] = "MUTATED"
	if srcList[0] != "x" {
		t.Errorf("merge aliased the source list: source mutated to %v", srcList[0])
	}
	gotEnv := got["env"].(map[string]any)
	gotEnv["K"] = "MUTATED"
	if over["env"].(map[string]any)["K"] != "V" {
		t.Error("merge aliased the source map: source mutated")
	}
}

func TestTypeMismatchOverWins(t *testing.T) {
	got := Merge(
		map[string]any{"x": map[string]any{"deep": 1}},
		map[string]any{"x": "scalar"},
	)
	want := map[string]any{"x": "scalar"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCloneDeep(t *testing.T) {
	orig := map[string]any{"a": []any{map[string]any{"k": "v"}}}
	cl := Clone(orig).(map[string]any)
	cl["a"].([]any)[0].(map[string]any)["k"] = "changed"
	if orig["a"].([]any)[0].(map[string]any)["k"] != "v" {
		t.Error("Clone is not deep")
	}
}

// Package task plans a project's task graph (spec 31): it resolves a set of
// target tasks plus their transitive dependencies into ordered execution layers,
// where every task in a layer has all its dependencies satisfied by earlier
// layers (so a layer may run in parallel). Missing dependencies and dependency
// cycles are errors. Output is deterministic (layers and intra-layer order are
// sorted), matching devstack's determinism posture.
package task

import (
	"fmt"
	"sort"

	"github.com/open-source-cloud/devstack/internal/config"
)

// Plan returns the execution layers for the given target tasks over the project's
// task map. Each returned slice is one layer of task names (sorted) that can run
// concurrently; layers run in order. Errors on an unknown task/dep or a cycle.
func Plan(tasks map[string]config.Task, targets []string) ([][]string, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("no tasks given")
	}
	// Transitive closure of needed tasks (BFS over deps), with unknown-task check.
	need := map[string]bool{}
	queue := append([]string(nil), targets...)
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if need[n] {
			continue
		}
		t, ok := tasks[n]
		if !ok {
			return nil, fmt.Errorf("unknown task %q", n)
		}
		need[n] = true
		queue = append(queue, t.Deps...)
	}
	// Kahn layering over the closure.
	indeg := map[string]int{}
	for n := range need {
		for _, d := range tasks[n].Deps {
			indeg[n]++ // every dep is in the closure by construction
			_ = d
		}
	}
	var layers [][]string
	processed := map[string]bool{}
	for len(processed) < len(need) {
		var layer []string
		for n := range need {
			if !processed[n] && indeg[n] == 0 {
				layer = append(layer, n)
			}
		}
		if len(layer) == 0 {
			return nil, fmt.Errorf("task dependency cycle among %v", remaining(need, processed))
		}
		sort.Strings(layer)
		for _, n := range layer {
			processed[n] = true
			for m := range need {
				if processed[m] {
					continue
				}
				for _, d := range tasks[m].Deps {
					if d == n {
						indeg[m]--
					}
				}
			}
		}
		layers = append(layers, layer)
	}
	return layers, nil
}

// Flatten returns the tasks in a single deterministic execution order (each layer
// in order, sorted within the layer) — used by --dry-run and sequential runs.
func Flatten(layers [][]string) []string {
	var out []string
	for _, l := range layers {
		out = append(out, l...)
	}
	return out
}

func remaining(need, processed map[string]bool) []string {
	var out []string
	for n := range need {
		if !processed[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

package health

import (
	"fmt"
	"sort"
	"strings"

	"github.com/open-source-cloud/devstack/internal/config"
)

// This file is the full workspace dependency DAG (X2, spec 10): it builds one
// graph from every service's dependsOn (shared services are nodes too), detects
// cycles with the full path, topologically sorts into ordered waves (stable, so
// the up checklist + --json are reproducible), and enforces the generate-time
// rule that a `condition: healthy` edge's target must declare a healthcheck.
// Pure logic — no daemon — so it is fully unit-testable; the saga consumes Waves
// to start shared services first, gate them, then start project waves in order.

// Node identifies a service in the dependency graph. Shared services have
// Shared=true and an empty Project.
type Node struct {
	Project string
	Service string
	Shared  bool
}

// ID is the stable graph key. Human/log form via String.
func (n Node) ID() string {
	if n.Shared {
		return "shared/" + n.Service
	}
	return n.Project + "/" + n.Service
}

// String is the human label used in cycle paths + errors.
func (n Node) String() string {
	if n.Shared {
		return "workspace.shared." + n.Service
	}
	return n.Project + "." + n.Service
}

// Edge is one dependsOn edge: From depends on To, gated by Condition.
type Edge struct {
	To        Node
	Condition string // healthy | started
}

// Graph is the workspace-wide dependsOn DAG.
type Graph struct {
	nodes map[string]Node   // by ID
	edges map[string][]Edge // fromID -> dependency edges
}

// BuildGraph assembles the DAG from every project service's dependsOn. Shared
// services declared in the workspace are nodes. An edge whose target does not
// exist is a hard error (positioned by name).
func BuildGraph(m *config.Model) (*Graph, error) {
	g := &Graph{nodes: map[string]Node{}, edges: map[string][]Edge{}}

	// All project-service nodes.
	for _, project := range sortedKeys(m.Projects) {
		for _, sname := range sortedKeys(m.Projects[project].Services) {
			n := Node{Project: project, Service: sname}
			g.nodes[n.ID()] = n
		}
	}
	// Shared-service nodes.
	for _, name := range sortedKeys(m.Workspace.Shared) {
		n := Node{Service: name, Shared: true}
		g.nodes[n.ID()] = n
	}

	// dependsOn edges.
	for _, project := range sortedKeys(m.Projects) {
		p := m.Projects[project]
		for _, sname := range sortedKeys(p.Services) {
			from := Node{Project: project, Service: sname}
			for _, d := range p.Services[sname].DependsOn {
				to, err := resolveTarget(m, project, d.Service)
				if err != nil {
					return nil, fmt.Errorf("%s dependsOn %q: %w", from, d.Service, err)
				}
				cond := d.Condition
				if cond == "" {
					cond = "healthy"
				}
				g.edges[from.ID()] = append(g.edges[from.ID()], Edge{To: to, Condition: cond})
			}
		}
	}
	return g, nil
}

// resolveTarget maps a dependsOn target string to an existing graph node.
func resolveTarget(m *config.Model, project, target string) (Node, error) {
	ref, ok := config.ParseRef(target)
	if !ok {
		// Bare name → intra-project service.
		if _, exists := m.Projects[project].Services[target]; !exists {
			return Node{}, fmt.Errorf("no service %q in project %q", target, project)
		}
		return Node{Project: project, Service: target}, nil
	}
	switch ref.Kind {
	case config.RefShared:
		if _, exists := m.Workspace.Shared[ref.Name]; !exists {
			return Node{}, fmt.Errorf("shared service %q does not exist", ref.Name)
		}
		return Node{Service: ref.Name, Shared: true}, nil
	case config.RefService:
		tp, exists := m.Projects[ref.Project]
		if !exists {
			return Node{}, fmt.Errorf("project %q does not exist", ref.Project)
		}
		if _, exists := tp.Services[ref.Name]; !exists {
			return Node{}, fmt.Errorf("service %q does not exist in project %q", ref.Name, ref.Project)
		}
		return Node{Project: ref.Project, Service: ref.Name}, nil
	default:
		return Node{}, fmt.Errorf("invalid reference")
	}
}

// Cycle returns the node-label path of a dependency cycle (a → b → a), or nil
// when the graph is acyclic. Deterministic (explores nodes in sorted order).
func (g *Graph) Cycle() []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var found []string

	var dfs func(id string) bool
	dfs = func(id string) bool {
		color[id] = gray
		stack = append(stack, id)
		for _, e := range g.edges[id] {
			w := e.To.ID()
			switch color[w] {
			case gray:
				// Back-edge: slice the cycle from the stack.
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i] == w {
						cyc := append([]string{}, stack[i:]...)
						cyc = append(cyc, w)
						found = labelsOf(g, cyc)
						return true
					}
				}
			case white:
				if dfs(w) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}

	for _, id := range sortedKeys(g.nodes) {
		if color[id] == white {
			if dfs(id) {
				return found
			}
		}
	}
	return nil
}

// Waves returns the topological waves (stable sort on ID within each wave): wave
// 0 has the nodes that depend on nothing, and each later wave depends only on
// earlier ones. Returns an error if the graph has a cycle (with the path).
func (g *Graph) Waves() ([][]Node, error) {
	if cyc := g.Cycle(); cyc != nil {
		return nil, fmt.Errorf("dependsOn cycle: %s", strings.Join(cyc, " → "))
	}
	remaining := map[string]int{}       // unplaced dependency count per node
	dependents := map[string][]string{} // toID -> [fromID...]
	for fromID := range g.edges {
		seen := map[string]bool{}
		for _, e := range g.edges[fromID] {
			toID := e.To.ID()
			if seen[toID] {
				continue
			}
			seen[toID] = true
			remaining[fromID]++
			dependents[toID] = append(dependents[toID], fromID)
		}
	}

	placed := map[string]bool{}
	var waves [][]Node
	for len(placed) < len(g.nodes) {
		var waveIDs []string
		for _, id := range sortedKeys(g.nodes) {
			if !placed[id] && remaining[id] == 0 {
				waveIDs = append(waveIDs, id)
			}
		}
		if len(waveIDs) == 0 {
			return nil, fmt.Errorf("dependsOn cycle (no schedulable wave)") // defensive; Cycle() should have caught it
		}
		wave := make([]Node, 0, len(waveIDs))
		for _, id := range waveIDs {
			placed[id] = true
			wave = append(wave, g.nodes[id])
			for _, dep := range dependents[id] {
				remaining[dep]--
			}
		}
		waves = append(waves, wave)
	}
	return waves, nil
}

// RequireHealthchecks enforces spec-10's rule: every `condition: healthy` edge's
// target must declare a healthcheck. hasHealthcheck reports whether a node's
// service declares one (project services from config; shared from their template
// — the caller supplies the predicate). Returns the first violation.
func (g *Graph) RequireHealthchecks(hasHealthcheck func(Node) bool) error {
	for _, fromID := range sortedKeys(g.edges) {
		from := g.nodes[fromID]
		for _, e := range g.edges[fromID] {
			if e.Condition == "healthy" && !hasHealthcheck(e.To) {
				return fmt.Errorf("%s depends on %s with condition: healthy, but %s declares no healthcheck",
					from, e.To, e.To)
			}
		}
	}
	return nil
}

// Nodes returns all graph nodes (sorted by ID) — for callers that need the set.
func (g *Graph) Nodes() []Node {
	out := make([]Node, 0, len(g.nodes))
	for _, id := range sortedKeys(g.nodes) {
		out = append(out, g.nodes[id])
	}
	return out
}

func labelsOf(g *Graph, ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = g.nodes[id].String()
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

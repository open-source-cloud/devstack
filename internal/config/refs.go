package config

import "strings"

// refKind classifies a parsed reference path.
type refKind int

const (
	refInvalid refKind = iota
	refShared          // workspace.shared.<name>[.<attr>]
	refService         // workspace.<project>.<service>[.<attr>]
)

// ref is a parsed cross-service reference (the target of `uses`, `import.from`,
// and ${ref:...}). All references are rooted at "workspace.".
type ref struct {
	kind    refKind
	project string // refService only
	name    string // shared name (refShared) or service name (refService)
	attr    string // optional trailing attribute (e.g. host); "" if absent
}

// parseRef parses a dotted reference path. Accepted forms:
//
//	workspace.shared.<name>            workspace.shared.<name>.<attr>
//	workspace.<project>.<service>      workspace.<project>.<service>.<attr>
//
// The second form's <project> may not be the literal "shared". Returns
// kind==refInvalid for anything else.
func parseRef(s string) ref {
	parts := strings.Split(s, ".")
	if len(parts) < 3 || parts[0] != "workspace" {
		return ref{}
	}
	if parts[1] == "shared" {
		// workspace.shared.<name>[.<attr>]
		switch len(parts) {
		case 3:
			return ref{kind: refShared, name: parts[2]}
		case 4:
			return ref{kind: refShared, name: parts[2], attr: parts[3]}
		default:
			return ref{}
		}
	}
	// workspace.<project>.<service>[.<attr>]
	switch len(parts) {
	case 3:
		return ref{kind: refService, project: parts[1], name: parts[2]}
	case 4:
		return ref{kind: refService, project: parts[1], name: parts[2], attr: parts[3]}
	default:
		return ref{}
	}
}

// nodeID is the cycle-graph identity of a project service ("<project>/<service>").
func nodeID(project, service string) string { return project + "/" + service }

// Reference kinds for the public ParseRef API.
const (
	RefShared  = "shared"  // workspace.shared.<name>[.<attr>]
	RefService = "service" // workspace.<project>.<service>[.<attr>]
)

// Reference is the public, parsed form of a cross-service reference, used by
// internal/generate to resolve ${ref:...} and env.import targets against the
// workspace graph. Kind is "" when the path is not a valid reference.
type Reference struct {
	Kind    string // RefShared | RefService | ""
	Project string // RefService only
	Name    string // shared name (RefShared) or service name (RefService)
	Attr    string // trailing attribute (e.g. "host"); "" if absent
}

// ParseRef parses a dotted reference path using the single canonical grammar
// (the same one validateRefs uses). The bool is false for an unparseable path.
func ParseRef(s string) (Reference, bool) {
	r := parseRef(s)
	switch r.kind {
	case refShared:
		return Reference{Kind: RefShared, Name: r.name, Attr: r.attr}, true
	case refService:
		return Reference{Kind: RefService, Project: r.project, Name: r.name, Attr: r.attr}, true
	default:
		return Reference{}, false
	}
}

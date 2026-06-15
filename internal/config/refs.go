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

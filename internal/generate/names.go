// Package generate is the single owner of the config→compose generation pipeline
// (ARCHITECTURE §3, spec 02). It renders each project's services (and the shared
// stack) from templates, resolves ${ref}/${env}/${self} and env.import against the
// WORKSPACE service graph, assembles a typed compose model, validates it through
// compose-go/v2, and writes byte-deterministic artifacts under each stack's
// .devstack/ directory with a SHA-256 rebuild-hash ledger.
//
// Determinism is a hard requirement (spec 02 acceptance #3): identical inputs
// produce byte-identical output. Every map is marshalled through compose-go's
// stable-key MarshalYAML, env keys are sorted, and no clock/random input enters.
package generate

import "strings"

// SharedNetwork is the pinned name of the tool-owned external bridge network that
// both the shared stack and every project stack join (ARCHITECTURE §4). Compose
// refuses to create/remove external networks, so devstack owns its lifecycle.
const SharedNetwork = "devstack_shared"

// SharedStackName is the compose project name for the shared-services stack.
const SharedStackName = "devstack-shared"

// Tool-owned label namespace. Container enumeration filters on com.devstack.managed
// (not just com.docker.compose.project), per DECISIONS D5.
const (
	LabelManaged   = "com.devstack.managed"
	LabelWorkspace = "com.devstack.workspace"
	LabelProject   = "com.devstack.project"
	LabelService   = "com.devstack.service"
	LabelShared    = "com.devstack.shared"
)

// GenDir is the per-stack output directory (gitignored — OPEN-QUESTIONS Q-GEN:
// regenerate by default).
const GenDir = ".devstack"

// ComposeFile is the generated compose document's filename.
const ComposeFile = "docker-compose.yaml"

// StateFile records the per-build-context SHA-256 rebuild hashes.
const StateFile = "state.json"

// projectStackName is the compose project name for a project stack.
func projectStackName(project string) string { return "devstack-" + project }

// ProjectStackName is the exported compose project name for a project stack. IDE
// artifacts (internal/ide, spec 17) reference it so the editor's `compose up`
// lands in the SAME tool-owned project as `devstack up` — never a forked one.
func ProjectStackName(project string) string { return projectStackName(project) }

// sharedAlias is the stable DNS alias a shared service is reached by over the
// shared network (never the bare service name — the collision guardrail).
func sharedAlias(name string) string { return "shared-" + name }

// SharedAlias is the exported form of sharedAlias, used by internal/workspace to
// key the ledger by the same instance name the generated compose reaches.
func SharedAlias(name string) string { return sharedAlias(name) }

// envPrefix upper-cases and underscore-sanitizes a name for use as an env-var
// prefix (e.g. the "postgres" import → POSTGRES_HOST).
func envPrefix(name string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
}

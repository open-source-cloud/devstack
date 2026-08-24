package ai

import (
	"embed"
	"fmt"
)

// pack holds the authored agent-facing content: the bodies of the emitted skills
// and the AGENTS.md block.
//
// It lives under internal/ so editing it touches internal/**, which CI always
// builds and tests — the same reason docs/ had to be removed from the workflow's
// paths-ignore list once it became a build input.
//
// The content is emitted VERBATIM. It is deliberately never run through
// internal/template: the template-authoring page must contain literal
// `[[ .params.version ]]` examples, and text/template with `[[ ]]` delimiters and
// missingkey=error would try to execute them.
//
//go:embed pack/*.md
var pack embed.FS

// Pack names one authored document.
type Pack string

// The authored documents. devstack/templates/troubleshooting become skills;
// reference is a supporting file loaded on demand; agents is the AGENTS.md block.
const (
	PackDevstack        Pack = "devstack"
	PackTemplates       Pack = "templates"
	PackTroubleshooting Pack = "troubleshooting"
	PackReference       Pack = "reference"
	PackAgents          Pack = "agents"
)

// packBody returns one authored document's markdown.
func packBody(p Pack) ([]byte, error) {
	b, err := pack.ReadFile("pack/" + string(p) + ".md")
	if err != nil {
		return nil, fmt.Errorf("read embedded pack %s: %w", p, err)
	}
	return b, nil
}

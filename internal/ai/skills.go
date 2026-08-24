package ai

import (
	"bytes"
	"fmt"
	"strings"
)

// skillSpec describes one emitted Claude Code skill.
//
// Frontmatter is restricted to the six fields the Agent Skills spec allows —
// name, description, license, compatibility, metadata, allowed-tools. Claude Code
// accepts many more (when_to_use, argument-hint, disable-model-invocation,
// context: fork, model, effort, paths…), but any of them is a HARD ERROR when the
// same file is uploaded to claude.ai or packaged with the Agent Skills tooling.
// One artifact set that works everywhere beats two sets behind a flag, so the
// "when to use" guidance is folded into description — which is what the skill
// router actually reads anyway.
type skillSpec struct {
	Dir         string // .claude/skills/<Dir>/SKILL.md
	Name        string
	Description string
	Body        Pack
	Support     []supportFile
}

// supportFile is an extra document in a skill directory, loaded only when the
// skill decides it needs it. Keeping bulk out of SKILL.md is what makes the
// progressive disclosure work.
type supportFile struct {
	Name string
	Body Pack
}

// skills is the emitted set. Three, not one and not ten: skills are routed by
// their description, and "operate devstack", "author a devstack template" and
// "something is broken" have disjoint triggers. Ten near-identical per-group
// skills would blur that routing; reference.md plus `--help` covers the detail.
var skills = []skillSpec{
	{
		Dir:  "devstack",
		Name: "devstack",
		Description: "Use when working in a repository that contains workspace.yaml or devstack.yaml, " +
			"or when asked to start, stop or inspect a local development environment, shared " +
			"Postgres/Redis/MinIO/Kafka/NATS, tenant databases, buckets, queues or topics. devstack " +
			"runs many project stacks against one warm shared infrastructure stack on a tool-owned " +
			"Docker network. Covers the two-file config model, the up/down/status lifecycle, and the " +
			"rules for driving the CLI safely — including that compose files under .devstack/ are " +
			"generated and must never be hand-edited.",
		Body:    PackDevstack,
		Support: []supportFile{{Name: "reference.md", Body: PackReference}},
	},
	{
		Dir:  "devstack-templates",
		Name: "devstack-templates",
		Description: "Use when authoring or editing a devstack service template — a directory holding " +
			"template.yaml, an optional build/ tree and golden.yaml — or when asked to add support for " +
			"a new database, engine, language or framework to devstack. Covers the engine-versus-app " +
			"split, the [[ ]] delimiters and the deterministic FuncMap, params/extends/provides/exports, " +
			"the rule that metadata keys are never templated, list-merge semantics, and the " +
			"template new → lint → test loop.",
		Body: PackTemplates,
	},
	{
		Dir:  "devstack-troubleshooting",
		Name: "devstack-troubleshooting",
		Description: "Use when a devstack command fails or a service is unhealthy — the Docker daemon is " +
			"unreachable, the devstack_shared network is missing, a host port is in use, generate --check " +
			"reports drift, config errors point at file:line:col, secret:// resolution fails, shared-service " +
			"ref counts look wrong, or file watching misbehaves on WSL2. Maps each symptom to the " +
			"diagnostic command and the fix.",
		Body: PackTroubleshooting,
	},
}

// buildSkills renders every skill file.
func (g *Generator) buildSkills() ([]Artifact, error) {
	var arts []Artifact
	for _, s := range skills {
		body, err := packBody(s.Body)
		if err != nil {
			return nil, err
		}
		doc, err := g.renderSkill(s, body)
		if err != nil {
			return nil, err
		}
		arts = append(arts, g.artifact("skill", MergeWhole, doc,
			".claude", "skills", s.Dir, "SKILL.md"))

		for _, sup := range s.Support {
			supBody, err := packBody(sup.Body)
			if err != nil {
				return nil, err
			}
			arts = append(arts, g.artifact("skill-support", MergeWhole, g.rewriteBinary(supBody),
				".claude", "skills", s.Dir, sup.Name))
		}
	}
	return arts, nil
}

// renderSkill assembles frontmatter plus body.
func (g *Generator) renderSkill(s skillSpec, body []byte) ([]byte, error) {
	if strings.Contains(s.Description, "\n") {
		return nil, fmt.Errorf("skill %s: description must be a single line", s.Name)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	fmt.Fprintf(&buf, "name: %s\n", s.Name)
	// A folded scalar keeps the long description readable in the file while
	// remaining one logical line for the skill router.
	buf.WriteString("description: >-\n")
	for _, line := range wrapText(s.Description, 76) {
		fmt.Fprintf(&buf, "  %s\n", line)
	}
	buf.WriteString("license: Apache-2.0\n")
	fmt.Fprintf(&buf, "allowed-tools: Bash(%s:*)\n", g.binary)
	// Deliberately NO version stamp. Claude Code does not act on `metadata`, and
	// embedding the binary version would make every user's committed files stale
	// on every release — turning `ai check` into a CI failure and `self update`
	// into a diff in everyone's repository. The emitted content depends only on
	// the pack and the binary name; TestOutputDoesNotDependOnVersion pins that.
	buf.WriteString("---\n\n")
	buf.Write(g.rewriteBinary(body))
	return buf.Bytes(), nil
}

// wrapText greedily wraps a single-line string to a width, for frontmatter
// readability. Deterministic: the same input always yields the same lines.
func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var (
		lines []string
		cur   strings.Builder
	)
	for _, w := range words {
		if cur.Len() > 0 && cur.Len()+1+len(w) > width {
			lines = append(lines, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
		}
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// SkillDirs returns the skill directory names, for callers that need to describe
// or clean up what was emitted.
func SkillDirs() []string {
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Dir)
	}
	return out
}

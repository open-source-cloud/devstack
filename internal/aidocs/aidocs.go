// Package aidocs indexes the embedded documentation corpus (docs.FS) for machine
// consumption: a stable slug per document, a title and summary lifted from the
// markdown, full-text search, and the raw body (spec 32).
//
// It is the single retrieval layer behind all three agent surfaces — the
// `devstack ai docs` CLI for agents that only have a shell, the devstack://docs/…
// MCP resources, and the slug index the emitted skills point at. Keeping one
// layer is what lets the emitted skills stay tiny: they carry navigation, not
// copies of the documentation, so an upgraded binary serves upgraded docs with no
// churn in the user's repo.
//
// Pure and read-only: no Docker, no ledger, no flock, no filesystem access beyond
// the embedded FS. Every listing is deterministically ordered.
package aidocs

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/open-source-cloud/devstack/docs"
)

// Section groups the corpus by the role a document plays.
type Section string

// The corpus sections, in the order List reports them: the task-oriented book
// first (what a newcomer or an agent should read), then the top-level design
// documents, then the per-component specs.
const (
	SectionGuide Section = "guide" // docs/guide/** — the task-oriented book
	SectionRoot  Section = "root"  // docs/*.md — ARCHITECTURE, DECISIONS, ROADMAP, …
	SectionSpec  Section = "specs" // docs/specs/** — the per-component specs
)

var sectionRank = map[Section]int{SectionGuide: 0, SectionRoot: 1, SectionSpec: 2}

// Doc is one indexed document. Body is not included: callers that want the text
// call Read, so listing the whole corpus stays cheap.
type Doc struct {
	Slug    string  `json:"slug"`    // stable retrieval key, e.g. guide/templates
	Path    string  `json:"path"`    // repo-relative path, e.g. docs/guide/templates.md
	Title   string  `json:"title"`   // first H1, or a humanized file name
	Summary string  `json:"summary"` // first prose paragraph, single-line
	Section Section `json:"section"`
	Lines   int     `json:"lines"`
}

// index is the lazily-built corpus index. The embedded FS is immutable, so it is
// built exactly once per process.
var index struct {
	once   sync.Once
	docs   []Doc
	bySlug map[string]Doc
	err    error
}

func build() {
	index.bySlug = make(map[string]Doc)
	err := fs.WalkDir(docs.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		body, err := docs.FS.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		doc := describe(p, body)
		index.docs = append(index.docs, doc)
		index.bySlug[doc.Slug] = doc
		for _, alias := range aliases(doc) {
			// A real slug always wins over an alias, so a future docs/specs/23.md
			// could never be shadowed by the 23-… alias.
			if _, taken := index.bySlug[alias]; !taken {
				index.bySlug[alias] = doc
			}
		}
		return nil
	})
	if err != nil {
		index.err = err
		return
	}
	sort.Slice(index.docs, func(i, j int) bool {
		a, b := index.docs[i], index.docs[j]
		if ra, rb := sectionRank[a.Section], sectionRank[b.Section]; ra != rb {
			return ra < rb
		}
		return a.Slug < b.Slug
	})
}

func load() error {
	index.once.Do(build)
	return index.err
}

// describe derives a Doc from one corpus file.
func describe(p string, body []byte) Doc {
	slug := strings.TrimSuffix(p, ".md")
	section := SectionRoot
	switch {
	case strings.HasPrefix(p, "guide/"):
		section = SectionGuide
	case strings.HasPrefix(p, "specs/"):
		section = SectionSpec
	default:
		// Top-level docs are ALL-CAPS on disk (ARCHITECTURE.md); lowercase the
		// slug so retrieval is not shift-key-sensitive.
		slug = strings.ToLower(slug)
	}
	title, summary := titleAndSummary(body)
	if title == "" {
		title = humanize(path.Base(slug))
	}
	return Doc{
		Slug:    slug,
		Path:    "docs/" + p,
		Title:   title,
		Summary: summary,
		Section: section,
		Lines:   countLines(body),
	}
}

// aliases returns extra retrieval keys for a document. Specs get their number
// ("specs/23", "23") because that is how every cross-reference in the corpus and
// in CLAUDE.md names them.
func aliases(d Doc) []string {
	if d.Section != SectionSpec {
		return nil
	}
	num, _, ok := strings.Cut(path.Base(d.Slug), "-")
	if !ok || num == "" {
		return nil
	}
	return []string{"specs/" + num, num}
}

// titleAndSummary lifts the first H1 and the first prose paragraph after it,
// skipping the badge/blockquote/link furniture the docs open with.
func titleAndSummary(body []byte) (title, summary string) {
	var para []string
	inFence := false
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if title == "" {
			if strings.HasPrefix(line, "# ") {
				title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			}
			continue
		}
		if isFurniture(line) {
			if len(para) > 0 {
				break
			}
			continue
		}
		if line == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, line)
	}
	return title, cleanSummary(strings.Join(para, " "))
}

// isFurniture reports whether a line is navigation, a badge row, an HTML wrapper,
// a heading, a blockquote callout or a horizontal rule — never prose.
func isFurniture(line string) bool {
	switch {
	case line == "---", line == "***", line == "___":
		return true
	case strings.HasPrefix(line, "#"), strings.HasPrefix(line, ">"):
		return true
	case strings.HasPrefix(line, "<"):
		return true
	case strings.HasPrefix(line, "|"), strings.HasPrefix(line, "["), strings.HasPrefix(line, "!["):
		return true
	case strings.HasPrefix(line, "*   "), strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "):
		return true
	}
	return false
}

// cleanSummary strips inline markdown emphasis and link syntax so the summary
// reads as plain text in a table or a JSON field, and caps it to one line.
func cleanSummary(s string) string {
	s = strings.NewReplacer("**", "", "`", "", "__", "").Replace(s)
	// [text](url) → text
	for {
		open := strings.Index(s, "](")
		if open < 0 {
			break
		}
		start := strings.LastIndex(s[:open], "[")
		end := strings.Index(s[open:], ")")
		if start < 0 || end < 0 {
			break
		}
		s = s[:start] + s[start+1:open] + s[open+end+1:]
	}
	s = strings.Join(strings.Fields(s), " ")
	const max = 240
	if len(s) > max {
		if cut := strings.LastIndex(s[:max], " "); cut > 0 {
			return s[:cut] + "…"
		}
		return s[:max] + "…"
	}
	return s
}

func humanize(base string) string {
	return strings.ToUpper(base[:1]) + strings.ReplaceAll(base[1:], "-", " ")
}

func countLines(body []byte) int {
	n := strings.Count(string(body), "\n")
	if len(body) > 0 && !strings.HasSuffix(string(body), "\n") {
		n++
	}
	return n
}

// List returns every indexed document in stable order: the guide first, then the
// top-level design docs, then the specs.
func List() ([]Doc, error) {
	if err := load(); err != nil {
		return nil, err
	}
	out := make([]Doc, len(index.docs))
	copy(out, index.docs)
	return out, nil
}

// Lookup resolves a slug (or a spec-number alias) to its Doc.
func Lookup(slug string) (Doc, error) {
	if err := load(); err != nil {
		return Doc{}, err
	}
	if d, ok := index.bySlug[normalizeSlug(slug)]; ok {
		return d, nil
	}
	return Doc{}, notFound(slug)
}

// Read returns a document's raw markdown.
func Read(slug string) (Doc, []byte, error) {
	d, err := Lookup(slug)
	if err != nil {
		return Doc{}, nil, err
	}
	body, err := docs.FS.ReadFile(strings.TrimPrefix(d.Path, "docs/"))
	if err != nil {
		return Doc{}, nil, fmt.Errorf("read %s: %w", d.Path, err)
	}
	return d, body, nil
}

// normalizeSlug accepts the forms a human or a model is likely to type: a bare
// slug, the repo-relative path, a trailing .md, or a leading slash.
func normalizeSlug(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "./")
	s = strings.TrimPrefix(s, "/")
	s = strings.TrimPrefix(s, "docs/")
	s = strings.TrimSuffix(s, ".md")
	if !strings.Contains(s, "/") {
		// Top-level docs are indexed lowercase; specs and guide pages already are.
		return strings.ToLower(s)
	}
	return s
}

// notFound builds an error that names the closest slugs, so a wrong guess costs
// one round trip instead of a listing call.
func notFound(slug string) error {
	near := suggest(slug, 5)
	if len(near) == 0 {
		return fmt.Errorf("no such doc %q (run `devstack ai docs` to list them)", slug)
	}
	return fmt.Errorf("no such doc %q (did you mean: %s?)", slug, strings.Join(near, ", "))
}

// suggest returns slugs sharing a substring with the query, closest first.
func suggest(slug string, limit int) []string {
	q := strings.ToLower(normalizeSlug(slug))
	if q == "" {
		return nil
	}
	var out []string
	for _, d := range index.docs {
		if strings.Contains(strings.ToLower(d.Slug), q) || strings.Contains(q, path.Base(d.Slug)) {
			out = append(out, d.Slug)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

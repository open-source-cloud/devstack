package aidocs

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mdLinkRE matches inline markdown links: [text](target). Reference-style links
// and bare URLs are deliberately out of scope.
var mdLinkRE = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// repoRoot locates the repository root from the package directory, or returns ""
// when the test is not running inside the source tree.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return ""
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return ""
	}
	return root
}

// TestCorpusLinksResolve walks every embedded document and checks that each
// relative link points at something that exists.
//
// This is the test that pays for compiling docs/ into the binary. Documentation
// used to be a build-time no-op, so a broken cross-link was cosmetic; now the
// corpus ships inside the binary and is what `devstack ai docs` and the MCP
// resources serve, so a dangling link is a product defect an agent will follow.
// It is also why .github/workflows/ci.yml must not skip checks for doc-only
// changes.
func TestCorpusLinksResolve(t *testing.T) {
	root := repoRoot(t)
	list, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var checked int
	for _, d := range list {
		_, body, err := Read(d.Slug)
		if err != nil {
			t.Fatalf("Read(%s): %v", d.Slug, err)
		}
		// Directory of this doc, repo-relative: docs/guide for docs/guide/x.md.
		docDir := path.Dir(d.Path)
		for _, m := range mdLinkRE.FindAllStringSubmatch(string(body), -1) {
			target := m[1]
			if skipLink(target) {
				continue
			}
			// Drop any #anchor; we verify the file, not the heading.
			file, _, _ := strings.Cut(target, "#")
			if file == "" {
				continue // pure in-page anchor
			}
			resolved := path.Clean(path.Join(docDir, file))
			checked++
			if err := existsInRepo(root, resolved); err != nil {
				t.Errorf("%s links to %q which %v", d.Path, target, err)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only checked %d links; the corpus is heavily cross-linked, so the matcher is probably broken", checked)
	}
	t.Logf("checked %d relative links across %d documents", checked, len(list))
}

// skipLink filters out targets this test cannot or should not resolve.
func skipLink(target string) bool {
	switch {
	case target == "":
		return true
	case strings.HasPrefix(target, "#"): // in-page anchor
		return true
	case strings.HasPrefix(target, "mailto:"):
		return true
	case strings.Contains(target, "://"): // absolute URL
		return true
	case strings.HasPrefix(target, "<"): // autolink / template placeholder
		return true
	}
	return false
}

// existsInRepo checks a repo-relative path: inside docs/ it must be in the
// EMBEDDED corpus (a link the binary itself will serve), anywhere else it only
// has to exist on disk.
func existsInRepo(root, rel string) error {
	if strings.HasPrefix(rel, "docs/") {
		inCorpus := strings.TrimPrefix(rel, "docs/")
		if strings.HasSuffix(inCorpus, ".md") {
			if _, err := Lookup(strings.TrimSuffix(inCorpus, ".md")); err != nil {
				return errNotEmbedded
			}
			return nil
		}
	}
	if root == "" {
		return nil // not running in the source tree; nothing to check against
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		return errMissing
	}
	return nil
}

type linkErr string

func (e linkErr) Error() string { return string(e) }

const (
	errNotEmbedded linkErr = "is under docs/ but is not in the embedded corpus"
	errMissing     linkErr = "does not exist in the repository"
)

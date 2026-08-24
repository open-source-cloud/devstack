package aidocs

import (
	"strings"
	"testing"
)

func TestListCoversTheWholeCorpus(t *testing.T) {
	got, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) < 60 {
		t.Fatalf("expected the full corpus (66 files at time of writing), got %d", len(got))
	}
	var guide, root, spec int
	for _, d := range got {
		switch d.Section {
		case SectionGuide:
			guide++
		case SectionRoot:
			root++
		case SectionSpec:
			spec++
		default:
			t.Errorf("%s has an unknown section %q", d.Slug, d.Section)
		}
		if d.Title == "" {
			t.Errorf("%s has no title", d.Slug)
		}
		if d.Lines == 0 {
			t.Errorf("%s reports 0 lines", d.Slug)
		}
		if !strings.HasPrefix(d.Path, "docs/") || !strings.HasSuffix(d.Path, ".md") {
			t.Errorf("%s has a malformed path %q", d.Slug, d.Path)
		}
	}
	if guide == 0 || root == 0 || spec == 0 {
		t.Errorf("expected all three sections populated; guide=%d root=%d specs=%d", guide, root, spec)
	}
}

func TestListIsDeterministicAndSectionOrdered(t *testing.T) {
	first, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	second, _ := List()
	for i := range first {
		if first[i].Slug != second[i].Slug {
			t.Fatalf("List is not deterministic at %d: %q vs %q", i, first[i].Slug, second[i].Slug)
		}
	}
	// The guide must come first — it is what a cold agent should read.
	if first[0].Section != SectionGuide {
		t.Errorf("first doc is %s (%s), want a guide page", first[0].Slug, first[0].Section)
	}
	lastRank := -1
	for _, d := range first {
		r := sectionRank[d.Section]
		if r < lastRank {
			t.Fatalf("section order broken at %s", d.Slug)
		}
		lastRank = r
	}
}

func TestLookupAcceptsTheFormsAModelWillType(t *testing.T) {
	for _, in := range []string{
		"guide/templates",
		"guide/templates.md",
		"docs/guide/templates.md",
		"/docs/guide/templates",
		"./guide/templates",
	} {
		d, err := Lookup(in)
		if err != nil {
			t.Errorf("Lookup(%q): %v", in, err)
			continue
		}
		if d.Slug != "guide/templates" {
			t.Errorf("Lookup(%q) = %q, want guide/templates", in, d.Slug)
		}
	}
	// Top-level docs are ALL-CAPS on disk but must resolve lowercase.
	for _, in := range []string{"architecture", "ARCHITECTURE", "ARCHITECTURE.md"} {
		if d, err := Lookup(in); err != nil || d.Slug != "architecture" {
			t.Errorf("Lookup(%q) = %q, %v; want architecture", in, d.Slug, err)
		}
	}
}

func TestSpecNumberAliases(t *testing.T) {
	// Specs are cross-referenced by number everywhere in the corpus, so the
	// number alone must resolve.
	for _, in := range []string{"23", "specs/23", "specs/23-template-authoring"} {
		d, err := Lookup(in)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", in, err)
		}
		if !strings.HasPrefix(d.Slug, "specs/23-") {
			t.Errorf("Lookup(%q) = %q, want the spec 23 page", in, d.Slug)
		}
	}
}

func TestLookupUnknownSuggests(t *testing.T) {
	_, err := Lookup("guide/template")
	if err == nil {
		t.Fatal("expected an error for an unknown slug")
	}
	if !strings.Contains(err.Error(), "guide/templates") {
		t.Errorf("error should suggest the near miss, got: %v", err)
	}
}

func TestReadReturnsRealMarkdown(t *testing.T) {
	d, body, err := Read("guide/templates")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(string(body), "[[") {
		t.Error("the templates guide should document the [[ ]] delimiters")
	}
	if d.Lines != countLines(body) {
		t.Errorf("indexed line count %d != actual %d", d.Lines, countLines(body))
	}
}

func TestSearchNarrowsWithEveryTerm(t *testing.T) {
	broad, err := Search("template", 50)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	narrow, err := Search("template lint golden", 50)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(broad) == 0 || len(narrow) == 0 {
		t.Fatalf("expected hits for both queries; broad=%d narrow=%d", len(broad), len(narrow))
	}
	if len(narrow) >= len(broad) {
		t.Errorf("adding terms should narrow: broad=%d narrow=%d", len(broad), len(narrow))
	}
	for _, h := range narrow {
		if len(h.Matches) == 0 {
			t.Errorf("%s matched but returned no excerpt", h.Doc.Slug)
		}
		if len(h.Matches) > maxMatchesPerDoc {
			t.Errorf("%s returned %d excerpts, cap is %d", h.Doc.Slug, len(h.Matches), maxMatchesPerDoc)
		}
	}
}

func TestSearchRanksTheObviousPageFirst(t *testing.T) {
	hits, err := Search("template authoring", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits")
	}
	if !strings.Contains(hits[0].Doc.Slug, "template") {
		t.Errorf("top hit for %q is %q; expected a templates page", "template authoring", hits[0].Doc.Slug)
	}
}

func TestSearchIsDeterministic(t *testing.T) {
	a, _ := Search("shared postgres", 10)
	b, _ := Search("shared postgres", 10)
	if len(a) != len(b) {
		t.Fatalf("non-deterministic result count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Doc.Slug != b[i].Doc.Slug || a[i].Score != b[i].Score {
			t.Fatalf("non-deterministic at %d: %v vs %v", i, a[i].Doc.Slug, b[i].Doc.Slug)
		}
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	hits, err := Search("   ", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("empty query should return nothing, got %d", len(hits))
	}
}

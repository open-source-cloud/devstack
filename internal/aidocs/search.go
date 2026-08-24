package aidocs

import (
	"sort"
	"strings"
)

// Hit is one search result: the document plus the matching lines that justify it,
// so a caller can decide whether to spend context on the full document.
type Hit struct {
	Doc     Doc      `json:"doc"`
	Score   int      `json:"score"`
	Matches []string `json:"matches"`
}

// maxMatchesPerDoc caps the excerpt list so a query matching a large reference
// page cannot flood a model's context.
const maxMatchesPerDoc = 4

// Search scores every document against a whitespace-separated query and returns
// the best matches, highest first. Scoring is deliberately simple and
// deterministic — a title hit outweighs a summary hit, which outweighs body hits,
// and ties break on slug — because the corpus is small enough that ranking
// sophistication buys nothing and non-determinism would break golden tests.
//
// A document must match EVERY term to be returned, so adding a term always
// narrows: "template lint" finds the lint section of the templates guide rather
// than every page mentioning templates.
func Search(query string, limit int) ([]Hit, error) {
	if err := load(); err != nil {
		return nil, err
	}
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil, nil
	}
	var hits []Hit
	for _, d := range index.docs {
		_, body, err := Read(d.Slug)
		if err != nil {
			return nil, err
		}
		if h, ok := score(d, string(body), terms); ok {
			hits = append(hits, h)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if ri, rj := sectionRank[hits[i].Doc.Section], sectionRank[hits[j].Doc.Section]; ri != rj {
			return ri < rj
		}
		return hits[i].Doc.Slug < hits[j].Doc.Slug
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// score rates one document, returning ok=false unless every term appears.
func score(d Doc, body string, terms []string) (Hit, bool) {
	lowTitle := strings.ToLower(d.Title)
	lowSlug := strings.ToLower(d.Slug)
	lowSummary := strings.ToLower(d.Summary)
	lowBody := strings.ToLower(body)

	total := 0
	for _, term := range terms {
		n := strings.Count(lowBody, term)
		termScore := 0
		if strings.Contains(lowSlug, term) {
			termScore += 40
		}
		if strings.Contains(lowTitle, term) {
			termScore += 25
		}
		if strings.Contains(lowSummary, term) {
			termScore += 10
		}
		termScore += min(n, 10)
		if termScore == 0 {
			return Hit{}, false
		}
		total += termScore
	}
	return Hit{Doc: d, Score: total, Matches: excerpts(body, terms)}, true
}

// excerpts returns the first few trimmed lines containing any term, so the caller
// sees why a document matched.
func excerpts(body string, terms []string) []string {
	var out []string
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "```") {
			continue
		}
		low := strings.ToLower(line)
		for _, term := range terms {
			if strings.Contains(low, term) {
				out = append(out, cleanSummary(line))
				break
			}
		}
		if len(out) == maxMatchesPerDoc {
			break
		}
	}
	return out
}

// tokenize lowercases and splits a query, dropping punctuation-only fragments.
func tokenize(q string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(q)) {
		f = strings.Trim(f, ".,:;!?\"'()[]{}")
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

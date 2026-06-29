package git

import (
	"context"
	"fmt"
	"strings"
)

// Status is the cross-repo health snapshot parsed from one cheap
// `git status --porcelain=v2 --branch` call.
type Status struct {
	Branch       string `json:"branch"`       // current branch ("" when detached)
	Detached     bool   `json:"detached"`     //
	Upstream     string `json:"upstream"`     // tracking ref ("" if none)
	Ahead        int    `json:"ahead"`        //
	Behind       int    `json:"behind"`       //
	UpstreamGone bool   `json:"upstreamGone"` // upstream configured but no longer exists
	Staged       int    `json:"staged"`       //
	Unstaged     int    `json:"unstaged"`     //
	Untracked    int    `json:"untracked"`    //
	Conflicts    int    `json:"conflicts"`    //
}

// Dirty reports whether the working tree has any uncommitted change.
func (s *Status) Dirty() bool {
	return s.Staged+s.Unstaged+s.Untracked+s.Conflicts > 0
}

// Status runs the porcelain-v2 status of the repo at dir. --no-optional-locks
// keeps a concurrent read from fighting an index.lock; -z makes output robust to
// paths with spaces/newlines.
func (g *Git) Status(ctx context.Context, dir string) (*Status, error) {
	out, err := g.run(ctx, dir, "--no-optional-locks", "status", "--porcelain=v2", "--branch", "-z")
	if err != nil {
		return nil, err
	}
	return parseStatus(out), nil
}

// parseStatus parses the NUL-separated porcelain-v2 records. The upstream-gone
// state has no explicit field — it is inferred from an upstream being configured
// while branch.ab is absent (spec 06).
func parseStatus(data []byte) *Status {
	s := &Status{}
	recs := strings.Split(string(data), "\x00")
	hasUpstream, hasAB := false, false

	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if rec == "" {
			continue
		}
		switch {
		case strings.HasPrefix(rec, "# branch.head "):
			head := strings.TrimPrefix(rec, "# branch.head ")
			if head == "(detached)" {
				s.Detached = true
			} else {
				s.Branch = head
			}
		case strings.HasPrefix(rec, "# branch.upstream "):
			s.Upstream = strings.TrimPrefix(rec, "# branch.upstream ")
			hasUpstream = true
		case strings.HasPrefix(rec, "# branch.ab "):
			hasAB = true
			fmt.Sscanf(strings.TrimPrefix(rec, "# branch.ab "), "+%d -%d", &s.Ahead, &s.Behind)
		case strings.HasPrefix(rec, "1 "):
			countXY(s, fieldN(rec, 1))
		case strings.HasPrefix(rec, "2 "):
			countXY(s, fieldN(rec, 1))
			// A renamed/copied entry is followed by its original path as a
			// separate NUL record (-z mode) — consume it so it is not miscounted.
			i++
		case strings.HasPrefix(rec, "u "):
			s.Conflicts++
		case strings.HasPrefix(rec, "? "):
			s.Untracked++
			// "! " (ignored) records are deliberately skipped.
		}
	}
	if hasUpstream && !hasAB {
		s.UpstreamGone = true
	}
	return s
}

// countXY tallies a changed entry's two-char staged/worktree status code.
func countXY(s *Status, xy string) {
	if len(xy) != 2 {
		return
	}
	if xy[0] != '.' {
		s.Staged++
	}
	if xy[1] != '.' {
		s.Unstaged++
	}
}

// fieldN returns the nth space-separated field of a record.
func fieldN(rec string, n int) string {
	f := strings.Fields(rec)
	if n < len(f) {
		return f[n]
	}
	return ""
}

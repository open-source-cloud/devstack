// Package docs embeds devstack's documentation corpus into the binary via
// go:embed (spec 32). The same markdown that renders on the repo is what
// `devstack ai docs` prints and what the MCP server serves as devstack://docs/…
// resources, so an AI agent working in a devstack workspace reads exactly the
// documentation a human would — no separately-authored, separately-drifting
// "agent docs".
//
// Consequence for CI: docs/ is now a BUILD INPUT. .github/workflows/ci.yml must
// not skip checks for doc-only changes, or a broken cross-link could ship inside
// a binary without a single check running. internal/aidocs owns the index, slug
// scheme and search over this FS.
package docs

import "embed"

//go:embed *.md guide/*.md specs/*.md
var corpus embed.FS

// FS is the embedded documentation root: the top-level guides (ARCHITECTURE.md,
// DECISIONS.md, …) plus the guide/ book and the specs/ set.
var FS = corpus

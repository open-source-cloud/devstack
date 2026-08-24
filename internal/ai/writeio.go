package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Fence markers delimiting the devstack-owned block inside a file devstack does
// not own. Kept stable forever — changing them would orphan every block already
// committed in a user's repository. HTML comments so they render invisibly in
// markdown, adapting the /etc/hosts idiom from internal/dns.
const (
	markerBegin = "<!-- >>> devstack >>> (managed — do not edit; regenerate with `devstack ai install`) -->"
	markerEnd   = "<!-- <<< devstack <<< -->"
)

// MergeMode says how Write reconciles an Artifact with what is already on disk.
//
// This is the one real deviation from internal/ide, which only ever writes whole
// files. Two of the three emitted target families land in files the USER owns —
// AGENTS.md, CLAUDE.md and .mcp.json — where clobbering the whole file would
// destroy hand-written content on every regeneration.
type MergeMode int

const (
	// MergeWhole replaces the file. Only for files devstack fully owns, i.e.
	// everything under .claude/skills/devstack*/.
	MergeWhole MergeMode = iota
	// MergeFence replaces only the marker-fenced block, leaving everything
	// outside it untouched. For user-owned markdown.
	MergeFence
	// MergeJSONKey sets a single key path in a JSON document, preserving every
	// other key. For user-owned JSON such as .mcp.json.
	MergeJSONKey
)

// Artifact is one file devstack emits. For MergeFence and MergeJSONKey, Data is
// the BLOCK or the VALUE — not the finished file — because Build must not read
// the disk: keeping Build pure is what makes the golden tests hermetic.
type Artifact struct {
	Path     string    `json:"-"`
	Rel      string    `json:"path"`
	Kind     string    `json:"kind"`
	Data     []byte    `json:"-"`
	Merge    MergeMode `json:"-"`
	JSONPath []string  `json:"-"` // MergeJSONKey only, e.g. {"mcpServers", "devstack"}
}

// WriteResult reports what Write changed for one artifact.
type WriteResult struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Changed bool   `json:"changed"`
}

// Write materializes every artifact and reports what changed. Re-running with an
// unchanged pack writes nothing, so `devstack ai install` is safe to run in a
// hook or a loop.
func Write(arts []Artifact) ([]WriteResult, error) {
	out := make([]WriteResult, 0, len(arts))
	for _, a := range arts {
		want, err := merged(a)
		if err != nil {
			return out, err
		}
		changed, err := writeIfChanged(a.Path, want)
		if err != nil {
			return out, err
		}
		out = append(out, WriteResult{Path: a.Rel, Kind: a.Kind, Changed: changed})
	}
	return out, nil
}

// UpToDate reports whether every artifact already matches disk. This is the basis
// for --check, so it must apply the same merge each mode would perform rather
// than comparing raw bytes.
func UpToDate(arts []Artifact) (bool, error) {
	for _, a := range arts {
		want, err := merged(a)
		if err != nil {
			return false, err
		}
		existing, err := os.ReadFile(a.Path)
		if err != nil || !bytes.Equal(existing, want) {
			return false, nil
		}
	}
	return true, nil
}

// Stale returns the artifacts whose on-disk content differs, so --check can name
// them instead of just failing.
func Stale(arts []Artifact) ([]Artifact, error) {
	var out []Artifact
	for _, a := range arts {
		want, err := merged(a)
		if err != nil {
			return nil, err
		}
		existing, err := os.ReadFile(a.Path)
		if err != nil || !bytes.Equal(existing, want) {
			out = append(out, a)
		}
	}
	return out, nil
}

// merged computes the full file content an artifact should produce, reading the
// current file for the two merge modes that preserve user content.
func merged(a Artifact) ([]byte, error) {
	switch a.Merge {
	case MergeWhole:
		return a.Data, nil
	case MergeFence:
		existing, err := readFileOrEmpty(a.Path)
		if err != nil {
			return nil, err
		}
		return applyFence(existing, a.Data), nil
	case MergeJSONKey:
		existing, err := readFileOrEmpty(a.Path)
		if err != nil {
			return nil, err
		}
		return applyJSONKey(existing, a.JSONPath, a.Data)
	}
	return nil, fmt.Errorf("artifact %s: unknown merge mode %d", a.Rel, a.Merge)
}

func readFileOrEmpty(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return b, nil
}

// applyFence replaces the devstack block in existing, or appends one when there
// is none. Content outside the fence is preserved byte for byte.
func applyFence(existing, block []byte) []byte {
	fenced := markerBegin + "\n" + strings.TrimRight(string(block), "\n") + "\n" + markerEnd + "\n"
	before, after, had := splitFence(string(existing))
	if had {
		return []byte(before + fenced + after)
	}
	if len(existing) == 0 {
		return []byte(fenced)
	}
	// Append, guaranteeing exactly one blank line before the block.
	return []byte(strings.TrimRight(string(existing), "\n") + "\n\n" + fenced)
}

// splitFence returns the content before the begin marker, after the end marker,
// and whether a complete fence was found, so before+block+after round-trips.
// Mirrors internal/dns/hosts.go, which has carried this idiom in production for
// /etc/hosts.
func splitFence(s string) (before, after string, had bool) {
	bi := strings.Index(s, markerBegin)
	if bi < 0 {
		return s, "", false
	}
	ei := strings.Index(s, markerEnd)
	if ei < 0 || ei < bi {
		return s, "", false
	}
	end := ei + len(markerEnd)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	return s[:bi], s[end:], true
}

// FenceContent returns what is currently inside the devstack block of a file, so
// callers can diff or report it without re-deriving the markers.
func FenceContent(existing []byte) (string, bool) {
	s := string(existing)
	bi := strings.Index(s, markerBegin)
	ei := strings.Index(s, markerEnd)
	if bi < 0 || ei < bi {
		return "", false
	}
	return strings.TrimSpace(s[bi+len(markerBegin) : ei]), true
}

// applyJSONKey sets one key path in a JSON document, preserving every other key
// and its value verbatim. An absent or empty document becomes a new object.
//
// It intentionally re-marshals the whole file, which reformats a user's
// hand-formatting. That is documented, visible through --check, and better than
// the alternative of refusing to touch a file that already exists.
func applyJSONKey(existing []byte, path []string, value []byte) ([]byte, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("applyJSONKey: empty key path")
	}
	root := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			return nil, fmt.Errorf("existing JSON is malformed: %w", err)
		}
	}
	if err := setRawPath(root, path, value); err != nil {
		return nil, err
	}
	return marshalJSON(root)
}

// setRawPath walks (creating as needed) the object path and sets the leaf.
func setRawPath(node map[string]json.RawMessage, path []string, value []byte) error {
	key := path[0]
	if len(path) == 1 {
		node[key] = json.RawMessage(bytes.TrimSpace(value))
		return nil
	}
	child := map[string]json.RawMessage{}
	if raw, ok := node[key]; ok && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &child); err != nil {
			return fmt.Errorf("existing key %q is not an object: %w", key, err)
		}
	}
	if err := setRawPath(child, path[1:], value); err != nil {
		return err
	}
	encoded, err := json.Marshal(child)
	if err != nil {
		return err
	}
	node[key] = encoded
	return nil
}

// marshalJSON renders v as stable, 2-space-indented JSON with a trailing newline.
// HTML escaping is off so URLs survive verbatim; map keys are sorted by
// encoding/json, which is what keeps the output deterministic.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const tempPrefix = ".devstack-ai-tmp-"

// writeIfChanged writes data to path only when the on-disk content differs,
// returning whether a write occurred. The write is atomic: a temp file in the
// same directory is fsync'd, chmod'd, then renamed over the target, so a crash
// leaves either the old file or the new one, never a half-written one.
func writeIfChanged(path string, data []byte) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return false, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", dir, err)
	}
	sweepTemp(dir)
	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return false, fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return false, fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	return true, nil
}

// sweepTemp removes stale temp files a previously-killed run left in dir.
func sweepTemp(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), tempPrefix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

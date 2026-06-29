package generate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// StateVersion is the schema version of the per-stack state.json.
const StateVersion = 1

// State is the per-stack rebuild-hash ledger persisted at .devstack/state.json.
// It maps each build context (keyed by service name) to the SHA-256 of its full
// rendered input set, so a regenerate can mark ONLY the changed contexts for a
// `docker compose build --no-cache <svc>` (spec 02 acceptance #4). It is
// timestamp-independent (WSL2/9p-safe) — content hashes, not mtimes.
type State struct {
	Version     int               `json:"version"`
	Stack       string            `json:"stack"`
	BuildHashes map[string]string `json:"buildHashes"`
}

// loadState reads a stack's state.json, returning an empty (non-nil) State when
// the file is absent or unreadable (first generation).
func loadState(path string) State {
	s := State{Version: StateVersion, BuildHashes: map[string]string{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	if s.BuildHashes == nil {
		s.BuildHashes = map[string]string{}
	}
	return s
}

// marshal renders the State as stable, indented JSON (sorted keys via the Go
// json encoder's map-key ordering) with a trailing newline.
func (s State) marshal() ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal state: %w", err)
	}
	return append(b, '\n'), nil
}

// hashBuildContext computes the SHA-256 over a build context's full rendered
// input set: every build file (sorted by path) and the entire build config
// (context/dockerfile/args, in any shape). Changing any rendered byte or any
// build setting changes the hash; unrelated services are untouched.
func hashBuildContext(files map[string][]byte, build any) string {
	h := sha256.New()
	for _, name := range sortedKeys(files) {
		fmt.Fprintf(h, "F:%s:%d:", name, len(files[name]))
		h.Write(files[name])
		h.Write([]byte{0})
	}
	io.WriteString(h, "B:")
	hashValue(h, build)
	return hex.EncodeToString(h.Sum(nil))
}

// hashValue writes a canonical, order-independent representation of a YAML-shaped
// value (maps keyed in sorted order, lists in order, scalars via fmt) so the hash
// is identical regardless of map iteration order or build-args shape.
func hashValue(h io.Writer, v any) {
	switch t := v.(type) {
	case map[string]any:
		io.WriteString(h, "{")
		for _, k := range sortedKeys(t) {
			fmt.Fprintf(h, "%s=", k)
			hashValue(h, t[k])
			io.WriteString(h, ";")
		}
		io.WriteString(h, "}")
	case []any:
		io.WriteString(h, "[")
		for _, e := range t {
			hashValue(h, e)
			io.WriteString(h, ",")
		}
		io.WriteString(h, "]")
	default:
		fmt.Fprintf(h, "%v", t)
	}
}

// changedContexts returns the build-context keys whose hash differs between the
// previous and current state (added or modified), sorted for determinism.
func changedContexts(prev, cur State) []string {
	var changed []string
	for key, h := range cur.BuildHashes {
		if prev.BuildHashes[key] != h {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

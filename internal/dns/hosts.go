// Package dns manages the marker-fenced /etc/hosts block devstack owns for
// resolving <service>.<project>.localhost on OS-resolver clients (spec 05). A
// fenced block is the only consistently reliable mechanism cross-platform
// (systemd-resolved isn't guaranteed on WSL2/minimal Ubuntu; macOS ≤15 resolves
// *.localhost only in browsers; Firefox ignores /etc/hosts for *.localhost).
//
// All operations are idempotent and operate on an injectable path so they are
// fully testable against a temp file; writing the real /etc/hosts needs root
// (the CLI surfaces a clear sudo remediation). Entries outside the fence are
// never touched, and the block is removed cleanly on uninstall.
package dns

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// DefaultHostsPath is the OS hosts file (override in tests).
const DefaultHostsPath = "/etc/hosts"

// Loopback is the address every managed host resolves to.
const Loopback = "127.0.0.1"

// Fence markers delimiting the devstack-owned block. Kept stable forever — they
// are how we find and replace our block without disturbing the rest of the file.
const (
	markerBegin = "# >>> devstack >>> (managed — do not edit)"
	markerEnd   = "# <<< devstack <<<"
)

// Block renders the fenced block for the given hosts (deduped, sorted), each
// mapped to 127.0.0.1. Returns "" when there are no hosts.
func Block(hosts []string) string {
	clean := normalize(hosts)
	if len(clean) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(markerBegin)
	b.WriteByte('\n')
	for _, h := range clean {
		fmt.Fprintf(&b, "%s\t%s\n", Loopback, h)
	}
	b.WriteString(markerEnd)
	b.WriteByte('\n')
	return b.String()
}

// Apply idempotently writes the devstack block into the file at path: it replaces
// an existing fenced block (or appends one) and leaves everything else intact.
// With no hosts it removes the block. Returns whether the file changed.
func Apply(path string, hosts []string) (bool, error) {
	if len(normalize(hosts)) == 0 {
		return Remove(path)
	}
	existing, err := readFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
		existing = "" // absent hosts file → create it with just our block
	}
	before, after, had := splitFence(existing)
	block := Block(hosts)

	var next string
	if had {
		next = before + block + after
	} else {
		next = ensureTrailingNewline(existing) + block
	}
	if next == existing {
		return false, nil
	}
	return true, writeFile(path, next)
}

// Remove strips the devstack block (no-op if absent). Returns whether it changed.
func Remove(path string) (bool, error) {
	existing, err := readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	before, after, had := splitFence(existing)
	if !had {
		return false, nil
	}
	next := strings.TrimRight(before, "\n")
	if next != "" {
		next += "\n"
	}
	if rest := strings.TrimLeft(after, "\n"); rest != "" {
		next += rest
	}
	return true, writeFile(path, next)
}

// Present returns the hostnames currently inside the devstack block.
func Present(path string) ([]string, error) {
	existing, err := readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_, _, had := splitFence(existing)
	if !had {
		return nil, nil
	}
	var out []string
	in := false
	sc := bufio.NewScanner(strings.NewReader(existing))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.TrimSpace(line) == markerBegin:
			in = true
		case strings.TrimSpace(line) == markerEnd:
			in = false
		case in:
			if fields := strings.Fields(line); len(fields) >= 2 {
				out = append(out, fields[1])
			}
		}
	}
	return out, sc.Err()
}

// Missing returns the desired hosts not currently present in the block (for a
// `dns status` diff without mutating anything).
func Missing(path string, want []string) ([]string, error) {
	present, err := Present(path)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, h := range present {
		have[h] = true
	}
	var missing []string
	for _, h := range normalize(want) {
		if !have[h] {
			missing = append(missing, h)
		}
	}
	return missing, nil
}

// --- helpers ---------------------------------------------------------------

// splitFence returns the content before the begin marker, after the end marker,
// and whether a complete fence was found. `before` keeps its trailing newline;
// `after` keeps its leading newline, so before+block+after round-trips.
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
	// Consume the newline immediately after the end marker so the block owns it.
	if end < len(s) && s[end] == '\n' {
		end++
	}
	return s[:bi], s[end:], true
}

func normalize(hosts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func ensureTrailingNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", err
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(b), nil
}

// writeFile preserves the file's existing mode (default 0644) and writes in place.
// /etc/hosts must be edited in place (atomic rename across the / mount can fail
// and some platforms require the inode preserved); callers hold the needed privs.
func writeFile(path, content string) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

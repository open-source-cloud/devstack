// Package alias implements the multi-alias mechanism (DECISIONS D14, spec 07):
// one binary invocable as `devstack`, `rq`, `uranus`, … via argv[0] dispatch.
//
//   - Reads filepath.Base(os.Args[0]); if it matches a registered alias, the
//     tool sets per-alias branding but runs the IDENTICAL cobra command tree
//     (the git/busybox pattern).
//   - `alias add <name>` symlinks the binary into XDG_BIN_HOME and records it in
//     the global registry; `alias remove` reverses it.
//   - `--as <name>` overrides the detected name for tests.
package alias

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/open-source-cloud/devdock-go/internal/xdg"
)

// Canonical is the tool's primary name; always valid regardless of the registry.
const Canonical = "devstack"

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Registry is the persisted set of installed alias names.
type Registry struct {
	Aliases []string `json:"aliases"`
}

func registryPath() string { return filepath.Join(xdg.ConfigHome(), "aliases.json") }

// Load reads the registry, returning an empty one when the file is absent.
func Load() (*Registry, error) {
	b, err := os.ReadFile(registryPath())
	if os.IsNotExist(err) {
		return &Registry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read alias registry: %w", err)
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse alias registry: %w", err)
	}
	return &r, nil
}

// Save writes the registry atomically and durably: a uniquely-named temp file in
// the same directory, fsync'd, then renamed into place, then the directory
// fsync'd. The unique temp name avoids two concurrent saves clobbering one
// temp file.
func (r *Registry) Save() error {
	sort.Strings(r.Aliases)
	dir, err := xdg.EnsureDir(xdg.ConfigHome())
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "aliases-*.json.tmp")
	if err != nil {
		return fmt.Errorf("write alias registry: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once renamed
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("write alias registry: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync alias registry: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close alias registry: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, registryPath()); err != nil {
		return fmt.Errorf("save alias registry: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Has reports whether name is a registered alias.
func (r *Registry) Has(name string) bool {
	return slices.Contains(r.Aliases, name)
}

// ValidateName rejects names that aren't safe as a command/symlink basename.
func ValidateName(name string) error {
	if name == Canonical {
		return fmt.Errorf("%q is the canonical name, not an alias", name)
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid alias %q: use lowercase letters, digits, '-' or '_', starting with a letter", name)
	}
	return nil
}

// Add installs an alias: symlink XDG_BIN_HOME/<name> → this binary, and record
// it. Idempotent when the symlink already points at us.
func Add(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve own path: %w", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	binDir, err := xdg.EnsureDir(xdg.BinHome())
	if err != nil {
		return "", err
	}
	link := filepath.Join(binDir, name)

	if existing, err := os.Readlink(link); err == nil {
		if existing != self {
			return "", fmt.Errorf("%s already exists and points elsewhere (%s); refusing to overwrite", link, existing)
		}
	} else if _, err := os.Lstat(link); err == nil {
		return "", fmt.Errorf("%s already exists and is not a devstack symlink; refusing to overwrite", link)
	} else {
		if err := os.Symlink(self, link); err != nil {
			return "", fmt.Errorf("create alias symlink: %w (note: native Windows lacks reliable symlinks — use WSL2)", err)
		}
	}

	reg, err := Load()
	if err != nil {
		return "", err
	}
	if !reg.Has(name) {
		reg.Aliases = append(reg.Aliases, name)
		if err := reg.Save(); err != nil {
			return "", err
		}
	}
	return link, nil
}

// Remove uninstalls an alias: delete the symlink (only when it resolves to THIS
// binary) and drop it from the registry. A missing symlink is treated as already
// gone (idempotent); a non-symlink file at the path is left untouched and errors.
func Remove(name string) error {
	binDir := xdg.BinHome()
	link := filepath.Join(binDir, name)
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own path: %w", err)
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return fmt.Errorf("resolve own path: %w", err)
	}

	target, rerr := os.Readlink(link)
	switch {
	case rerr == nil:
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(binDir, resolved)
		}
		if r, e := filepath.EvalSymlinks(resolved); e == nil {
			resolved = r
		}
		if resolved != self {
			return fmt.Errorf("%s points to %s, not this binary; not removing", link, target)
		}
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("remove alias symlink: %w", err)
		}
	case os.IsNotExist(rerr):
		// Already gone — fall through to heal the registry.
	default:
		// Path exists but is not a symlink (or is unreadable): don't touch it.
		if _, lerr := os.Lstat(link); lerr == nil {
			return fmt.Errorf("%s exists and is not a devstack symlink; not removing", link)
		}
		return fmt.Errorf("inspect alias symlink %s: %w", link, rerr)
	}

	reg, err := Load()
	if err != nil {
		return err
	}
	out := reg.Aliases[:0]
	for _, a := range reg.Aliases {
		if a != name {
			out = append(out, a)
		}
	}
	reg.Aliases = out
	return reg.Save()
}

// ResolveInvocation determines the effective invoked name from argv, honoring a
// `--as <name>` / `--as=<name>` override, and returns argv with that flag
// stripped (cobra never sees it). args should be os.Args.
func ResolveInvocation(args []string) (name string, cleaned []string) {
	if len(args) == 0 {
		return Canonical, args
	}
	name = filepath.Base(args[0])
	cleaned = append(cleaned, args[0])
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			// End-of-flags terminator: everything after it (including a literal
			// "--as") belongs to the subcommand — pass through verbatim.
			cleaned = append(cleaned, args[i:]...)
			return name, cleaned
		case a == "--as":
			if i+1 < len(args) {
				name = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--as="):
			name = strings.TrimPrefix(a, "--as=")
		default:
			cleaned = append(cleaned, a)
		}
	}
	return name, cleaned
}

// Branding is the per-alias presentation (env-var prefix, display name). v1 keeps
// it minimal; the command tree itself is identical across aliases.
type Branding struct {
	Name      string
	EnvPrefix string
}

// BrandFor returns branding for an invoked name.
func BrandFor(name string) Branding {
	if name == "" {
		name = Canonical
	}
	return Branding{
		Name:      name,
		EnvPrefix: strings.ToUpper(strings.NewReplacer("-", "_").Replace(name)),
	}
}

// Package store owns the machine-global devstack home at ~/.devstack/ (Linux/WSL
// and macOS): a user-owned place to keep a store config file, custom service
// templates, and the global shared-services stack (Postgres / Redis / MinIO).
//
// Unlike the XDG state ledger (internal/xdg + internal/state, which is disposable
// machine bookkeeping keyed by Docker context), this is the durable,
// human-editable store — templates you author and the shared infrastructure
// definition shared across every workspace. The location is `~/.devstack`,
// overridable with $DEVSTACK_HOME.
//
//	~/.devstack/
//	  config.yaml     # the store config: the global shared services (pg/redis/minio…)
//	  templates/      # custom templates; override the embedded built-ins by name
//	  shared/         # the global shared stack's generated artifacts
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"

	"github.com/open-source-cloud/devstack/internal/config"
)

// Layout constants.
const (
	HomeEnv      = "DEVSTACK_HOME"
	DirName      = ".devstack"
	ConfigFile   = "config.yaml"
	TemplatesDir = "templates"
	SharedDir    = "shared"
	SnapshotsDir = "snapshots"
)

// Home returns the devstack home directory: $DEVSTACK_HOME if set, else
// ~/.devstack. Falls back to ".devstack" in the CWD only if the home dir is
// unresolvable (extremely rare).
func Home() string {
	if h := os.Getenv(HomeEnv); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return DirName
	}
	return filepath.Join(home, DirName)
}

// ConfigPath is the store config file path.
func ConfigPath() string { return filepath.Join(Home(), ConfigFile) }

// TemplatesPath is the custom-templates directory.
func TemplatesPath() string { return filepath.Join(Home(), TemplatesDir) }

// SharedPath is the global shared-stack artifacts directory.
func SharedPath() string { return filepath.Join(Home(), SharedDir) }

// SnapshotsPath is the db-snapshot store for one workspace: dumps captured by
// `devstack db snapshot` live under ~/.devstack/snapshots/<workspace>/ (spec 15).
// Keyed by workspace so two checkouts' snapshots never collide.
func SnapshotsPath(workspace string) string {
	return filepath.Join(Home(), SnapshotsDir, workspace)
}

// Initialized reports whether the store config file exists.
func Initialized() bool {
	info, err := os.Stat(ConfigPath())
	return err == nil && !info.IsDir()
}

// EnsureLayout creates the home directory tree (idempotent).
func EnsureLayout() error {
	for _, d := range []string{Home(), TemplatesPath(), SharedPath()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// Config is the store config file (~/.devstack/config.yaml): the machine-global
// definition of the shared services every workspace can attach to.
type Config struct {
	APIVersion string                      `yaml:"apiVersion"`
	Kind       string                      `yaml:"kind"`
	Shared     map[string]config.SharedSvc `yaml:"shared"`
	// Backend is the machine-global default for WHERE the shared stack runs (spec
	// 21): a `docker context` or DOCKER_HOST endpoint. nil = the local daemon. A
	// workspace.yaml `backend:` block, when present, overrides this per workspace.
	Backend *config.BackendConfig `yaml:"backend,omitempty"`
	// Telemetry is the per-user/per-machine opt-in usage-telemetry consent
	// (spec 20). It lives here — never in workspace.yaml (must not be committed)
	// and never in state.db (it's user policy, not ledger state). Default OFF: a
	// zero value / missing block means telemetry has never been enabled.
	Telemetry TelemetryConfig `yaml:"telemetry"`
}

// TelemetryConfig is the persisted telemetry consent. Enabled defaults to false
// and is only ever set true by an explicit `telemetry enable`. InstallID is a
// random UUIDv4 minted on first enable (rotatable, not machine-derived); it is
// cleared when telemetry is disabled.
type TelemetryConfig struct {
	Enabled   bool   `yaml:"enabled"`
	InstallID string `yaml:"installId,omitempty"`
	ConsentAt string `yaml:"consentAt,omitempty"`
}

// DefaultConfig is the seed written by `store init`: one warm Postgres, Redis,
// and MinIO (S3-compatible), each rendered from a built-in template.
func DefaultConfig() Config {
	return Config{
		APIVersion: config.APIVersion,
		Kind:       "Store",
		Shared: map[string]config.SharedSvc{
			"postgres": {Template: "postgres", Params: map[string]any{"version": "16"}},
			"redis":    {Template: "redis"},
			"minio":    {Template: "minio"},
		},
	}
}

// Load reads the store config. ok is false when the store is not initialized.
func Load() (*Config, bool, error) {
	b, err := os.ReadFile(ConfigPath())
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read store config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, false, fmt.Errorf("%s: %s", ConfigPath(), yaml.FormatError(err, false, true))
	}
	return &c, true, nil
}

// Save writes the store config atomically (temp file + rename) with 0644 perms.
func (c Config) Save() error {
	if err := EnsureLayout(); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal store config: %w", err)
	}
	dir := Home()
	tmp, err := os.CreateTemp(dir, ".config-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("write store config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, ConfigPath())
}

// loadOrDefault returns the persisted store config, or a fresh DefaultConfig when
// the store has not been initialized yet. Used by the telemetry consent mutators
// so `telemetry enable` works before `store init`.
func loadOrDefault() (*Config, error) {
	cfg, ok, err := Load()
	if err != nil {
		return nil, err
	}
	if !ok {
		c := DefaultConfig()
		return &c, nil
	}
	return cfg, nil
}

// TelemetryConsent reads the persisted telemetry consent. A missing/uninitialized
// store means "never decided" → OFF (default). It never errors on absence.
func TelemetryConsent() (TelemetryConfig, error) {
	cfg, ok, err := Load()
	if err != nil {
		return TelemetryConfig{}, err
	}
	if !ok {
		return TelemetryConfig{}, nil
	}
	return cfg.Telemetry, nil
}

// SetTelemetry flips the persisted consent and saves the store config (creating
// the store with defaults if needed). Enabling mints a random UUIDv4 install id
// and stamps consentAt if not already set; disabling clears the install id so a
// disabled user carries no correlatable identifier. Returns the resulting consent.
func SetTelemetry(enabled bool) (TelemetryConfig, error) {
	cfg, err := loadOrDefault()
	if err != nil {
		return TelemetryConfig{}, err
	}
	cfg.Telemetry.Enabled = enabled
	if enabled {
		if cfg.Telemetry.InstallID == "" {
			cfg.Telemetry.InstallID = uuid.NewString()
		}
		if cfg.Telemetry.ConsentAt == "" {
			cfg.Telemetry.ConsentAt = time.Now().UTC().Format(time.RFC3339)
		}
	} else {
		cfg.Telemetry.InstallID = ""
		cfg.Telemetry.ConsentAt = ""
	}
	if err := cfg.Save(); err != nil {
		return TelemetryConfig{}, err
	}
	return cfg.Telemetry, nil
}

// templatesReadme is seeded into ~/.devstack/templates so the dir is discoverable.
const templatesReadme = `# ~/.devstack/templates

Custom service templates live here. A template is a directory with a
` + "`template.yaml`" + ` (see ` + "`devstack template init <name>`" + `). A template here
**overrides a built-in of the same name** — so dropping a ` + "`postgres/`" + ` here
customizes the shared Postgres for every workspace.

Validate one with ` + "`devstack template lint <dir>`" + `.
`

// SeedTemplatesReadme writes the templates README if absent.
func SeedTemplatesReadme() error {
	path := filepath.Join(TemplatesPath(), "README.md")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(templatesReadme), 0o644)
}

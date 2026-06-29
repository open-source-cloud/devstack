// Package selfupdate implements the version check + `self update` flow (spec 14,
// DECISIONS D13): detect install method → resolve the latest GitHub release →
// download the asset for the running GOOS/GOARCH → verify its SHA-256 → atomically
// replace the running binary (write-temp-then-rename, never truncate-in-place).
//
// This is the thin v1: checksum-only integrity (release signing is deferred —
// it prints a one-line warning that signatures are not yet enforced, and never
// silently downgrades to "no integrity check"). Package-managed installs are
// refused with the exact upgrade command (a correct outcome, not an error).
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// describeRe matches the `git describe` suffix (e.g. -11-gabc1234) so a real
// release tag whose prerelease merely starts with 'g' (v1.0.0-grpc.1) is not
// mistaken for a dev build.
var describeRe = regexp.MustCompile(`-[0-9]+-g[0-9a-f]+`)

// Repo is the GitHub repository releases are pulled from.
const Repo = "open-source-cloud/devstack"

// Binary is the released binary / archive name stem.
const Binary = "devstack"

// APIBase is the GitHub API base for this repo's releases, overridable so tests
// can point at an httptest server. Asset downloads also go through the API
// (Accept: octet-stream) so private repos work with a token.
var APIBase = "https://api.github.com/repos/" + Repo

// httpClient bounds every network call so an outage never hangs a command.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// token returns a GitHub token from the environment (raises rate limits; required
// while the repo/releases are private). Injected via the process env.
var tokenFn = defaultToken

// CheckResult is the outcome of comparing the running version to the latest.
type CheckResult struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"updateAvailable"`
	Dev       bool   `json:"devBuild"` // running a non-release (dev/dirty) build
}

// Check resolves the latest release and compares it to current. A dev/dirty build
// is never reported as "an update is available" by version math (it has no clean
// semver to compare), but Latest is still populated so the caller can offer it.
func Check(ctx context.Context, current string) (*CheckResult, error) {
	latest, err := LatestTag(ctx)
	if err != nil {
		return nil, err
	}
	res := &CheckResult{Current: current, Latest: latest, Dev: IsDevBuild(current)}
	if !res.Dev && semver.IsValid(current) && semver.IsValid(latest) {
		res.Available = semver.Compare(latest, current) > 0
	}
	return res, nil
}

// IsDevBuild reports whether v is a non-release build (the default "dev", a
// `git describe` build like v0.1.0-11-gabc1234, or a -dirty tree) for which
// semver comparison against a release tag is meaningless.
func IsDevBuild(v string) bool {
	return v == "" || v == "dev" || strings.HasSuffix(v, "-dirty") || describeRe.MatchString(v) || !semver.IsValid(v)
}

// LatestTag resolves the newest release tag, trying /releases/latest first and
// falling back to the /releases list (covers pre-release-only repos and the brief
// post-publish API-cache window).
func LatestTag(ctx context.Context) (string, error) {
	if tag, err := latestFromEndpoint(ctx, APIBase+"/releases/latest"); err == nil && tag != "" {
		return tag, nil
	}
	tag, err := firstFromList(ctx, APIBase+"/releases")
	if err != nil {
		return "", err
	}
	if tag == "" {
		return "", fmt.Errorf("no releases found for %s", Repo)
	}
	return tag, nil
}

func latestFromEndpoint(ctx context.Context, url string) (string, error) {
	body, err := githubGET(ctx, url)
	if err != nil {
		return "", err
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}
	return r.TagName, nil
}

func firstFromList(ctx context.Context, url string) (string, error) {
	body, err := githubGET(ctx, url)
	if err != nil {
		return "", err
	}
	var rs []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rs); err != nil {
		return "", err
	}
	if len(rs) == 0 {
		return "", nil
	}
	return rs[0].TagName, nil
}

// asset is one release asset as reported by the GitHub API.
type asset struct {
	Name string `json:"name"`
	URL  string `json:"url"` // API asset URL (honors the token; works for private)
}

// releaseAssets returns the assets of a release by tag, keyed by filename. The
// API asset URLs work for private repos (with a token) and public repos
// (anonymously) — unlike the browser download URL, which 404s while private.
func releaseAssets(ctx context.Context, tag string) (map[string]asset, error) {
	body, err := githubGET(ctx, APIBase+"/releases/tags/"+tag)
	if err != nil {
		return nil, err
	}
	var r struct {
		Assets []asset `json:"assets"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	m := make(map[string]asset, len(r.Assets))
	for _, a := range r.Assets {
		m[a.Name] = a
	}
	return m, nil
}

// githubGET performs an authenticated (when a token is set) GitHub API GET.
func githubGET(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if t := tokenFn(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact GitHub: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API %s returned %s (set GITHUB_TOKEN if the repo is private)", url, resp.Status)
	}
	return body, nil
}

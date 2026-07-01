package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"
)

// Options tune an Update.
type Options struct {
	Version string // pin a release tag (e.g. "v0.2.0"); "" = latest
	Force   bool   // re-install even when already up-to-date (repair a corrupt binary);
	// NEVER overrides the package-manager CanSelfReplace refusal (spec 26/14).
}

// Result reports what Update did (or why it refused).
type Result struct {
	From     string
	To       string
	Replaced bool
	UpToDate bool
	Install  Install
}

// Update performs the self-update flow for the running binary. When the install
// is package-managed (or not writable), it returns a Result with Replaced=false
// and Install.Hint set — the caller prints the hint and exits 0 (a correct
// outcome, not a failure).
func Update(ctx context.Context, current string, opts Options) (*Result, error) {
	inst, err := DetectInstall(ctx)
	if err != nil {
		return nil, err
	}
	res := &Result{From: current, Install: inst}
	if !inst.CanSelfReplace() {
		return res, nil
	}

	tag := opts.Version
	if tag == "" {
		tag, err = LatestTag(ctx)
		if err != nil {
			return res, err
		}
	}
	res.To = tag

	// Nothing to do when already on (or ahead of) the latest, unless a specific
	// version was pinned, this is an uncomparable dev build, or --force asks to
	// re-install over an up-to-date binary (repair a corrupt/partial install).
	if upToDate(current, tag, opts) {
		res.UpToDate = true
		return res, nil
	}

	bin, err := downloadBinary(ctx, tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return res, err
	}
	if err := replaceExecutable(inst.Path, bin); err != nil {
		return res, err
	}
	res.Replaced = true
	return res, nil
}

// upToDate decides whether Update should short-circuit as a no-op: the running
// binary is already on (or ahead of) the resolved tag. --force, a pinned
// --version, and an uncomparable dev build all defeat the short-circuit so the
// resolved release is (re-)installed. --force NEVER bypasses the earlier
// CanSelfReplace refusal for package-managed installs (that check runs first).
func upToDate(current, tag string, opts Options) bool {
	if opts.Force || opts.Version != "" {
		return false
	}
	if IsDevBuild(current) {
		return false
	}
	return semver.IsValid(tag) && semver.Compare(tag, current) <= 0
}

// assetName is the release archive for an os/arch (goreleaser strips the leading
// v from the version field).
func assetName(tag, goos, goarch string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", Binary, strings.TrimPrefix(tag, "v"), goos, goarch)
}

// downloadBinary fetches the release archive, verifies its SHA-256 against the
// release checksums.txt, and returns the extracted binary bytes. Assets are
// fetched through the GitHub API (token-honoring; works for private repos).
func downloadBinary(ctx context.Context, tag, goos, goarch string) ([]byte, error) {
	assetFile := assetName(tag, goos, goarch)
	assets, err := releaseAssets(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("list release %s assets: %w", tag, err)
	}
	archiveAsset, ok := assets[assetFile]
	if !ok {
		return nil, fmt.Errorf("release %s has no asset %s (unsupported os/arch?)", tag, assetFile)
	}
	sumsAsset, ok := assets["checksums.txt"]
	if !ok {
		return nil, fmt.Errorf("release %s has no checksums.txt", tag)
	}

	archive, err := downloadAsset(ctx, archiveAsset.URL)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", assetFile, err)
	}
	sums, err := downloadAsset(ctx, sumsAsset.URL)
	if err != nil {
		return nil, fmt.Errorf("download checksums.txt: %w", err)
	}

	want := checksumFor(string(sums), assetFile)
	if want == "" {
		return nil, fmt.Errorf("no checksum entry for %s in this release", assetFile)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(archive))
	if !strings.EqualFold(got, want) {
		return nil, fmt.Errorf("checksum mismatch for %s (expected %s, got %s) — refusing to install", assetFile, want, got)
	}
	return extractBinary(archive, Binary)
}

// checksumFor finds the hash for filename in a `<sha>  <file>` checksums list.
func checksumFor(sums, filename string) string {
	for line := range strings.SplitSeq(strings.TrimSpace(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == filename {
			return fields[0]
		}
	}
	return ""
}

// extractBinary returns the named file's bytes from a .tar.gz archive.
func extractBinary(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("gunzip release: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		if filepath.Base(hdr.Name) == name && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
	return nil, fmt.Errorf("release archive did not contain a %q binary", name)
}

// replaceExecutable atomically replaces target with data: write a temp file in
// the SAME directory (so rename is a same-filesystem move), fsync, chmod 0755,
// and rename over the target. On unix this works while the old image is still
// executing (the inode stays mapped) — never truncate-in-place (spec 14).
func replaceExecutable(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".devstack-update-")
	if err != nil {
		return fmt.Errorf("create temp next to %s: %w", target, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("chmod new binary: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// downloadAsset GETs a GitHub API asset URL with Accept: application/octet-stream
// (which returns the binary, redirecting to a signed URL — Go drops the auth
// header on the cross-host redirect, which is correct). Token-authenticated so it
// works for private repos; size-limited.
func downloadAsset(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	if t := tokenFn(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

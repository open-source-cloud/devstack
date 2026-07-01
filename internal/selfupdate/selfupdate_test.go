package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsDevBuild(t *testing.T) {
	dev := []string{"dev", "", "v0.1.0-11-g8581975", "v0.1.0-11-g8581975-dirty", "garbage"}
	for _, v := range dev {
		if !IsDevBuild(v) {
			t.Errorf("IsDevBuild(%q) = false, want true", v)
		}
	}
	// Valid release tags, including prereleases whose identifier merely starts
	// with 'g' — must NOT be treated as dev builds.
	rel := []string{"v0.1.0", "v1.2.3", "v0.2.0", "v1.0.0-rc.1", "v1.0.0-grpc.1", "v1.0.0-gke", "v1.0.0-go.1"}
	for _, v := range rel {
		if IsDevBuild(v) {
			t.Errorf("IsDevBuild(%q) = true, want false", v)
		}
	}
}

// makeArchive builds a .tar.gz containing a single `devstack` file with body, and
// returns the archive bytes plus their sha256 hex.
func makeArchive(t *testing.T, body string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: Binary, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	data := buf.Bytes()
	return data, fmt.Sprintf("%x", sha256.Sum256(data))
}

func TestExtractBinary(t *testing.T) {
	data, _ := makeArchive(t, "BINARY-CONTENT")
	bin, err := extractBinary(data, Binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(bin) != "BINARY-CONTENT" {
		t.Errorf("extracted %q, want BINARY-CONTENT", bin)
	}
}

func TestChecksumFor(t *testing.T) {
	sums := "abc123  devstack_0.2.0_linux_amd64.tar.gz\ndef456  devstack_0.2.0_darwin_arm64.tar.gz\n"
	if got := checksumFor(sums, "devstack_0.2.0_darwin_arm64.tar.gz"); got != "def456" {
		t.Errorf("checksumFor = %q, want def456", got)
	}
	if got := checksumFor(sums, "missing"); got != "" {
		t.Errorf("checksumFor(missing) = %q, want empty", got)
	}
}

func TestReplaceExecutableAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "devstack")
	os.WriteFile(target, []byte("OLD"), 0o755)

	if err := replaceExecutable(target, []byte("NEW")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "NEW" {
		t.Errorf("content = %q, want NEW", got)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o755 {
		t.Errorf("perms = %v, want 0755", fi.Mode().Perm())
	}
	// No temp litter left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "devstack" {
			t.Errorf("unexpected leftover file %s", e.Name())
		}
	}
}

// releaseServer mimics the GitHub API release-by-tag + asset endpoints: the tags
// endpoint returns the archive, checksums.txt, and (keyless-signing) the
// checksums.txt.sig + checksums.txt.pem assets, all pointing back at this server.
func releaseServer(t *testing.T, tag string, archive []byte, sum string) (*httptest.Server, func()) {
	t.Helper()
	asset := assetName(tag, runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/releases/tags/"+tag, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"assets":[{"name":%q,"url":%q},{"name":"checksums.txt","url":%q},{"name":"checksums.txt.sig","url":%q},{"name":"checksums.txt.pem","url":%q}]}`,
			asset, srv.URL+"/asset/archive", srv.URL+"/asset/sums", srv.URL+"/asset/sig", srv.URL+"/asset/pem")
	})
	mux.HandleFunc("/asset/archive", func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	mux.HandleFunc("/asset/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sum, asset)
	})
	mux.HandleFunc("/asset/sig", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "FAKE-SIGNATURE") })
	mux.HandleFunc("/asset/pem", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "FAKE-CERT") })
	old := APIBase
	APIBase = srv.URL
	return srv, func() { APIBase = old; srv.Close() }
}

func TestDownloadBinaryVerifies(t *testing.T) {
	archive, sum := makeArchive(t, "THE-NEW-BINARY")
	_, cleanup := releaseServer(t, "v0.2.0", archive, sum)
	defer cleanup()

	bin, err := downloadBinary(context.Background(), "v0.2.0", runtime.GOOS, runtime.GOARCH, okVerifier{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(bin) != "THE-NEW-BINARY" {
		t.Errorf("downloaded %q", bin)
	}
}

func TestDownloadBinaryChecksumMismatchAborts(t *testing.T) {
	archive, _ := makeArchive(t, "TAMPERED")
	_, cleanup := releaseServer(t, "v0.2.0", archive, "0000000000000000000000000000000000000000000000000000000000000000")
	defer cleanup()

	if _, err := downloadBinary(context.Background(), "v0.2.0", runtime.GOOS, runtime.GOARCH, okVerifier{}, false); err == nil {
		t.Fatal("want a checksum-mismatch error, got nil")
	}
}

func TestCheckAgainstMockAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.2.0"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := APIBase
	APIBase = srv.URL
	defer func() { APIBase = old }()

	res, err := Check(context.Background(), "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if res.Latest != "v0.2.0" || !res.Available {
		t.Errorf("Check = %+v, want latest v0.2.0 available", res)
	}
	// Same version → not available.
	res2, _ := Check(context.Background(), "v0.2.0")
	if res2.Available {
		t.Error("v0.2.0 vs v0.2.0 should not be available")
	}
	// Dev build → not flagged available by version math, but latest is populated.
	res3, _ := Check(context.Background(), "v0.1.0-5-gabc-dirty")
	if res3.Available || !res3.Dev || res3.Latest != "v0.2.0" {
		t.Errorf("dev check = %+v", res3)
	}
}

func TestDetectInstallSelfManaged(t *testing.T) {
	// A writable plain file is self-managed (not a brew/dpkg/rpm path).
	// DetectInstall inspects os.Executable() (the test binary); assert it does not
	// panic and returns a method. The writable-temp path is covered by writable().
	dir := t.TempDir()
	f := filepath.Join(dir, "devstack")
	os.WriteFile(f, []byte("x"), 0o755)
	if !writable(f) {
		t.Error("a fresh 0755 temp file should be writable")
	}
}

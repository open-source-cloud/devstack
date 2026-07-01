package registry

import "testing"

func TestParseReference(t *testing.T) {
	tests := []struct {
		in       string
		wantReg  string
		wantRepo string
		wantTag  string
		wantDig  string
		wantErr  bool
	}{
		{in: "oci://ghcr.io/acme/templates:1.4.0", wantReg: "ghcr.io", wantRepo: "acme/templates", wantTag: "1.4.0"},
		{in: "ghcr.io/acme/templates:1.4.0", wantReg: "ghcr.io", wantRepo: "acme/templates", wantTag: "1.4.0"},
		{in: "localhost:5000/t:1.0", wantReg: "localhost:5000", wantRepo: "t", wantTag: "1.0"},
		{
			in:      "ghcr.io/acme/postgres@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			wantReg: "ghcr.io", wantRepo: "acme/postgres",
			wantDig: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			in:      "ghcr.io/acme/postgres:1.0@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			wantReg: "ghcr.io", wantRepo: "acme/postgres", wantTag: "1.0",
			wantDig: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		{in: "", wantErr: true},
		{in: "templates:1.4.0", wantErr: true},             // no registry host
		{in: "ghcr.io/acme/templates", wantErr: true},      // no tag or digest
		{in: "ghcr.io/acme/x@sha256:short", wantErr: true}, // bad digest
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			r, err := ParseReference(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error for %q, got %+v", tt.in, r)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.Registry != tt.wantReg || r.Repository != tt.wantRepo || r.Tag != tt.wantTag || r.Digest != tt.wantDig {
				t.Errorf("ParseReference(%q) = %+v, want reg=%s repo=%s tag=%s dig=%s",
					tt.in, r, tt.wantReg, tt.wantRepo, tt.wantTag, tt.wantDig)
			}
		})
	}
}

func TestFetchRefPrefersDigest(t *testing.T) {
	r := Reference{Registry: "ghcr.io", Repository: "a/b", Tag: "1.0", Digest: "sha256:deadbeef"}
	if got := r.FetchRef(); got != "ghcr.io/a/b@sha256:deadbeef" {
		t.Errorf("FetchRef = %q, want the digest ref", got)
	}
	r.Digest = ""
	if got := r.FetchRef(); got != "ghcr.io/a/b:1.0" {
		t.Errorf("FetchRef = %q, want the tag ref when unpinned", got)
	}
}

func TestIsFloatingTag(t *testing.T) {
	if !(Reference{Tag: "latest"}).IsFloatingTag() {
		t.Error(":latest must be flagged as floating")
	}
	if (Reference{Tag: "1.4.0"}).IsFloatingTag() {
		t.Error("a pinned version tag is not floating")
	}
}

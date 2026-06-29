package git

import "testing"

func TestExpandURL(t *testing.T) {
	cases := map[string]string{
		"github:acme/api":              "git@github.com:acme/api.git",
		"gitlab:acme/web":              "git@gitlab.com:acme/web.git",
		"bitbucket:acme/x":             "git@bitbucket.org:acme/x.git",
		"github:acme/api.git":          "git@github.com:acme/api.git", // no double .git
		"repo:git@host:custom/x.git":   "git@host:custom/x.git",       // explicit passthrough
		"git@github.com:acme/api.git":  "git@github.com:acme/api.git", // scp-style passthrough
		"https://github.com/acme/api":  "https://github.com/acme/api", // url passthrough
		"ssh://git@host:22/acme/x.git": "ssh://git@host:22/acme/x.git",
		"/local/path/repo":             "/local/path/repo", // local path
		"./relative":                   "./relative",
	}
	for in, want := range cases {
		if got := ExpandURL(in); got != want {
			t.Errorf("ExpandURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandURLTrimsRepoPassthrough(t *testing.T) {
	if got := ExpandURL("repo: git@host:acme/x.git"); got != "git@host:acme/x.git" {
		t.Errorf("repo: passthrough leaked whitespace: %q", got)
	}
	if got := ExpandURL("github: acme/api"); got != "git@github.com:acme/api.git" {
		t.Errorf("shorthand leaked whitespace: %q", got)
	}
}

func TestSameRemote(t *testing.T) {
	same := [][2]string{
		{"git@github.com:acme/api.git", "https://github.com/acme/api"},
		{"git@github.com:acme/api.git", "https://github.com/acme/api.git"},
		{"github:acme/api", "https://github.com/acme/api"},
		{"ssh://git@github.com:22/acme/api.git", "git@github.com:acme/api.git"},
	}
	for _, p := range same {
		if !SameRemote(p[0], p[1]) {
			t.Errorf("SameRemote(%q, %q) = false, want true", p[0], p[1])
		}
	}
	diff := [][2]string{
		{"git@github.com:acme/api.git", "git@github.com:acme/web.git"},
		{"git@github.com:acme/api.git", "git@gitlab.com:acme/api.git"},
	}
	for _, p := range diff {
		if SameRemote(p[0], p[1]) {
			t.Errorf("SameRemote(%q, %q) = true, want false", p[0], p[1])
		}
	}
}

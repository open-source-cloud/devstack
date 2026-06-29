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

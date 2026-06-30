package scaffold_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/scaffold"
)

func TestSanitizeName(t *testing.T) {
	dsNameRE := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	cases := []struct{ in, want string }{
		{"My.App", "my-app"},
		{"demo", "demo"},
		{"123abc", "abc"},
		{"Foo Bar!", "foo-bar"},
		{"___", "workspace"},
		{"", "workspace"},
		{"a..b", "a-b"},
		{strings.Repeat("a", 80), strings.Repeat("a", 63)},
	}
	for _, tc := range cases {
		got := scaffold.SanitizeName(tc.in)
		if got != tc.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if !dsNameRE.MatchString(got) {
			t.Errorf("SanitizeName(%q) = %q does not satisfy dsNameRE", tc.in, got)
		}
	}
}

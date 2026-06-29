package template

import (
	"strings"
	"testing"
)

// TestDelimitersDoNotCollide is spec 02 acceptance #2: a Dockerfile carrying
// literal ${XDEBUG_HOST}, $TAG and ${VAR:-""} renders without the engine
// mangling them, while the [[ ]] action is substituted.
func TestDelimitersDoNotCollide(t *testing.T) {
	src := []byte("ARG V=[[ .params.phpVersion ]]\n" +
		"ENV XDEBUG_HOST=${XDEBUG_HOST}\n" +
		"ENV APP_TAG=$TAG\n" +
		"ENV FALLBACK=${VAR:-\"\"}\n")
	out, err := RenderText("Dockerfile", src, map[string]any{"params": map[string]any{"phpVersion": "8.3"}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, lit := range []string{"${XDEBUG_HOST}", "$TAG", `${VAR:-""}`} {
		if !strings.Contains(got, lit) {
			t.Errorf("literal %q was mangled; output:\n%s", lit, got)
		}
	}
	if !strings.Contains(got, "ARG V=8.3") {
		t.Errorf("action not rendered; output:\n%s", got)
	}
}

// TestMissingParamFailsFast is spec 02 acceptance #5: referencing an undeclared
// param errors instead of rendering an empty string.
func TestMissingParamFailsFast(t *testing.T) {
	_, err := RenderText("x", []byte("v=[[ .params.nope ]]"), map[string]any{"params": map[string]any{}})
	if err == nil {
		t.Fatal("want an error for a missing param, got nil")
	}
}

func TestRenderYAMLDecodes(t *testing.T) {
	m, err := RenderYAML("frag", []byte("image: \"redis:[[ .params.v ]]\"\nrestart: always\n"),
		map[string]any{"params": map[string]any{"v": "7"}})
	if err != nil {
		t.Fatal(err)
	}
	if m["image"] != "redis:7" {
		t.Errorf("image = %v, want redis:7", m["image"])
	}
	if m["restart"] != "always" {
		t.Errorf("restart = %v", m["restart"])
	}
}

func TestRenderYAMLEmpty(t *testing.T) {
	m, err := RenderYAML("frag", []byte("   \n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || len(m) != 0 {
		t.Errorf("want empty non-nil map, got %v", m)
	}
}

func TestFuncs(t *testing.T) {
	cases := map[string]string{
		`[[ default "x" "" ]]`:                                "x",
		`[[ default "x" "y" ]]`:                               "y",
		`[[ upper "ab" ]]`:                                    "AB",
		`[[ lower "AB" ]]`:                                    "ab",
		`[[ "16" | atoi | printf "%d" ]]`:                     "16",
		`[[ atoi "9.6" | printf "%d" ]]`:                      "9",
		`[[ trim "  z  " ]]`:                                  "z",
		`[[ replace "a" "b" "aa" ]]`:                          "bb",
		`[[ if lt (atoi "16") 18 ]]old[[ else ]]new[[ end ]]`: "old",
		`[[ if lt (atoi "18") 18 ]]old[[ else ]]new[[ end ]]`: "new",
	}
	for in, want := range cases {
		out, err := RenderText("t", []byte(in), nil)
		if err != nil {
			t.Errorf("RenderText(%q): %v", in, err)
			continue
		}
		if string(out) != want {
			t.Errorf("RenderText(%q) = %q, want %q", in, out, want)
		}
	}
}

package selfupdate

import "testing"

func TestUpToDateForceSemantics(t *testing.T) {
	cases := []struct {
		name    string
		current string
		tag     string
		opts    Options
		want    bool
	}{
		{"same version → up to date", "v0.2.0", "v0.2.0", Options{}, true},
		{"older → not up to date", "v0.1.0", "v0.2.0", Options{}, false},
		{"ahead → up to date", "v0.3.0", "v0.2.0", Options{}, true},
		{"force over same → re-install", "v0.2.0", "v0.2.0", Options{Force: true}, false},
		{"force over ahead → re-install", "v0.3.0", "v0.2.0", Options{Force: true}, false},
		{"pinned version defeats short-circuit", "v0.2.0", "v0.2.0", Options{Version: "v0.2.0"}, false},
		{"dev build never up to date", "v0.1.0-5-gabc-dirty", "v0.2.0", Options{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := upToDate(c.current, c.tag, c.opts); got != c.want {
				t.Errorf("upToDate(%q,%q,%+v) = %v, want %v", c.current, c.tag, c.opts, got, c.want)
			}
		})
	}
}

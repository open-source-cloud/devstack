package docker

import "testing"

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		wantMaj int
		wantMin int
		wantErr bool
	}{
		{"git version 2.43.0", 2, 43, false},
		{"Docker Compose version v2.29.1", 2, 29, false},
		{"v2.20", 2, 20, false},
		{"2.20.0\n", 2, 20, false},
		{"nonsense", 0, 0, true},
	}
	for _, tt := range tests {
		got, err := parseVersion(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseVersion(%q) = nil err, want error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseVersion(%q) err = %v", tt.in, err)
			continue
		}
		if got.Major != tt.wantMaj || got.Minor != tt.wantMin {
			t.Errorf("parseVersion(%q) = %d.%d, want %d.%d", tt.in, got.Major, got.Minor, tt.wantMaj, tt.wantMin)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		v, min Version
		want   bool
	}{
		{Version{2, 20}, Version{2, 20}, true},
		{Version{2, 29}, Version{2, 20}, true},
		{Version{2, 19}, Version{2, 20}, false},
		{Version{3, 0}, Version{2, 20}, true},
		{Version{1, 99}, Version{2, 20}, false},
	}
	for _, c := range cases {
		if got := c.v.AtLeast(c.min); got != c.want {
			t.Errorf("%v.AtLeast(%v) = %v, want %v", c.v, c.min, got, c.want)
		}
	}
}

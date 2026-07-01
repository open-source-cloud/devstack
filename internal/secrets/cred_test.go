package secrets

import "testing"

func TestRandomPasswordLengthAndUniqueness(t *testing.T) {
	for _, n := range []int{1, 8, 16, 32, 64} {
		p, err := RandomPassword(n)
		if err != nil {
			t.Fatalf("RandomPassword(%d): %v", n, err)
		}
		if len(p) != n {
			t.Errorf("RandomPassword(%d) len = %d, want %d (%q)", n, len(p), n, p)
		}
	}
	// Alphanumeric alphabet only (no shell/DSN-hostile characters, not even -/_).
	p, _ := RandomPassword(128)
	for _, r := range p {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			t.Fatalf("RandomPassword produced non-alphanumeric rune %q in %q", r, p)
		}
	}
	// Two draws must differ (astronomically likely).
	a, _ := RandomPassword(32)
	b, _ := RandomPassword(32)
	if a == b {
		t.Errorf("two RandomPassword(32) draws were identical: %q", a)
	}
}

func TestRandomPasswordRejectsNonPositive(t *testing.T) {
	if _, err := RandomPassword(0); err == nil {
		t.Error("RandomPassword(0) should error")
	}
	if _, err := RandomPassword(-5); err == nil {
		t.Error("RandomPassword(-5) should error")
	}
}

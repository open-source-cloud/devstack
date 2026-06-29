package generate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGolden is the spec 02 golden-file conformance suite: the full fixture
// workspace is generated and every stack's canonical compose document is compared
// byte-for-byte against a committed golden. Re-materialize after an intentional
// change with:  UPDATE_GOLDEN=1 go test ./internal/generate -run TestGolden
func TestGolden(t *testing.T) {
	g, _ := newGen(t)
	stacks, err := g.GenerateAll()
	if err != nil {
		t.Fatal(err)
	}
	update := os.Getenv("UPDATE_GOLDEN") == "1"
	for _, st := range stacks {
		golden := filepath.Join("testdata", "golden", st.Name+".docker-compose.yaml")
		if update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, st.Compose, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("missing golden %s (run UPDATE_GOLDEN=1): %v", golden, err)
		}
		if string(want) != string(st.Compose) {
			t.Errorf("%s: generated compose does not match %s\n--- got ---\n%s", st.Name, golden, st.Compose)
		}
	}
}

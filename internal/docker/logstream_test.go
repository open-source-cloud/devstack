package docker

import (
	"context"
	"testing"
)

func TestSplitTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantTS   string
		wantText string
	}{
		{"rfc3339", "2026-06-14T12:03:01.412Z GET /healthz 200", "2026-06-14T12:03:01.412Z", "GET /healthz 200"},
		{"no-timestamp", "plain log line", "", "plain log line"},
		{"first-token-no-T", "hello world here", "", "hello world here"},
		{"empty", "", "", ""},
		{"only-ts", "2026-06-14T12:03:01Z", "", "2026-06-14T12:03:01Z"}, // no space → whole line is text
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, text := splitTimestamp(tt.line)
			if ts != tt.wantTS || text != tt.wantText {
				t.Fatalf("splitTimestamp(%q) = (%q, %q), want (%q, %q)", tt.line, ts, text, tt.wantTS, tt.wantText)
			}
		})
	}
}

// TestLineEmitter verifies the writer splits input into lines, tags the stream,
// buffers partial lines across writes, and flushes the final unterminated line.
func TestLineEmitter(t *testing.T) {
	out := make(chan LogLine, 8)
	e := &lineEmitter{ctx: context.Background(), out: out, stream: "stderr"}

	// Two writes; the second completes a line split across the boundary.
	_, _ = e.Write([]byte("first\nseco"))
	_, _ = e.Write([]byte("nd\nthird-no-newline"))
	e.flush()

	var got []LogLine
	close(out)
	for ll := range out {
		got = append(got, ll)
	}
	want := []string{"first", "second", "third-no-newline"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Text != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i].Text, want[i])
		}
		if got[i].Stream != "stderr" {
			t.Errorf("line %d stream = %q, want stderr", i, got[i].Stream)
		}
	}
}

// TestLineEmitterCtxCancel ensures a cancelled context stops the emitter instead
// of blocking on a full channel (follow-mode teardown).
func TestLineEmitterCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan LogLine) // unbuffered: send would block without ctx guard
	e := &lineEmitter{ctx: ctx, out: out, stream: "stdout"}
	if ok := e.send("blocked"); ok {
		t.Fatal("send returned true on a cancelled context")
	}
}

// TestMockContainerLogStream verifies the mock emits seeded lines then closes.
func TestMockContainerLogStream(t *testing.T) {
	m := &MockClient{Streams: map[string][]LogLine{
		"c1": {{Stream: "stdout", Text: "one"}, {Stream: "stderr", Text: "two"}},
	}}
	ch, err := m.ContainerLogStream(context.Background(), "c1", LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got []LogLine
	for ll := range ch {
		got = append(got, ll)
	}
	if len(got) != 2 || got[0].Text != "one" || got[1].Text != "two" {
		t.Fatalf("unexpected stream: %+v", got)
	}
}

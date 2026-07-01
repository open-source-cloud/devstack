package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/moby/moby/api/pkg/stdcopy"
	moby "github.com/moby/moby/client"
)

// logChanBuffer bounds the per-container fan-in channel so a chatty container
// applies natural backpressure (a slow consumer blocks the demux goroutine)
// rather than growing an unbounded queue (spec 16 §backpressure).
const logChanBuffer = 256

// ContainerLogStream opens a demuxed, optionally-following log stream for one
// container. It first inspects the container to learn whether it has a TTY (a
// TTY stream is NOT stdcopy-framed and must be read raw — reading a non-TTY
// stream raw corrupts it with the 8-byte frame header, spec 16 §demux).
func (m *mobyClient) ContainerLogStream(ctx context.Context, id string, opts LogOptions) (<-chan LogLine, error) {
	// Learn the TTY-ness up front; a missing/erroring inspect defaults to the
	// safe non-TTY (demuxed) path.
	tty := false
	if det, err := m.ContainerInspect(ctx, id); err == nil {
		tty = det.TTY
	}

	lopts := moby.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     opts.Follow,
		Since:      opts.Since,
		Timestamps: opts.Timestamps,
	}
	if opts.Tail > 0 {
		lopts.Tail = strconv.Itoa(opts.Tail)
	}
	rc, err := m.cli.ContainerLogs(ctx, id, lopts)
	if err != nil {
		// The Engine returns a distinct error for services on a logging driver
		// that can't be read (syslog/none); surface a one-liner (spec 16 gotcha).
		if strings.Contains(strings.ToLower(err.Error()), "logging driver") {
			return nil, fmt.Errorf("logs for %q unavailable: its logging driver does not support reading (use json-file/local): %w", id, err)
		}
		return nil, fmt.Errorf("logs for container %q: %w", id, err)
	}

	out := make(chan LogLine, logChanBuffer)
	go func() {
		defer close(out)
		defer func() { _ = rc.Close() }()
		if tty {
			// Raw single stream: everything is stdout as far as the TTY is concerned.
			e := &lineEmitter{ctx: ctx, out: out, stream: "stdout", ts: opts.Timestamps}
			_, _ = io.Copy(e, rc)
			e.flush()
			return
		}
		outE := &lineEmitter{ctx: ctx, out: out, stream: "stdout", ts: opts.Timestamps}
		errE := &lineEmitter{ctx: ctx, out: out, stream: "stderr", ts: opts.Timestamps}
		_, _ = stdcopy.StdCopy(outE, errE, rc)
		outE.flush()
		errE.flush()
	}()
	return out, nil
}

// lineEmitter is an io.Writer that splits its input into lines and forwards each
// as a LogLine on out, parsing the Engine's leading RFC3339 timestamp when ts is
// set. Partial lines are buffered across Writes; flush emits any final unterminated
// line at EOF.
type lineEmitter struct {
	ctx    context.Context
	out    chan<- LogLine
	stream string
	ts     bool
	buf    []byte
}

func (e *lineEmitter) Write(p []byte) (int, error) {
	e.buf = append(e.buf, p...)
	for {
		i := bytes.IndexByte(e.buf, '\n')
		if i < 0 {
			break
		}
		line := string(e.buf[:i])
		e.buf = e.buf[i+1:]
		if !e.send(line) {
			return len(p), context.Canceled
		}
	}
	return len(p), nil
}

func (e *lineEmitter) flush() {
	if len(e.buf) == 0 {
		return
	}
	line := string(e.buf)
	e.buf = nil
	e.send(line)
}

// send parses the optional timestamp prefix and delivers the line, honoring ctx
// cancellation so a torn-down stream never blocks forever. Returns false if ctx
// is done.
func (e *lineEmitter) send(line string) bool {
	line = strings.TrimRight(line, "\r")
	ll := LogLine{Stream: e.stream, Text: line}
	if e.ts {
		ll.TS, ll.Text = splitTimestamp(line)
	}
	select {
	case e.out <- ll:
		return true
	case <-e.ctx.Done():
		return false
	}
}

// splitTimestamp separates the Engine's leading RFC3339Nano timestamp (added
// when Timestamps=true) from the log text. A line without a leading timestamp is
// returned unchanged with an empty TS.
func splitTimestamp(line string) (ts, text string) {
	sp := strings.IndexByte(line, ' ')
	if sp <= 0 {
		return "", line
	}
	head := line[:sp]
	// A valid Engine timestamp contains a 'T' and is date-shaped; a cheap guard
	// avoids mis-splitting an untimestamped line whose first token has no 'T'.
	if !strings.ContainsRune(head, 'T') {
		return "", line
	}
	return head, line[sp+1:]
}

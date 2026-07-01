package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
)

// newLogsCmd wires `devstack logs [service...]` (spec 16) — the non-interactive
// sibling of `dashboard`. It multiplexes container logs across the workspace's
// project + shared stacks, streaming from the read-only Engine SDK filtered on
// devstack's own label (+ an optional per-service filter). A TTY gets a
// color-keyed `service │` gutter; a pipe gets plain prefixed lines; `--json`
// emits one structured object per line. Non-follow mode exits at EOF.
func newLogsCmd(g *GlobalOpts) *cobra.Command {
	var (
		follow     bool
		tail       int
		since      string
		timestamps bool
		noColor    bool
	)
	cmd := &cobra.Command{
		Use:   "logs [service...]",
		Short: "Stream service logs across the workspace's project + shared stacks",
		Long: "logs multiplexes container logs across every project + shared service in\n" +
			"this workspace (or the named services), color-keyed by service. It streams\n" +
			"from the read-only Engine SDK; with no daemon it reports nothing to stream.\n" +
			"`--json` emits one {ts,service,project,stream,container,line} object per line.",
		ValidArgsFunction: logsServiceCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, closeFn, err := newLogsClient(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			targets, err := resolveLogTargets(cmd.Context(), client, args)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				return logsNoTargets(cmd, g, args)
			}

			// Follow mode runs until interrupted; wire a signal-aware context so
			// Ctrl-C tears down every SDK stream cleanly (the root ctx is not
			// signal-aware). Non-follow returns at EOF on its own.
			ctx := cmd.Context()
			if follow {
				var stop context.CancelFunc
				ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
				defer stop()
			}

			opts := docker.LogOptions{Follow: follow, Tail: tail, Since: since, Timestamps: timestamps}
			w := &logRenderer{
				out:   cmd.OutOrStdout(),
				json:  g.JSON,
				color: logsUseColor(cmd, g, noColor),
			}
			return streamLogs(ctx, client, targets, opts, w)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&follow, "follow", "f", false, "follow log output (stream until interrupted)")
	f.IntVar(&tail, "tail", 200, "number of trailing lines to show per service (<=0 = all)")
	f.StringVar(&since, "since", "", "show logs since a duration (e.g. 10m) or timestamp")
	f.BoolVar(&timestamps, "timestamps", false, "prefix each line with the container timestamp")
	f.BoolVar(&noColor, "no-color", false, "disable the color-keyed gutter")
	return cmd
}

// logTarget is one resolved container to stream, with the metadata each line is
// tagged with.
type logTarget struct {
	ID        string
	Container string // container name
	Project   string // "" for shared services
	Service   string // display + filter name: bare service, or shared-<engine>
}

// resolveLogTargets enumerates the workspace's managed containers (tool-owned
// label, All=true, one-offs excluded — the same rules that keep ref-counting
// honest) and narrows them to the requested services. A shared service is named
// by its DNS alias (shared-<engine>); a project service by its service label.
// An empty filter selects every managed container. Sorted for deterministic
// ordering.
func resolveLogTargets(ctx context.Context, client docker.Client, services []string) ([]logTarget, error) {
	cs, err := client.ListManaged(ctx, map[string]string{generate.LabelManaged: "true"})
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, s := range services {
		want[s] = true
	}
	var out []logTarget
	for _, c := range cs {
		t := logTarget{ID: c.ID, Container: c.Name, Project: c.Labels[generate.LabelProject]}
		switch {
		case c.Labels[generate.LabelShared] != "":
			t.Service = generate.SharedAlias(c.Labels[generate.LabelShared])
		case c.Labels[generate.LabelService] != "":
			t.Service = c.Labels[generate.LabelService]
		default:
			t.Service = c.Name
		}
		if len(want) > 0 && !logTargetWanted(t, c, want) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Container < out[j].Container
	})
	return out, nil
}

// logTargetWanted reports whether a target matches the requested filter set. A
// user may name the display service (shared-postgres / api), the bare shared
// engine (postgres), or the owning project.
func logTargetWanted(t logTarget, c docker.Container, want map[string]bool) bool {
	if want[t.Service] {
		return true
	}
	if s := c.Labels[generate.LabelShared]; s != "" && want[s] {
		return true
	}
	if t.Project != "" && want[t.Project] {
		return true
	}
	return false
}

// logJSONLine is the scriptable per-line contract (spec 16): one object per line.
type logJSONLine struct {
	TS        string `json:"ts,omitempty"`
	Service   string `json:"service"`
	Project   string `json:"project,omitempty"`
	Stream    string `json:"stream"`
	Container string `json:"container"`
	Line      string `json:"line"`
}

// logRenderer writes tagged log lines in one of the two output contracts.
type logRenderer struct {
	out   io.Writer
	json  bool
	color bool
	mu    sync.Mutex // serialize concurrent stream writers onto one stdout
	enc   *json.Encoder
}

// write renders one line from one target. It is safe for concurrent callers.
func (r *logRenderer) write(t logTarget, ll docker.LogLine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.json {
		if r.enc == nil {
			r.enc = json.NewEncoder(r.out)
		}
		_ = r.enc.Encode(logJSONLine{
			TS: ll.TS, Service: t.Service, Project: t.Project,
			Stream: ll.Stream, Container: t.Container, Line: ll.Text,
		})
		return
	}
	gutter := t.Service
	if r.color {
		gutter = logColor(t.Service).Render(gutter)
	}
	ts := ""
	if ll.TS != "" {
		ts = ll.TS + " "
	}
	fmt.Fprintf(r.out, "%s │ %s%s\n", gutter, ts, ll.Text)
}

// streamLogs opens a stream per target, fans them into the renderer, and returns
// when every stream ends (non-follow EOF) or ctx is cancelled (follow Ctrl-C).
func streamLogs(ctx context.Context, client docker.Client, targets []logTarget, opts docker.LogOptions, r *logRenderer) error {
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for _, t := range targets {
		ch, err := client.ContainerLogStream(ctx, t.ID, opts)
		if err != nil {
			// A single unreadable service (e.g. `none` logging driver) is a
			// one-line notice, not a fatal — the rest still stream (spec 16).
			fmt.Fprintf(r.out, "%s: %v\n", t.Service, err)
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			errMu.Unlock()
			continue
		}
		wg.Add(1)
		go func(t logTarget, ch <-chan docker.LogLine) {
			defer wg.Done()
			for ll := range ch {
				r.write(t, ll)
			}
		}(t, ch)
	}
	wg.Wait()
	// In follow mode a clean Ctrl-C teardown is success, not an error.
	if ctx.Err() != nil {
		return nil
	}
	_ = firstErr // non-fatal; surfaced inline above
	return nil
}

// logsUseColor decides whether to emit the color-keyed gutter: never for --json
// or --quiet or --no-color or NO_COLOR/$TERM=dumb, and only when stdout is a TTY
// (a piped stdout gets plain lines even if stderr is a terminal — spec 16).
func logsUseColor(cmd *cobra.Command, g *GlobalOpts, noColor bool) bool {
	if g.JSON || g.Quiet || noColor {
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if f, ok := cmd.OutOrStdout().(*os.File); ok {
		return isTerminal(f)
	}
	return false
}

var logPalette = []string{"39", "208", "76", "170", "220", "51", "199", "141", "45", "214", "84", "205"}

// logColor maps a service name to a stable color from the palette (hash → index).
func logColor(name string) lipgloss.Style {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	c := logPalette[int(h.Sum32())%len(logPalette)]
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true)
}

// logsNoTargets prints the right zero-state for the plain and --json contracts.
// --json stays silent (an empty stream is zero objects) to keep the line contract
// clean for consumers.
func logsNoTargets(cmd *cobra.Command, g *GlobalOpts, services []string) error {
	if g.JSON || g.Quiet {
		return nil
	}
	w := cmd.OutOrStdout()
	if len(services) > 0 {
		fmt.Fprintf(w, "no running services match %v (try `devstack status`)\n", services)
		return nil
	}
	fmt.Fprintln(w, "no managed services are running in this workspace (run `devstack up`)")
	return nil
}

// logsServiceCompletion completes service names from live managed containers.
func logsServiceCompletion(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	client, closeFn, err := newLogsClient(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	defer closeFn()
	targets, err := resolveLogTargets(cmd.Context(), client, nil)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	set := map[string]bool{}
	for _, t := range targets {
		set[t.Service] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// newLogsClient builds the read-only Engine client for log streaming. An
// unreachable daemon (construction OR ping failure) degrades to an empty mock (no
// targets) rather than an error, so `logs` in a dead-daemon workspace prints a
// clean zero-state instead of a stack trace.
func newLogsClient(cmd *cobra.Command) (docker.Client, func(), error) {
	c, err := docker.NewClient(cmd.Context())
	if err != nil {
		return &docker.MockClient{}, func() {}, nil
	}
	if err := c.Ping(cmd.Context()); err != nil {
		_ = c.Close()
		return &docker.MockClient{}, func() {}, nil
	}
	return c, func() { _ = c.Close() }, nil
}

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/task"
)

// newRunCmd wires `run <task...>` (spec 31): execute a project's task graph. Tasks
// are non-container commands with `deps` edges; they run in dependency order,
// independent tasks in parallel (bounded by --parallel). `run: host` runs on the
// host; `run: exec` runs inside a service container via compose exec. Streams each
// task's output live, prefixed by task name. `devstack run` takes no flock — it
// mutates no shared/ledger state.
func newRunCmd(g *GlobalOpts) *cobra.Command {
	var project string
	var parallel int
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "run <task> [task2 ...]",
		Short: "Run a project's task graph (deps-ordered, parallel; host or in-container)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			proj := project
			if proj == "" {
				proj = resolveActiveProject(mgr.Model, mgr.DB)
			}
			if proj == "" {
				return fmt.Errorf("no project selected (pass --project)")
			}
			p, ok := mgr.Model.Projects[proj]
			if !ok {
				return fmt.Errorf("unknown project %q", proj)
			}
			if len(p.Tasks) == 0 {
				return fmt.Errorf("project %q declares no tasks: (add a tasks: block to devstack.yaml)", proj)
			}
			layers, err := task.Plan(p.Tasks, args)
			if err != nil {
				return err
			}
			if dryRun {
				return renderRunPlan(cmd, g, proj, layers)
			}
			if parallel <= 0 {
				parallel = min(8, 2*runtime.NumCPU())
			}
			projDir := mgr.Model.ProjectDir(proj)
			composeFile := filepath.Join(projDir, generate.GenDir, generate.ComposeFile)
			r := &taskExec{out: cmd.OutOrStdout(), projDir: projDir, composeFile: composeFile}
			return runLayers(cmd.Context(), r, p.Tasks, layers, parallel)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "target project (default: the active/first project)")
	cmd.Flags().IntVar(&parallel, "parallel", 0, "max concurrent tasks (default min(8, 2*CPUs))")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the resolved task DAG and exit")
	return cmd
}

func renderRunPlan(cmd *cobra.Command, g *GlobalOpts, project string, layers [][]string) error {
	if g.JSON {
		return writeJSON(cmd, map[string]any{"project": project, "layers": layers})
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "run plan for %q (%d layer(s)):\n", project, len(layers))
	for i, l := range layers {
		fmt.Fprintf(w, "  %d: %s\n", i+1, strings.Join(l, ", "))
	}
	return nil
}

// runLayers executes each layer in order; tasks within a layer run concurrently
// up to `parallel`. A failing task fails the run (its layer's siblings finish).
func runLayers(ctx context.Context, r *taskExec, tasks map[string]config.Task, layers [][]string, parallel int) error {
	for _, layer := range layers {
		eg, ectx := errgroup.WithContext(ctx)
		eg.SetLimit(parallel)
		for _, name := range layer {
			name := name
			t := tasks[name]
			eg.Go(func() error {
				if err := r.run(ectx, name, t); err != nil {
					return fmt.Errorf("task %q: %w", name, err)
				}
				return nil
			})
		}
		if err := eg.Wait(); err != nil {
			return err
		}
	}
	return nil
}

// taskExec runs a single task with live, name-prefixed output. Concurrent tasks
// share one output writer, so emit serializes whole (prefix+line) writes under a
// mutex to keep lines intact and race-free.
type taskExec struct {
	mu          sync.Mutex
	out         io.Writer
	projDir     string
	composeFile string
}

// emit writes prefix+data as one atomic unit under the lock.
func (r *taskExec) emit(prefix string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = io.WriteString(r.out, prefix)
	_, _ = r.out.Write(data)
}

func (r *taskExec) run(ctx context.Context, name string, t config.Task) error {
	prefix := name + " | "
	pw := &prefixWriter{emit: r.emit, prefix: prefix}
	env := append(os.Environ(), envKV(t.Env)...)

	var c *exec.Cmd
	if t.Run == "exec" {
		if t.Service == "" {
			return fmt.Errorf("run: exec requires a service")
		}
		args := []string{"compose", "-f", r.composeFile, "exec", "-T"}
		if t.Workdir != "" {
			args = append(args, "-w", t.Workdir)
		}
		for _, kv := range envKV(t.Env) {
			args = append(args, "-e", kv)
		}
		args = append(args, t.Service)
		args = append(args, t.Command...)
		c = exec.CommandContext(ctx, "docker", args...)
		c.Dir = r.projDir
		c.Env = env
	} else {
		c = exec.CommandContext(ctx, t.Command[0], t.Command[1:]...)
		c.Dir = taskWorkdir(r.projDir, t.Workdir)
		c.Env = env
	}
	c.Stdout = pw
	c.Stderr = pw
	r.emit(prefix, []byte("→ "+strings.Join(t.Command, " ")+"\n"))
	return c.Run()
}

func taskWorkdir(projDir, workdir string) string {
	if workdir == "" {
		return projDir
	}
	if filepath.IsAbs(workdir) {
		return workdir
	}
	return filepath.Join(projDir, workdir)
}

func envKV(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// prefixWriter buffers bytes and flushes each complete line through emit, so a
// line and its prefix are written atomically even under concurrent tasks.
type prefixWriter struct {
	emit   func(prefix string, data []byte)
	prefix string
	buf    []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		line := make([]byte, i+1)
		copy(line, p.buf[:i+1])
		p.emit(p.prefix, line)
		p.buf = p.buf[i+1:]
	}
	return len(b), nil
}

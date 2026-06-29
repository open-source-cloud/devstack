package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/git"
)

// repo is one workspace repository to manage.
type repo struct {
	name string
	dir  string
	url  string // expanded clone URL ("" if no git declared)
}

// newWsCmd wires `ws clone|sync|status|git` — multi-repo git over the workspace
// using the developer's existing git auth, with bounded-parallel execution and a
// plain/JSON dual-mode contract (spec 06).
func newWsCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ws",
		Short: "Multi-repo workspace git operations",
	}
	cmd.AddCommand(newWsStatusCmd(g), newWsCloneCmd(g), newWsSyncCmd(g), newWsGitCmd(g))
	return cmd
}

// loadRepos loads the workspace and returns its repositories, optionally filtered
// to the named subset (empty/`all` = every repo).
func loadRepos(names []string) ([]repo, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// Workspace-only parse: the project devstack.yaml files may not exist yet
	// (ws clone runs before repos are on disk).
	root, ws, err := config.LoadWorkspaceOnly(cwd)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range names {
		if n != "all" {
			want[n] = true
		}
	}
	var repos []repo
	for _, ref := range ws.Projects {
		if len(want) > 0 && !want[ref.Name] {
			continue
		}
		repos = append(repos, repo{
			name: ref.Name,
			dir:  filepath.Join(root, ref.Path),
			url:  git.ExpandURL(ref.Git),
		})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].name < repos[j].name })
	return repos, nil
}

// defaultJobs is min(8, 2*GOMAXPROCS) per spec 06.
func defaultJobs() int {
	return max(1, min(8, 2*runtime.GOMAXPROCS(0)))
}

// runPerRepo runs fn for every repo with bounded parallelism, collecting a
// per-repo error (a failure never cancels the batch — each repo fails fast on its
// own, spec 06 acceptance #6). Returns results in the input order.
func runPerRepo(ctx context.Context, repos []repo, jobs int, fn func(context.Context, repo) error) []repoResult {
	results := make([]repoResult, len(repos))
	var eg errgroup.Group
	eg.SetLimit(jobs)
	for i, r := range repos {
		eg.Go(func() error {
			results[i] = repoResult{Name: r.name, Err: fn(ctx, r)}
			return nil
		})
	}
	_ = eg.Wait()
	return results
}

type repoResult struct {
	Name string
	Err  error
}

// --- ws status -------------------------------------------------------------

func newWsStatusCmd(g *GlobalOpts) *cobra.Command {
	var (
		check bool
		jobs  int
	)
	cmd := &cobra.Command{
		Use:   "status [names...]",
		Short: "Cross-repo git status table (branch, ahead/behind, dirty)",
		RunE: func(cmd *cobra.Command, args []string) error {
			repos, err := loadRepos(args)
			if err != nil {
				return err
			}
			gx, err := git.New()
			if err != nil {
				return err
			}
			if jobs <= 0 {
				jobs = defaultJobs()
			}

			type row struct {
				Name    string      `json:"name"`
				Present bool        `json:"present"`
				Status  *git.Status `json:"status,omitempty"`
				Error   string      `json:"error,omitempty"`
			}
			rows := make([]row, len(repos))
			var eg errgroup.Group
			eg.SetLimit(jobs)
			for i, r := range repos {
				eg.Go(func() error {
					rr := row{Name: r.name}
					if gx.IsRepo(cmd.Context(), r.dir) {
						rr.Present = true
						if st, err := gx.Status(cmd.Context(), r.dir); err != nil {
							rr.Error = err.Error()
						} else {
							rr.Status = st
						}
					}
					rows[i] = rr // distinct index per goroutine — no mutex needed
					return nil
				})
			}
			_ = eg.Wait()

			anyDirty := false
			for _, r := range rows {
				if r.Status != nil && r.Status.Dirty() {
					anyDirty = true
				}
			}

			if g.JSON {
				if err := writeJSON(cmd, map[string]any{"repos": rows}); err != nil {
					return err
				}
			} else {
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
				fmt.Fprintln(w, "REPO\tBRANCH\tAHEAD/BEHIND\tSTATE")
				for _, r := range rows {
					switch {
					case !r.Present:
						fmt.Fprintf(w, "%s\t-\t-\tabsent (run `ws clone`)\n", r.Name)
					case r.Error != "":
						fmt.Fprintf(w, "%s\t-\t-\terror\n", r.Name)
					default:
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, branchLabel(r.Status), abLabel(r.Status), stateLabel(r.Status))
					}
				}
				w.Flush()
			}
			if check && anyDirty {
				return fmt.Errorf("one or more repositories have uncommitted changes")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "exit non-zero if any repo has uncommitted changes (CI)")
	cmd.Flags().IntVar(&jobs, "jobs", 0, "max parallel git operations (default min(8, 2*CPUs))")
	return cmd
}

func branchLabel(s *git.Status) string {
	if s.Detached {
		return "(detached)"
	}
	if s.UpstreamGone {
		return s.Branch + " ⚠gone"
	}
	return s.Branch
}

func abLabel(s *git.Status) string {
	if s.Upstream == "" {
		return "-"
	}
	return fmt.Sprintf("+%d/-%d", s.Ahead, s.Behind)
}

func stateLabel(s *git.Status) string {
	if !s.Dirty() {
		return "clean"
	}
	return fmt.Sprintf("dirty (%d staged, %d unstaged, %d untracked)", s.Staged, s.Unstaged, s.Untracked)
}

// --- ws clone --------------------------------------------------------------

func newWsCloneCmd(g *GlobalOpts) *cobra.Command {
	var jobs int
	cmd := &cobra.Command{
		Use:   "clone [names...]",
		Short: "Clone workspace repos in parallel (skips existing)",
		RunE: func(cmd *cobra.Command, args []string) error {
			repos, err := loadRepos(args)
			if err != nil {
				return err
			}
			gx, err := git.New()
			if err != nil {
				return err
			}
			if jobs <= 0 {
				jobs = defaultJobs()
			}
			results := runPerRepo(cmd.Context(), repos, jobs, func(ctx context.Context, r repo) error {
				if r.url == "" {
					return fmt.Errorf("no git URL declared")
				}
				if gx.IsRepo(ctx, r.dir) {
					// Idempotent: validate the remote matches the expected URL.
					if cur, err := gx.RemoteURL(ctx, r.dir); err == nil && cur != r.url {
						return fmt.Errorf("exists but origin is %q, expected %q", cur, r.url)
					}
					return nil // already cloned
				}
				return gx.Clone(ctx, r.url, r.dir, git.CloneOptions{})
			})
			return reportResults(cmd, g, "clone", results)
		},
	}
	cmd.Flags().IntVar(&jobs, "jobs", 0, "max parallel clones (default min(8, 2*CPUs))")
	return cmd
}

// --- ws sync ---------------------------------------------------------------

func newWsSyncCmd(g *GlobalOpts) *cobra.Command {
	var jobs int
	cmd := &cobra.Command{
		Use:   "sync [names...]",
		Short: "Fetch + fast-forward pull every repo in parallel",
		RunE: func(cmd *cobra.Command, args []string) error {
			repos, err := loadRepos(args)
			if err != nil {
				return err
			}
			gx, err := git.New()
			if err != nil {
				return err
			}
			if jobs <= 0 {
				jobs = defaultJobs()
			}
			results := runPerRepo(cmd.Context(), repos, jobs, func(ctx context.Context, r repo) error {
				if !gx.IsRepo(ctx, r.dir) {
					return fmt.Errorf("not cloned (run `ws clone`)")
				}
				if err := gx.Fetch(ctx, r.dir); err != nil {
					return err
				}
				return gx.Pull(ctx, r.dir)
			})
			return reportResults(cmd, g, "sync", results)
		},
	}
	cmd.Flags().IntVar(&jobs, "jobs", 0, "max parallel syncs (default min(8, 2*CPUs))")
	return cmd
}

// --- ws git ----------------------------------------------------------------

func newWsGitCmd(g *GlobalOpts) *cobra.Command {
	var jobs int
	cmd := &cobra.Command{
		Use:   "git -- <git args...>",
		Short: "Run an arbitrary git command across every repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("usage: ws git -- <git args...>")
			}
			repos, err := loadRepos(nil)
			if err != nil {
				return err
			}
			gx, err := git.New()
			if err != nil {
				return err
			}
			if jobs <= 0 {
				jobs = defaultJobs()
			}
			results := runPerRepo(cmd.Context(), repos, jobs, func(ctx context.Context, r repo) error {
				if !gx.IsRepo(ctx, r.dir) {
					return fmt.Errorf("not cloned")
				}
				return gx.Run(ctx, r.dir, args...)
			})
			return reportResults(cmd, g, "git", results)
		},
	}
	cmd.Flags().IntVar(&jobs, "jobs", 0, "max parallel ops (default min(8, 2*CPUs))")
	return cmd
}

// reportResults prints a per-repo outcome summary and returns an aggregate error
// when any repo failed. Plain prefixed lines (non-TTY-safe) or JSON.
func reportResults(cmd *cobra.Command, g *GlobalOpts, op string, results []repoResult) error {
	failed := 0
	if g.JSON {
		type r struct {
			Name  string `json:"name"`
			OK    bool   `json:"ok"`
			Error string `json:"error,omitempty"`
		}
		out := make([]r, len(results))
		for i, res := range results {
			out[i] = r{Name: res.Name, OK: res.Err == nil}
			if res.Err != nil {
				out[i].Error = res.Err.Error()
				failed++
			}
		}
		if err := writeJSON(cmd, map[string]any{"op": op, "repos": out}); err != nil {
			return err
		}
	} else {
		w := cmd.OutOrStdout()
		for _, res := range results {
			if res.Err != nil {
				failed++
				fmt.Fprintf(w, "[%s] FAILED: %v\n", res.Name, res.Err)
			} else if !g.Quiet {
				fmt.Fprintf(w, "[%s] ok\n", res.Name)
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%s failed for %d of %d repo(s)", op, failed, len(results))
	}
	return nil
}

# Spec 31 — JS/TS + monorepo templates & the task-graph runner (`devstack run`)

**Module:** `templates/` (new built-in app + monorepo templates), `internal/template`/`internal/generate` (unchanged mechanism), `internal/config` (parse `tasks:`), `internal/task` (the DAG planner, new), `internal/run` (the executor, new — reusing `internal/hooks`), `internal/cli` (`run`) · **Milestone:** local-cloud DX lane (0.x beta line) · **Effort:** ~8w (templates ~3w + task runner ~5w, phased) · **ADR:** [D19](../DECISIONS.md) (task graph)

## Purpose
Two gaps, one lane. **(1)** The JS/TS story is a single `node.vite` template ([`templates/node.vite/`](../../templates/node.vite)); there are no first-class templates for the frameworks developers actually run (Express, NestJS, Next.js, React+Vite, Bun) and **no dev-mode hot reload** guidance. **(2)** devstack orchestrates *containers and shared infra* but has **no task graph** — nothing like Turborepo/Nx that runs `build → test → dev` across the packages of a monorepo. Users asked for both: a real template library **and** "projects that run commands / work with monorepos like Turborepo". This spec adds the template library (with hot reload as a first-class requirement) and a **devstack-native task-graph runner**: a non-container `tasks:` kind plus `devstack run`, built on the existing `internal/hooks` command executor and the `ws` bounded-parallel pattern. The container model (long-running services on the shared network) is unchanged; tasks are a **new, orthogonal execution kind** for short-lived, dependency-ordered commands.

Depends on the interactive DX layer only for polish (the `project`/`env` TUIs, [spec 30](30-interactive-dx-and-shell.md), can scaffold these templates); the template and runner work stand alone.

## Decisions

### The JS/TS + monorepo template library
- **New built-in templates, same `go:embed` mechanism as `node.vite`.** Add under `templates/`: `node.express`, `node.nestjs`, `node.next`, `react.vite`, `bun.app` (plus `bun.elysia`/`bun.hono` if cheap), and a `turborepo` monorepo template. Each is a directory with `template.yaml` + `build/Dockerfile` (+ `golden.yaml`), resolved by the existing engine ([spec 02](02-templating-and-generation.md)) — **no engine changes**. They are **project** templates (no `provides:`), so they're hidden from the shared-engine picker exactly like `php.*`/`node.*` today ([spec 22](22-init-wizard.md)).
- **A shared `node.base` parent via `extends`.** The Node templates `extends: node.base` (Node version arg, pnpm/npm corepack, workdir, non-root user), mirroring `php.laravel.nginx extends php.nginx`. Bun templates use a `bun.base`. Keeps the version/toolchain in one place; leaf templates add only their dev command + env.
- **Hot reload is a first-class requirement, not a note.** Every app template ships a dev-server `command` — `["npm","run","dev"]` / `["pnpm","dev"]` / `["bun","run","dev"]`, `["node","--watch",…]`, `nest start --watch`, `next dev`, `vite` — with `NODE_ENV: development`, and a **source bind-mount** so edits on the host hot-reload in the container. The bind-mount is expressed in the template `service.volumes` as a host-relative mount of the project dir onto the workdir, with an anonymous volume masking `node_modules` (so the image's installed deps aren't shadowed by the host).
- **WSL2/9p file-watching correctness.** On WSL2 and other 9p/networked mounts, inotify is unreliable (the same reality behind the `/mnt` refusal, [`internal/xdg`](../../internal/xdg)). Templates set `CHOKIDAR_USEPOLLING=true` / `WATCHPACK_POLLING=true` (Next/webpack) / Vite `server.watch.usePolling` via env, guarded so native Linux/macOS keep inotify. Documented per template and carried as `[Q-WSL-WATCH]`.
- **Deterministic + golden-gated.** Each template ships a `golden.yaml` so `make determinism` asserts byte-identical rendering, exactly as the existing engine templates do.

### The task-graph runner (`tasks:` + `devstack run`)
- **A non-container "task" kind — the load-bearing new concept.** Tasks are **not** containers and **not** shared services; they are short-lived commands with dependency edges. A new optional `tasks:` block in `devstack.yaml` declares them; `devstack run <task>` plans and executes the DAG. This is the "run commands / monorepo orchestration" capability, kept deliberately separate from the container/services model so nothing about the deterministic compose pipeline or ref-counting changes. Codified in [D19](../DECISIONS.md).
- **Reuse the `internal/hooks` executor — do not build a second command runner.** Lifecycle hooks ([spec 11](11-lifecycle-hooks.md), `internal/hooks`) already run `host`/`exec` commands with `workdir`/`env`/`timeout`/`retries`/`onFailure`, never shell-split. A `Task` is the same command shape **plus `deps: []` and `watch: bool`**; `internal/run` drives them, calling the hooks executor per node. One executor, two callers (hooks-at-saga-phases and tasks-on-demand).
- **The DAG uses the `ws` bounded-parallel pattern.** Topological layers executed with `errgroup` + a `--parallel N` cap (default `min(8, 2*CPUs)`), identical to the multi-repo git commands ([`internal/cli/ws.go`](../../internal/cli/ws.go)). A cycle is a config-validation error (reuse the `${ref}` cycle-detection shape from [`internal/config`](../../internal/config)). Output is streamed per-task, color-keyed by task name (the `logs` gutter style).
- **Monorepo-awareness via `--filter` + package discovery.** For a `turborepo` (or any workspace-of-packages) project, `devstack run <task> --filter <pkg>` scopes the DAG to a package and its dependents. Package discovery reads the JS workspace globs (`package.json#workspaces` / `pnpm-workspace.yaml`); the task graph is the product of `(package × task)` with edges from `deps` and inter-package dependencies.
- **Turborepo interop — two supported modes, one default.** A `turborepo` project can either **(a)** run `turbo run <task>` inside its container/toolchain (devstack shells one command; Turbo owns the graph), or **(b)** map its `turbo.json` pipeline into devstack `tasks:` so `devstack run` owns the graph and caching hooks. **Default = (a)** `turbo run` passthrough (least surprising, uses Turbo's own cache); (b) is opt-in for teams that want devstack to own scheduling. Fixed in `[Q-TURBO-DEFAULT]`/[D19](../DECISIONS.md).
- **`run:` targets, not just commands.** A task may target `run: host` (a process on the host, inheriting the user's toolchain) or `run: exec` (inside a named service container, via `compose exec -T` — the hooks `exec` path). Host is the default for build/test/lint; `exec` is for tasks that must run inside a service image.

### Cross-cutting
- **No change to determinism, locking, or ref-counting.** Templates flow through the unchanged generate pipeline (golden-gated). `devstack run` takes **no flock** (it mutates no shared ledger/infra state — like `secrets ingest`); it only spawns processes/exec. Tasks never create or destroy containers or volumes (the never-recreate-a-stateful guard is not even in scope — tasks don't touch infra).
- **Additive, forward-tolerant config.** `tasks:` is optional; a `devstack.yaml` without it is unchanged. Unknown task keys are ignored (the [spec 27](27-resource-layer.md) `resources:` forward-tolerance precedent).

## CLI surface
```
devstack run <task> [task2 ...]     # plan + execute the task DAG for the active/target project
  --project <name>                  #   target project (default: active/first — spec 30)
  --filter <pkg>                    #   monorepo: scope to a package (+ its dependents)
  --parallel <N>                    #   max concurrent tasks (default min(8, 2*CPUs))
  --watch                           #   keep watch-mode tasks running (hot-reload dev servers)
  --dry-run                         #   print the resolved DAG + execution order, run nothing
  --json                            #   stream {task, package, status, code, ...} records
```
```yaml
# devstack.yaml — the new optional tasks: block
tasks:
  build:   { run: host, command: ["pnpm", "build"] }
  test:    { run: host, command: ["pnpm", "test"], deps: [build] }
  lint:    { run: host, command: ["pnpm", "lint"] }
  dev:     { run: host, command: ["pnpm", "dev"], watch: true }
  migrate: { run: exec, service: api, command: ["sh","-lc","pnpm migrate"], deps: [build] }
```

New built-in templates (project kind, no `provides:`):
```
templates/node.base/        # shared parent: Node version, corepack, workdir, user
templates/bun.base/         # shared parent: Bun toolchain
templates/node.express/     # express dev server (node --watch / nodemon)
templates/node.nestjs/      # nest start --watch
templates/node.next/        # next dev (WATCHPACK_POLLING on WSL2)
templates/react.vite/       # vite dev server (usePolling on WSL2)
templates/bun.app/          # bun run dev (+ bun.elysia / bun.hono variants)
templates/turborepo/        # monorepo: turbo run passthrough (default) or tasks: mapping
```

## Acceptance criteria
- `devstack up` on a project using `node.next`/`react.vite` brings up a working dev server with **hot reload** — a host source edit is reflected in the container (native inotify; polling on WSL2).
- `devstack run test` executes `build` then `test` (dependency order); `devstack run build lint` runs both, `lint` in parallel with `build`'s independents; a dependency cycle fails `config validate`.
- `devstack run <task> --filter <pkg>` in a `turborepo` project scopes the DAG to that package + dependents; `--dry-run` prints the order without executing.
- A `turborepo` project runs `turbo run build` by default (mode a); mapping `turbo.json` into `tasks:` (mode b) makes `devstack run` own the graph.
- Every new template ships a `golden.yaml`; `make determinism` stays green; a `devstack.yaml` without `tasks:` behaves exactly as today.
- `devstack run` takes no flock and touches no container/volume/ledger state.

## Open questions
- `[Q-WSL-WATCH]` — the exact per-framework polling knobs (`CHOKIDAR_USEPOLLING`, `WATCHPACK_POLLING`, Vite `server.watch.usePolling`) and whether to auto-detect WSL2 (via `internal/xdg`) and inject them, vs. document-and-opt-in.
- `[Q-TURBO-DEFAULT]` — confirm `turbo run` passthrough as the default (uses Turbo's cache) with `turbo.json`→`tasks:` mapping opt-in; decide whether devstack ever wraps Turbo's remote cache.
- `[Q-TASK-CACHE]` — does `devstack run` gain content-hash task caching (skip unchanged tasks, à la Turbo), or stay a pure scheduler in v1? Lean: pure scheduler first; caching is a later phase.
- `[Q-PKGMGR]` — pnpm vs npm vs bun as the template default and how the runner detects the workspace package manager (lockfile sniff).
- `[Q-TASK-vs-HOOK]` — whether lifecycle hooks ([spec 11](11-lifecycle-hooks.md)) should be re-expressed as tasks over the same executor to avoid two config surfaces, or stay distinct (hooks = saga-phase-attached, tasks = on-demand). Lean: shared executor, distinct config surfaces.
</content>

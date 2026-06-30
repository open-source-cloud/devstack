# Spec 23 — Interactive template & Dockerfile authoring (TUI)

**Module:** `internal/prompt` (new) · `internal/template/scaffold` (new) · `internal/cli` (`template new` / extended `template init`) · **Milestone:** M8 (post-GA polish lane) · **Effort:** ~2.5w

## Purpose
Let an author scaffold a new service template — `template.yaml` + an optional `build/` tree (Dockerfile, entrypoint, conf) + an optional golden fixture — **interactively**, instead of hand-writing the on-disk bundle from the one-paragraph guide in `templates/README.md`. The TUI is a thin front-end over the M1 substrate that already exists ([spec 02](02-templating-and-generation.md)): it prompts for the same fields the typed `Manifest` ([internal/template/model.go]) carries, renders a live preview through the **real** `template.Resolve` → `generate.LintResolved` path, and writes the bundle atomically to the global `$DEVSTACK_HOME/templates` store (`internal/store`, the override-by-name custom-templates dir). It is **strictly additive**: it produces nothing the engine can't already consume, introduces no new template feature, and every interactive flow has a flag-driven / `--json` equivalent so CI and scripts never touch the bubbletea runtime.

This is a *quality-of-onboarding* feature, not a pipeline change. The hard rules of [spec 02](02-templating-and-generation.md) (custom `[[ ]]` delimiters, deterministic FuncMap, compose-go validation, byte-stable output) are the **acceptance gate** the authored bundle must clear, not something this spec relaxes.

## Decisions
- **New verb `template new`, not a rewrite of `template init`.** `template init` stays exactly as it is today (it scaffolds a fixed `template.yaml` + `build/Dockerfile` skeleton, single positional arg, `--quiet`-safe, no TTY needed, refuses to overwrite an existing dir) — scripts and the existing acceptance depend on it. `template new` is the interactive sibling. A bare `template new` with no TTY (piped stdin, `CI` set, `--json`, `--quiet`) **degrades to the same scriptable path as `init`**: it reads answers from flags/`--from` and errors with a clear "no TTY; pass `--name`/`--from` or use `template init`" message rather than hanging on a prompt.
- **The TUI is the *data-entry* layer only.** Every prompt fills one field of an in-memory `scaffold.Spec`; the spec is then handed to a **single pure function** `scaffold.Build(Spec) (Bundle, error)` that the non-interactive path *also* calls. The form and the flag-parser are two front-ends to one builder — there is no behavior reachable only through the TUI (the headline-output contract, [ARCHITECTURE §7.9](../ARCHITECTURE.md)).
- **The TUI is a custom Bubble Tea v2 program with a live two-pane preview — modern aesthetics are a first-class requirement** (owner directive: "a really nice modern TUI for CLIs"). It is a hand-written `charm.land/bubbletea/v2` model built from `charm.land/bubbles/v2` components — `list` (the kind / `extends`-parent / base-image pickers, each row annotated with the parent's `provides`/`exports`/`params`), `textinput`/`textarea` (name, description, param fields, Dockerfile body), and a **`viewport` live-preview pane** rendering the generated `template.yaml` + Dockerfile + compose fragment as you type (the two-pane layout shown in the worked example). `charm.land/huh/v2` is **embedded for the linear param-entry sub-forms** only — it is not the top-level driver. Styling is the shared `charm.land/lipgloss/v2` theme owned by `internal/prompt`/`internal/tui` (adaptive color, rounded borders, keymap footer), the **same theme as [spec 22](22-init-wizard.md)** so every devstack TUI looks like one product. All deps are direct, pure-Go, pinned to the v2 line, byte-aligned with the already-vendored `charm.land/lipgloss/v2 v2.0.1` + `charm.land/fang/v2 v2.0.1`, and CGO-free (see gotchas).
- **App-vs-engine is the first branch.** A buildable **app** template emits `service.build{context:build,dockerfile:Dockerfile}` + a `build/Dockerfile` and **no `provides:`**. A shared **engine** template emits `image:` + `provides`/`exports`/`defaultPort`/`volumes` and **no `build/` tree** — because `generate.buildSharedService` rejects any `build:` key on a shared service ([internal/generate/compose.go]). The wizard refuses to let you mix the two.
- **The builder writes deterministically.** `scaffold.Build` produces a `Bundle` (a `map[relpath][]byte`), emitted byte-stably (sorted, fixed newlines, LF, no trailing whitespace) via the same `writeIfChanged` (same-dir temp + `os.Rename`, `0644`) pattern as `internal/generate`. The same `Spec` always yields byte-identical files — no clock/uuid/random anywhere (there are none in the FuncMap by design; `engine.go` documents the omission explicitly).
- **Meta fields are written as literal YAML, never templated.** `schemaVersion/extends/description/provides/exports/defaultPort/params` are parsed *unrendered* by `parseMeta` ([model.go] discards `service:`/`volumes:` from the meta parse); the builder emits them as plain scalars and **never inserts a `[[ ]]` action into a meta key**. Only `service:`/`volumes:` blocks (and `build/` files) may carry `[[ .params.* ]]`.
- **Live preview uses the production path, not a mock.** As the author edits, the preview pane calls `template.Resolve(memSource, name, nil)` → `generate.LintResolved(name, res)` — the exact render-with-defaults + compose-go/v2 validation that `template lint` runs (see `internal/cli/template.go` `lintTemplateDir`, which is the identical `Resolve`→`LintResolved` chain). What you preview is what `generate` will emit. No second renderer.
- **Lock-free, store-local, secret-free.** Authoring writes only files under `$DEVSTACK_HOME/templates/<name>/`. It never touches the SQLite ledger, never takes the flock, never starts Docker, and never resolves a `secret://` ref (templates declare param *names/defaults*, not secret values — same property as `template init` today). No leak surface is added; the no-plaintext-secret-on-disk CI leak-test ([spec 04](04-secrets.md)) is not even in scope because nothing here can emit a secret value.
- **A golden fixture is scaffolded (opt-in, default on for `new`).** When the author opts in, the builder runs the preview render once and writes it to `<name>/golden.yaml`, so `template test <dir>` has something to byte-assert from day one. The real `template init` writes **no** golden today (it emits only `template.yaml` + `build/Dockerfile`), and `template test` already compares `<dir>/golden.yaml` byte-for-byte when present (`internal/cli/template.go`) — so this closes a genuine gap, not a hypothetical one. The golden is regenerable: `template new --regold <dir>` re-renders and rewrites it.
- **`params.schema.json` stays deferred.** [spec 19 / D16](../DECISIONS.md) says the editor schema travels with a template, but it's a v2 registry concern; the wizard does **not** emit one in this milestone (one less thing to keep in sync, and nothing in v1 reads it — `template init`/`lint`/`test` do not produce or consume a schema today, despite spec 19's prose). Tracked as an open question below.

## CLI surface
```
devstack template new [name]                 # interactive when stdin is a TTY; else flag-driven
  --kind app|engine                          # app ⇒ build/Dockerfile, no provides; engine ⇒ image:, provides:
  --name <ref>                               # template name (single path segment; dots ok, no slash/"..")
  --from <file.yaml>                          # non-interactive: read a scaffold.Spec (YAML) instead of prompting
  --extends <ref>                             # parent template to extend (app or engine)
  --description <text>
  --base-image <image>                        # app: FROM <image>; engine: image: <image>
  --param name:type[:default][:required]      # repeatable; type ∈ string|int|bool
  --port <int>                                # engine: defaultPort; app: published-port hint (advisory)
  --provides <capability>                     # engine only
  --exports k1,k2,...                         # engine only
  --entrypoint                                # app: also scaffold build/entrypoint.sh
  --golden / --no-golden                      # scaffold <name>/golden.yaml (default: on)
  --regold <dir>                              # re-render the golden for an existing template, write nothing else
  --dir <parent>                              # parent dir (default: $DEVSTACK_HOME/templates, == store.TemplatesPath())
  --dry-run                                   # print the would-be bundle as a tree + per-file diff, write nothing
  --force                                     # overwrite an existing template dir (backs up first)
  --print-spec                                # emit the resolved scaffold.Spec as YAML to stdout and exit (for --from round-trip)
```
- `template init` is **unchanged** and remains the documented scriptable minimum (its existing `--dir` default is `store.TemplatesPath()`; `new` matches it).
- `--json` makes `new` non-interactive and machine-readable: it requires `--name`/`--kind` (or `--from`), runs the builder, and prints `{"template":"<name>","path":"...","files":[...],"wrote":bool}`. No prompts, no footer, no bubbletea.

## Behavior
The interactive flow (TTY path) is a custom Bubble Tea v2 model with a live preview pane (embedded `huh` sub-forms for linear param entry) whose every input maps to a `scaffold.Spec` field; the builder pipeline below is identical on the non-interactive path (flags/`--from` populate the same `Spec`).

1. **Kind & name.** Prompt `kind` (app / engine), then `name`. The name input validates live against the same rule as `source.validRef` — single path segment, dots allowed (`php.laravel.nginx`), **no slash, no `..`, not absolute** (`validRef` requires `ref == path.Base(ref) && !filepath.IsAbs(ref)`) — and rejects a name that already resolves in the store/built-in source unless `--force`.
2. **Extends (optional).** Offer an "extends parent" picker populated by `template.Describe` over the store ∪ embedded source. Selecting a parent shows its `provides`/`exports`/`params` so the author sees what they inherit. Surface the [spec 02](02-templating-and-generation.md) deep-merge gotcha inline: **lists REPLACE by default**; offer a checkbox to emit `$merge: append` on list keys the child overrides.
3. **Base image & description.** For an app: `FROM <base-image>`; for an engine: `image: <base-image>`. Free-text description.
4. **Params loop.** Repeatedly add params: `name`, `type` (string|int|bool — advisory only, [model.go] `ParamSpec.Type`), `default`, `required` (bool). Required params with no default are flagged; the builder enforces "a referenced-but-undeclared param is a hard error" by construction (every `[[ .params.X ]]` it writes corresponds to a declared param — `Option("missingkey=error")` in `engine.go` would otherwise fail the preview, and `effectiveParams` fails fast on a missing *required* value).
5. **Engine specifics** (kind=engine only): `provides` (capability string), `exports` (multi-select / comma list), `defaultPort`, optional named `volumes`. **No `build/` is offered** — choosing engine disables the Dockerfile step entirely.
6. **App specifics** (kind=app only): optionally add `build/entrypoint.sh` and arbitrary extra `build/*.conf` files. The Dockerfile is pre-seeded with a `# syntax=docker/dockerfile:1` header and a literal-`$`-safe body where only `[[ .params.* ]]` is templated.
7. **Build → preview.** On every change (and on the final "Review" screen) the wizard calls `scaffold.Build(Spec)` → `template.Resolve` → `generate.LintResolved`. The preview pane shows (a) the rendered single-service compose document and (b) compose-go/v2 validation status. A render or validation error is shown in-pane and **blocks the write button** — you cannot scaffold a bundle that wouldn't generate.
8. **Lint checks.** Before write, run the authoring lints. These are **new** checks: `LintResolved` today performs only compose-go validation, and `internal/generate/lint.go` implements only the workspace alias-collision guardrail — neither the delimiter-collision nor the param-type check exists yet, even though [spec 19](19-template-registry.md) attributes them to `template lint`. This spec adds: **delimiter-collision** (a literal `[[`/`]]` in a `build/` file that isn't a known action → warn), **param-type** (a `default` that doesn't parse as its declared `type` → warn), and **meta-templating** (a `[[ ]]` action in a meta key → hard error). These also run inside the live preview, and should be wired into `template lint` proper so authored and registry-consumed bundles share one check set.
9. **Confirm & write.** The Review screen prints the bundle as a file tree. On confirm, `Bundle` is written under `--dir/<name>/` with `writeIfChanged` (atomic, `0644`); refuses an existing dir unless `--force` (which backs up the prior dir to `<name>.bak.<unixts>/` first). `--dry-run` prints the tree + a unified diff against any existing files and writes nothing.
10. **Golden.** Unless `--no-golden`, the builder writes `<name>/golden.yaml` = the previewed compose, so `template test <name-dir>` passes immediately and guards future edits.
11. **Footer / next step.** On success print the path and the exact follow-ups: `template lint <dir>`, `template test <dir>`, and how to reference it from a repo's `devstack.yaml` (`services: { app: { template: <name> } }`). The wizard does **not** auto-edit a project's `devstack.yaml` in v1 (open question below).

```go
// internal/template/scaffold — one builder, two front-ends (TUI + flags) feed it.
package scaffold

type Kind string // "app" | "engine"

type Param struct{ Name, Type, Default, Description string; Required bool }

type Spec struct {
    Kind        Kind
    Name        string   // validated against source.validRef
    Extends     string   // optional parent ref
    Description string
    BaseImage   string   // app: FROM; engine: image:
    Params      []Param
    // engine-only:
    Provides    string
    Exports     []string
    DefaultPort int
    Volumes     []string
    // app-only:
    Entrypoint  bool
    ExtraBuild  []string // extra build/<name> files to seed
    Golden      bool
}

type Bundle map[string][]byte // relpath ("template.yaml","build/Dockerfile",…) → bytes, byte-stable

func Build(s Spec) (Bundle, error) // pure, deterministic; emits meta as literal YAML, [[ ]] only in service/build
```

```go
// internal/prompt — thin interface over huh/v2 so non-TTY paths never enter bubbletea.
package prompt

type Asker interface {
    // Run returns ErrNoTTY when stdin is not a terminal; callers fall back to flags/--from.
    Run(ctx context.Context, form *Form) error
}
func IsInteractive(in *os.File) bool // term.IsTerminal && !CI && !--json && !--quiet
```

## Verified constraints / gotchas
- **Use `charm.land/huh/v2`, NOT `github.com/charmbracelet/huh` (v1).** The v1 module pulls bubbletea v1 + lipgloss v1 and would double-vendor a *conflicting* charm stack against the already-vendored `charm.land/lipgloss/v2 v2.0.1` + `charm.land/fang/v2 v2.0.1`. huh v2 (currently `v2.0.3`) is built on bubbletea v2 / lipgloss v2 and imports as `charm.land/huh/v2`; add its transitive `charm.land/bubbletea/v2` + `charm.land/bubbles/v2` and pin the whole set to the v2 line. Keep `make vuln` (govulncheck) in CI after adding.
- **huh/bubbletea v2 are cgo-free — the static build holds.** All terminal I/O goes through `golang.org/x/sys` + `charmbracelet/x/term` + `x/ansi` + `ultraviolet`, all already vendored (`go.mod` already carries `charmbracelet/ultraviolet`, `x/term`, `x/ansi`, `x/termios`, `x/windows` as indirect deps of fang/lipgloss) under `CGO_ENABLED=0`; huh adds no native deps beyond that family. No build tags, works across `darwin/{amd64,arm64}` + `linux/{amd64,arm64}` from the one Linux runner.
- **A TUI must never be the only path.** `form.Run()` returns an error when stdin is not a TTY; gate every interactive entry on `term.IsTerminal(os.Stdin.Fd()) && os.Getenv("CI")=="" && !json && !quiet` and fall back to flags/`--from`. huh also has `.WithAccessible()` for screen-reader mode — wire it, but it is not the non-TTY fallback. This is the [ARCHITECTURE §7.9](../ARCHITECTURE.md) headline-output contract: `--json`/`--quiet`/non-TTY/`CI` must reach the builder without touching bubbletea.
- **Determinism is the acceptance gate, not a nicety.** The builder must emit byte-identical bytes for a given `Spec`, and the *rendered* bundle must re-render byte-identically (`make determinism`). Naive "throw a timestamp/UUID into the scaffolded comment header" is wrong — there is no `now`/`uuid`/`random`/`env` in the FuncMap on purpose (`funcs.go`/`engine.go` document the omission), and `missingkey=error` turns a referenced-but-undeclared param into a hard failure, not an empty string. The wizard must never offer a non-deterministic helper.
- **Don't template meta keys.** `parseMeta` reads `schemaVersion/extends/description/provides/exports/defaultPort/params` *before* rendering and **discards `service:`/`volumes:`** from that parse (re-reading them from the rendered document); a `[[ ]]` action in a meta key is silently un-rendered garbage. The builder emits meta as literal YAML and confines actions to `service:`/`volumes:`/`build/` — and the meta-templating lint (step 8) makes a stray action a hard error.
- **Engine templates must be image-based.** `generate.buildSharedService` rejects *any* `build:` key on a shared service ("shared services must be image-based; build contexts are not supported"), so the wizard must structurally prevent an engine kind from getting a `build/` tree (it's a compile-time branch in `scaffold.Build`, not a runtime check the author can bypass).
- **Keep author `$`-syntax literal.** The seeded Dockerfile/entrypoint must keep `$VAR` / `${VAR:-default}` / `$TAG` untouched and use `[[ ]]` only for param substitution — this is [spec 02](02-templating-and-generation.md) acceptance #2 (delimiter non-collision), exercised by `php.nginx/build/Dockerfile`. The delimiter-collision lint warns if a `build/` file contains a bare `[[`/`]]` that isn't a known action.
- **Name validation must mirror `source.validRef` exactly.** Single path segment, dots allowed, no slash, no `..`, not absolute. A wizard that accepts `my/app` writes a directory the source layer can never resolve.
- **Write atomically, never clobber silently.** Same-dir temp + `os.Rename`, `0644`; refuse an existing template dir without `--force`; with `--force`, back up first — mirroring `template init`'s refuse-to-overwrite and `internal/generate`'s `writeIfChanged`.
- **Lock-free and ledger-free by construction.** [spec 19](19-template-registry.md) states `init`/`lint`/`test` are read-only/local and take no flock; `template new` inherits that — it writes only `$DEVSTACK_HOME/templates` files and must not import `internal/state` or `internal/lock`.

## Worked example
`devstack template new` (TTY), authoring a buildable Node app template called `node.bun`:

```
? Kind                    › app (buildable)         ▌preview ───────────────────
? Name                    › node.bun                ▌services:
? Extends (optional)      › (none)                  ▌  node.bun:
? Base image              › oven/bun:1              ▌    build:
? Description             › Bun + Vite dev server   ▌      context: build
+ Param  name=bunVersion type=string default="1" required=false   ▌      dockerfile: Dockerfile
+ Param  name=port       type=int    default="5173"               ▌    restart: unless-stopped
[x] Scaffold build/entrypoint.sh                    ▌    environment:
[x] Write golden.yaml                               ▌      NODE_ENV: development
                                                    ▌  ✓ compose-go: valid
```

On confirm the builder writes (byte-stable):

```
$DEVSTACK_HOME/templates/node.bun/
├── template.yaml
├── build/
│   ├── Dockerfile
│   └── entrypoint.sh
└── golden.yaml
```

`template.yaml` (meta literal, actions only under `service:`):
```yaml
schemaVersion: 1
description: "Bun + Vite dev server"
params:
  bunVersion:
    type: string
    default: "1"
  port:
    type: int
    default: "5173"
service:
  build:
    context: build
    dockerfile: Dockerfile
    args:
      BUN_VERSION: "[[ .params.bunVersion ]]"
  restart: unless-stopped
  environment:
    NODE_ENV: development
```

`build/Dockerfile` ( `$`-syntax literal, `[[ ]]` only for the param):
```dockerfile
# syntax=docker/dockerfile:1
FROM oven/bun:[[ .params.bunVersion ]]
WORKDIR /app
COPY build/entrypoint.sh /entrypoint.sh
# author shell stays literal — ${PORT:-5173} is NOT a template action:
ENTRYPOINT ["/entrypoint.sh"]
```

The same bundle is reproducible headlessly — the round-trip the CI determinism check exercises:
```
devstack template new --print-spec --kind app --name node.bun --base-image oven/bun:1 \
  --param bunVersion:string:1 --param port:int:5173 --entrypoint > spec.yaml
devstack template new --from spec.yaml --json     # byte-identical bundle, no TTY
```

## Acceptance criteria
- [ ] `template new` on a TTY walks the wizard and writes a valid bundle; the same inputs via `template new --from spec.yaml` (no TTY) produce a **byte-identical** directory.
- [ ] `template new` with no TTY / `--json` / `--quiet` / `CI` set never enters bubbletea; it either runs from flags/`--from` or exits with a "no TTY; pass `--name`/`--from`" error — it never hangs on a prompt.
- [ ] An authored app bundle passes `template lint <dir>` and `template test <dir>` immediately (the scaffolded `golden.yaml` matches the rendered output byte-for-byte).
- [ ] Choosing `--kind engine` emits `image:`/`provides`/`exports`/`defaultPort` and **no `build/` tree**; the wizard structurally prevents an engine from getting a `build:` key (would be rejected by `generate.buildSharedService`).
- [ ] A name with a slash, `..`, or leading `/` is rejected with the same rule as `source.validRef`; an existing template dir is not overwritten without `--force` (which backs up first).
- [ ] No scaffolded file contains a non-deterministic value; `make determinism` stays green and `scaffold.Build` is shown idempotent by a unit test (same `Spec` → same bytes).
- [ ] A `[[ ]]` action placed in a meta key is a hard lint error; a literal `[[`/`]]` in a `build/` file that isn't a known action warns (delimiter-collision); a `default` that doesn't parse as its declared `type` warns.
- [ ] The live preview renders through `template.Resolve` + `generate.LintResolved` and blocks the write button on a render/validation error.
- [ ] Authoring writes only `$DEVSTACK_HOME/templates` files; it imports neither `internal/state` nor `internal/lock`, starts no Docker, and resolves no `secret://` ref.
- [ ] `template init` behavior, flags, and output are unchanged.

## Dependencies / consumers
Consumes `internal/template` (`Resolve`/`Describe`/`source.validRef`/the FuncMap), `internal/generate` (`LintResolved` for preview, `writeIfChanged` pattern), `internal/store` (`TemplatesPath` emit target + override-by-name wiring for the global `$DEVSTACK_HOME` store), and the new `internal/prompt` (huh/v2 wrapper) + `internal/template/scaffold` (the builder). New deps (all direct, v2 line): `charm.land/bubbletea/v2` + `charm.land/bubbles/v2` + `charm.land/huh/v2` (currently `v2.0.3`), pinned alongside the vendored `charm.land/lipgloss/v2 v2.0.1` / `charm.land/fang/v2 v2.0.1`. Consumed by `internal/cli` (`template new`). Feeds the [spec 19](19-template-registry.md) registry work: a bundle authored here is exactly the shape a future `template publish`/bundle target packages, and the new delimiter-collision/param-type lints should land in shared lint code both `template new` and the spec-19 `template lint` call.

## Open questions
- **`params.schema.json` emission.** [spec 19 / D16](../DECISIONS.md) wants the editor schema to travel with a template, but v1 reads no such file (and `template init`/`lint`/`test` neither emit nor consume one today, despite spec 19's prose). Recommendation: defer — emit nothing now, add a `--with-schema` flag when the registry lands so the schema and the bundle stay in one owner. **Decision:** deferred to the registry milestone; not in this spec. (Spec 19's `template init` description should be reconciled to match the actual implementation.)
- **Auto-wire into `devstack.yaml`.** Should `template new` offer to add a `services: { app: { template: <name> } }` block to the nearest `devstack.yaml` so the app is immediately usable? Recommendation: keep authoring and project-wiring separate in v1 (a template is reusable across repos; wiring is a per-repo edit) — stop at the template dir, print the snippet to copy. **Decision:** print-only in v1; an opt-in `--wire <service>` is a fast-follow.
- **Scaffolding a new *built-in* (`templates/` + `embed.go`).** A store template needs no code change, but a built-in requires a manual `go:embed` edit + recompile. Recommendation: `template new` always targets the store; promoting a store template to a built-in stays a deliberate maintainer PR, not a wizard action. **Decision:** store-only target; built-in promotion is out of scope.
- **How much merge-semantics UI for `extends`.** Surfacing the list-replace-vs-`$merge: append` gotcha ([spec 02](02-templating-and-generation.md)) interactively risks overwhelming a first-time author. Recommendation: show it only when the child actually overrides a list key the parent defines, with a one-line explainer + a checkbox. **Decision:** contextual, not always-on.
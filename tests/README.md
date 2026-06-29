# tests/

Cross-cutting tests that exercise the **built `devstack` binary** end to end,
complementing the per-package unit + `//go:build integration` tests under
`internal/`.

## Layout

- **`e2e/`** — drives the compiled CLI in an isolated XDG sandbox + temp
  workspace. Two tiers, both behind `//go:build e2e` (so they never run in the
  fast unit lane and only build the binary when asked):
  - **functional** (no daemon): `generate`, `config validate`, `template list`,
    `status`, `version`, and error paths like `up` outside a workspace.
  - **daemon e2e** (Docker): a full `up → status → re-up(idempotent) → down`
    against a real Engine. Gated on `DEVSTACK_E2E=1` because it mutates Docker
    (the shared stack + `devstack_shared` network); each test cleans up after
    itself with `t.Cleanup`.

## Running

```bash
make e2e          # DEVSTACK_E2E=1 go test -tags=e2e ./tests/e2e/...   (needs Docker)
go test -tags=e2e ./tests/e2e/...                                      # functional only (daemon tests skip)
make integration  # the internal/ -tags=integration suite (needs Docker)
```

CI runs all of these in the consolidated `ci` lane (cheap → expensive, fail-fast)
on a Docker-enabled runner; see `.github/workflows/ci.yml`.

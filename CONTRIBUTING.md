# Contributing to FlashBackup

Thanks for considering a contribution.

## Scope

FlashBackup targets a narrow wedge: portable USB-runnable macOS backup with strict integrity guarantees. Bug fixes always welcome. New features must align with the wedge; off-wedge proposals get a friendly "please fork" response.

## Building

See `Makefile`. `make build` for a local debug binary, `make ci-local` to mirror CI.

## Reporting bugs

Bugs go in GitHub Issues. Data-loss bugs are Sev1: use the "data-loss-report" issue template; MM will respond within 24 hours.

## Coding conventions

- **Go file naming:** `snake_case.go` (Go convention). On-disk state files: `kebab-case.{ndjson,json}`.
- **Conventional Commits:** `feat:`, `fix:`, `chore:`, `build:`, `docs:`, `test:`, `refactor:` (no others). Breaking changes use `!:` suffix.
- **Error wrapping:** wrap once at the package boundary with `fmt.Errorf("<verb> <noun>: %w", err)`. Don't double-wrap.
- **Comment policy:** comments explain WHY, not what. Reference locked invariants as `// invariant #N`.
- **Per-package `doc.go`:** every `internal/*` package has a `doc.go` containing: package purpose, invariants it enforces, state diagram if non-trivial.
- **`context.Context`:** every public function that does I/O or runs a subprocess takes `ctx context.Context` as the first parameter.
- **Tests:** `t.Helper()` on shared helpers; table-driven `t.Run` subtests when 2+ cases; `t.TempDir()` for tmp paths; `t.Cleanup` for teardown.

## Workflow

Trunk-based development. Branch from `main`, PR back to `main`. CI runs lint + test + e2e-fast + e2e-safety + coverage + symbol-scan release gate.

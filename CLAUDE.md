# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project status

**Pre-implementation, design in progress.** Brainstorming via the Superpowers flow. Sections 1-6 of the design spec are locked; section 7 (TUI) is mid-design; sections 8-9 are pending. No source code, no git, no build system, no test suite yet. Do not invent commands.

## Source-of-truth documents

| File | Purpose |
|---|---|
| `docs/specs/2026-06-03-1338-flashbackup-prd.md` | Canonical PRD. Working product title: **FlashBackup**. |
| `docs/specs/2026-06-03-1532-flashbackup-design.md` | Working design spec. Captures all locked decisions, architecture, on-disk layout, run flow, move semantics, verify command, error handling. Status: draft-in-progress. |
| `docs/BACKLOG.md` | Rolling history of decisions, open items, and project status. Updated as work proceeds. |
| `docs/archive/2026-06-03-portable-macos-backup-utility-original.txt` | Original PRD as received (preserved for historical record). Superseded by the relocated copy in `docs/specs/`. |

Read the design spec first; it captures the current state of all 23 locked invariants. Fall back to the PRD for any context the design spec doesn't carry forward.

## Non-negotiable invariants (summary)

The design spec carries the full list of 23 locked invariants. The most critical for any future implementation work:

- **Move semantics:** copy → validate → delete with atomic gate. Any non-verified file blocks all source deletion.
- **Validation:** SHA256 captured at source-read time + at dest-read time; compare. Manifest stores source-side hash.
- **Source mutation gate:** re-stat source at T3 before unlink; skip if `(size, mtime_ns)` changed since T0.
- **Portability:** runs from USB, no install, no admin, all state under `<USB>/.flashbackup/`.
- **Filesystem requirement:** APFS or HFS+ only. Refuse exFAT with reformat recipe.
- **Namespacing:** destination paths prefixed by `<hostname>-<username>`.
- **Notarization in scope.** Build pipeline requires Apple Developer Program account.
- **Engine:** Go + Bubble Tea TUI + embedded GNU rsync 3.x (GPLv3 acceptable; FlashBackup itself is open-source).

If a proposed approach violates any locked invariant, stop and surface the conflict.

## Workflow

Before writing any code, the Superpowers flow must complete:

1. **Brainstorm** (in progress) with `superpowers:brainstorming`.
2. **Apply `spec-development-discipline`** when finalizing the design spec (substantial: multi-week, hard safety requirements).
3. **Multi-hat review** the spec per global CLAUDE.md menu (CTO, Enterprise Architect, BA, CIO, CISO, Hacker, UX, DevOps/SRE, DPO, QA, End User). Subagent-driven, parallel.
4. **Write implementation plan** with `superpowers:writing-plans`.
5. **Multi-hat review** the plan (CISO, Hacker, DevOps/SRE, QA, Senior Developer, DX).
6. **Execute** with `superpowers:subagent-driven-development`.
7. **Debug** with `superpowers:systematic-debugging` for any test failure or unexpected behavior.

**Hard rule from PRD §11 and the design spec:** do not start coding without explicit approval. No "let me just try a prototype."

## When the project moves into implementation

Update this file with: how to build, how to run tests, how to run the binary from a mounted USB volume, and any platform quirks for Intel vs Apple Silicon. Remove the "Project status" section once code lands.

## Housekeeping

- `.superpowers/` directory at repo root holds visual companion mockups. Add to `.gitignore` once the repo is git-initialized.
- Project not yet under version control. Recommend `git init` before any implementation work begins.

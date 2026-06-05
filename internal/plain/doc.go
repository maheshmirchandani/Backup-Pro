// Package plain implements runner.Renderer for terminal output. It is the
// CLI's only sanctioned UI surface for FlashBackup v0.1; the future Bubble
// Tea TUI (Plan 2) will satisfy the same Renderer interface by translating
// UIEvents into tea.Msg values.
//
// Boundary rule: this package owns ALL CLI output formatting. The
// cmd/flashbackup main and every internal package collect data and pass it
// to the renderer via UIEvent values; no other package may fmt.Fprintln to
// the operator-facing stdout/stderr. This rule keeps the rendering vocabulary
// (phase names, status glyphs, progress shape, summary block) in one file so
// a refactor of the human-readable text never has to grep across the tree,
// and so the plain/TUI parity contract holds: both renderers consume the
// same UIEvent stream and produce semantically equivalent output.
//
// Two render modes, selected at construction (NewPlainRenderer(out, isTTY)):
//
//   - TTY (isTTY=true): interactive output. Per-file started/completed lines
//     are suppressed to reduce volume; progress events overwrite the prior
//     progress line via a leading carriage return and are throttled to at
//     most 10 Hz so the terminal does not flicker.
//   - non-TTY (isTTY=false): pipe-friendly output. Every event becomes a
//     newline-terminated line. UIEvtProgress is dropped entirely; the
//     durable record of throughput lives in events.ndjson, and progress
//     ticks at 50/sec would only spam a log file.
//
// Invariants enforced by this package:
//
//   - Concurrency safety: OnEvent may be called from multiple goroutines
//     (the runner's rsync-parser progress callback runs in parallel with
//     phase-function emissions). All writes to out are serialized through a
//     sync.Mutex so terminal lines are never interleaved.
//   - PS3 fail-open contract: an unknown UIEventKind produces a "??" fallback
//     line and a nil return; the renderer never panics or refuses an event
//     the runner emits. Underlying io.Writer errors ARE returned to the
//     caller wrapped, so cmd can decide whether to keep running with a
//     half-broken terminal or abort.
//   - Verify-side compatibility: internal/verify/rehash emits UIEvent with
//     Phase="verify" (a wire string outside the runner Phase enum, per the
//     rehash.go phaseWire comment). The renderer handles this gracefully via
//     the same path as a runner Phase; the wire string is the source of
//     truth for the human-readable label.
//   - No persistence: this renderer never writes to disk. events.ndjson and
//     runs.ndjson are the durable record; the renderer is best-effort UI.
//
// PS3 of the plan strategic decisions places Renderer in runner/types so
// runner and plain are siblings, not in an import cycle. This package
// imports runner/types only; runner never imports plain.
package plain

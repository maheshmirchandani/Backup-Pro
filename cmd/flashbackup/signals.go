package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// secondSignalWindow is the design-spec window for second-signal escalation
// (spec section 6: "Second signal of the same type within 5 seconds forces
// immediate exit"). Kept as a const because the value is part of the user
// contract: a friend who knows "two Ctrl-Cs forces exit" expects it to work
// even on a fast machine; loosening this would silently change behaviour.
const secondSignalWindow = 5 * time.Second

// secondSignalExitCode is the POSIX convention from sh(1): a process killed
// by signal N exits 128 + N. SIGINT is 2, so a Ctrl-C-killed process exits
// 130. We forward that exact code to mirror what `kill -INT` followed by
// `wait` would report; scripts that probe for "user cancelled" recognise it.
const secondSignalExitCode = 130

// installSignalHandlers wires SIGINT + SIGTERM and the second-signal-within-
// 5s escalation. It returns a child context that is cancelled on the first
// signal and a cleanup func that stops the underlying notifier.
//
// Behaviour:
//   - signal.NotifyContext gives us the first-signal cancellation.
//   - A monitor goroutine watches an additional os.Signal channel. The FIRST
//     signal records a UTC timestamp via atomic.Int64 and lets the cancel
//     path drive a graceful abort. A SECOND signal whose receipt is within
//     secondSignalWindow of the first calls os.Exit(secondSignalExitCode).
//   - errSink receives a one-line "second signal received, forcing exit"
//     notice before the os.Exit so the operator knows why the process died
//     without a clean summary block. The runner's own stderr writes during
//     phase teardown may interleave with this notice; that is acceptable for
//     a force-exit path where the alternative is silent kill.
//
// Why a dedicated goroutine instead of just signal.NotifyContext: the stdlib
// notifier cancels ctx exactly once and then drops further signals. To honour
// the design-spec "two signals forces exit" contract we have to keep a
// channel open for the second arrival.
func installSignalHandlers(parent context.Context, errSink io.Writer) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var firstSignalNS atomic.Int64

	// stopCh is closed by the cancel func to tear down the monitor goroutine.
	// Separate from sigCh so we never race a "close the channel we're reading
	// signals from" against a concurrent kernel-driven send.
	stopCh := make(chan struct{})
	// cancelOnce guards the close-stopCh path so a doubly-invoked CancelFunc
	// (defer + explicit caller cleanup) cannot panic on the second close.
	var cancelOnce sync.Once

	go func() {
		for {
			select {
			case <-stopCh:
				return
			case sig := <-sigCh:
				now := time.Now().UnixNano()
				prev := firstSignalNS.Load()
				if prev != 0 && now-prev <= int64(secondSignalWindow) {
					// Second signal inside the window. Force exit. The
					// errSink write is best-effort; we ignore the error
					// because we are about to call os.Exit anyway and
					// re-raising would lose the user's intent.
					_, _ = fmt.Fprintf(errSink, "\nflashbackup: second %s received within %s; forcing exit\n",
						sig, secondSignalWindow)
					os.Exit(secondSignalExitCode)
				}
				if prev == 0 {
					firstSignalNS.Store(now)
				}
			}
		}
	}()

	cancel := func() {
		cancelOnce.Do(func() {
			stop()
			signal.Stop(sigCh)
			close(stopCh)
		})
	}

	return ctx, cancel
}

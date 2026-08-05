// Package debug makes a wedged orbit say what it is waiting on.
//
// A dashboard that stops responding leaves nothing behind. Go's own answer —
// SIGQUIT — writes every goroutine's stack to stderr, which for a full-screen
// program is the terminal it has been painting over: the traceback lands in
// the alt screen, is mangled by whatever redraw came last, and takes the
// process with it. So the one moment the state is available is the one moment
// it cannot be read.
//
// SIGUSR1 writes the same stacks to a file instead, and leaves the process
// running. Nothing else in orbit uses that signal, so it costs one goroutine
// and is always armed rather than something to remember to enable before a
// freeze that has, by then, already happened.
package debug

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/sadrig91/orbit/internal/format"
)

// DumpPath is where the stacks go. Beside orbit's other state rather than in
// the system temp directory, which on macOS is a per-user /var/folders path
// nobody could be told to look in over a chat.
func DumpPath() string { return format.Home(".cache", "orbit", "goroutines.txt") }

// ListenForDumps arms SIGUSR1 for the life of the process:
//
//	kill -USR1 $(pgrep -x orbit)
//
// Returns the path so callers can tell people where to look.
func ListenForDumps() string {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			writeDump(DumpPath())
		}
	}()
	return DumpPath()
}

// writeDump captures every goroutine, not just the running ones — the point is
// to see what is blocked and on what.
func writeDump(path string) error {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		// Truncated: the whole value of this is completeness, so grow and
		// retry rather than hand back a dump missing the goroutine that matters.
		if len(buf) >= 64<<20 {
			buf = buf[:n]
			break
		}
		buf = make([]byte, len(buf)*2)
	}

	// Owner-only. A stack dump is the least revealing thing orbit writes —
	// function names and goroutine states, not conversation content — but it
	// lands in the same directory as the summaries, which are the opposite,
	// and there is no reason for either to be world-readable on a shared box.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "orbit goroutine dump — %s\n%d goroutines\n\n",
		time.Now().Format(time.RFC3339), runtime.NumGoroutine())
	_, err = f.Write(buf)
	return err
}

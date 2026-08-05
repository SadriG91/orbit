package debug

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The dump has to contain every goroutine, not just running ones — a wedged
// dashboard is interesting precisely because everything is blocked.
func TestWriteDumpCapturesBlockedGoroutines(t *testing.T) {
	// A goroutine parked on a channel nobody will ever send to, which is the
	// shape of the bug this exists to catch.
	stuck := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(done)
		<-stuck
	}()
	<-done

	path := filepath.Join(t.TempDir(), "dump.txt")
	if err := writeDump(path); err != nil {
		t.Fatalf("writeDump: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)

	if !strings.Contains(got, "goroutine ") {
		t.Fatalf("no goroutines in the dump:\n%s", got)
	}
	if !strings.Contains(got, "orbit goroutine dump") {
		t.Error("the dump lost its header")
	}
	// The blocked goroutine and its reason both have to be there.
	if !strings.Contains(got, "chan receive") {
		t.Errorf("a goroutine blocked on a channel was not reported:\n%s", firstLines(got, 20))
	}
	if !strings.Contains(got, "internal/debug.TestWriteDumpCapturesBlockedGoroutines") {
		t.Errorf("the dump does not name the blocking function:\n%s", firstLines(got, 20))
	}
	close(stuck)
}

// The path has to be one a person can be told over a chat while their
// dashboard is wedged, which rules out the system temp directory: on macOS
// that is a per-user /var/folders path nobody would guess.
func TestDumpPathIsFindable(t *testing.T) {
	t.Setenv("HOME", "/home/example")
	got := DumpPath()
	if got != DumpPath() {
		t.Error("DumpPath moved between calls")
	}
	if want := "/home/example/.cache/orbit/goroutines.txt"; got != want {
		t.Errorf("DumpPath = %q, want %q", got, want)
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// A dump lands beside the summaries, which are conversation content, and there
// is no reason for either to be readable by every account on a shared machine.
func TestDumpIsOwnerOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := DumpPath()

	if err := writeDump(path); err != nil {
		t.Fatalf("writeDump: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("dump mode = %v, want 0600", got)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("cache dir mode = %v, want 0700", got)
	}

	// Rewriting must not widen it — O_TRUNC on an existing file keeps the old
	// mode, so a dump written before this change stays as it was, but a fresh
	// one must never come back permissive.
	if err := writeDump(path); err != nil {
		t.Fatalf("second writeDump: %v", err)
	}
	fi, _ = os.Stat(path)
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode after rewrite = %v, want 0600", got)
	}
}

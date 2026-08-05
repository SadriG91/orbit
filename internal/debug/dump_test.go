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

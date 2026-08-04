package term

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sadrig91/orbit/internal/tmux"
)

// TestLiveGhosttyRoundTrip drives the real thing: a tmux session on the orbit
// socket, a real Ghostty tab opened over the dictionary, a focus by id, and
// teardown. It opens and closes a visible tab, so it only runs when asked:
//
//	ORBIT_TERM_LIVE=1 go test ./internal/term -run TestLive -v
func TestLiveGhosttyRoundTrip(t *testing.T) {
	if os.Getenv("ORBIT_TERM_LIVE") != "1" {
		t.Skip("set ORBIT_TERM_LIVE=1 to run the live terminal test")
	}
	if detect() != ghosttyAPI {
		t.Skip("needs to run inside Ghostty 1.3+")
	}
	if err := tmux.InstallConf(); err != nil {
		t.Fatalf("install conf: %v", err)
	}

	const name = "orbit-term-live-test"
	const title = "orbit · live test"
	if err := tmux.Spawn(name, t.TempDir(), "true", title, "claude", ""); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer tmux.Kill(name)

	id, err := OpenTab(tmux.AttachArgv(name), "")
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if id == "" {
		t.Fatal("OpenTab returned no id on the API route")
	}
	t.Logf("opened tab %s", id)
	defer osascript(fmt.Sprintf(`
tell application "Ghostty"
	repeat with w in windows
		repeat with tb in tabs of w
			if id of tb is %s then
				close tab tb
				return "closed"
			end if
		end repeat
	end repeat
end tell`, asString(id)))

	// The attach and the OSC 2 title push need a moment to land.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err = Focus(id, title); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Focus(%s, %q) never succeeded: %v", id, title, err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Focus by title alone must find it too — that's the fallback for
	// sessions whose tab id was lost or reused.
	if err := Focus("", title); err != nil {
		t.Errorf("Focus by title alone: %v", err)
	}
	// And something that matches nothing must say so, not error.
	if err := Focus("tab-nonexistent", "no such orbit tab title"); err != ErrNotFound {
		t.Errorf("Focus on nothing = %v, want ErrNotFound", err)
	}
}

// The window route goes through `new window` in the dictionary, which could
// plausibly share the quirk `new tab` has without a target window (created
// but not returned, -1708) — this pins down that it actually returns the tab.
func TestLiveGhosttyWindow(t *testing.T) {
	if os.Getenv("ORBIT_TERM_LIVE") != "1" {
		t.Skip("set ORBIT_TERM_LIVE=1 to run the live terminal test")
	}
	if detect() != ghosttyAPI {
		t.Skip("needs to run inside Ghostty 1.3+")
	}

	id, err := ghosttyOpenWindow([]string{"sh", "-c", "sleep 30"})
	if err != nil {
		t.Fatalf("ghosttyOpenWindow: %v", err)
	}
	if id == "" {
		t.Fatal("no tab id back from the window route")
	}
	t.Logf("opened window, tab %s", id)
	out, err := osascript(fmt.Sprintf(`
tell application "Ghostty"
	repeat with w in windows
		repeat with tb in tabs of w
			if id of tb is %s then
				close tab tb
				return "closed"
			end if
		end repeat
	end repeat
	return "missing"
end tell`, asString(id)))
	if err != nil || out != "closed" {
		t.Fatalf("cleanup close = %q, %v — the returned id did not resolve", out, err)
	}
}

// Package test holds integration tests: ones that touch the real system —
// spawning a tmux server, reading the actual session stores — rather than
// exercising a single package in isolation. Unit tests live beside the code
// they cover, where they can reach unexported internals.
package test

import (
	"github.com/sadrig91/orbit/internal/tmux"

	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Exercises the real tmux plumbing on the orbit Socket: config load, session
// creation, the @orbit_* option round-trip, pane capture and teardown. Uses a
// harmless echo rather than starting an actual agent.
func TestTmuxRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if err := tmux.InstallConf(); err != nil {
		t.Fatalf("installConf: %v", err)
	}
	if _, err := os.Stat(tmux.ConfPath()); err != nil {
		t.Fatalf("config not written: %v", err)
	}

	const name = "orbit-selftest"
	cwd, _ := os.Getwd()
	tmux.KillServerForTest() // in case a previous run leaked
	defer tmux.Kill(name)

	if err := tmux.Spawn(name, cwd, "echo ORBIT_SELFTEST_MARKER", "selftest · title", "codex", "sess-123"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	var got *tmux.Session
	for i := 0; i < 40 && got == nil; i++ {
		for _, s := range tmux.List() {
			if s.Name == name {
				got = s
			}
		}
		if got == nil {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if got == nil {
		t.Fatal("session not found in List")
	}
	if got.SessionID != "sess-123" {
		t.Errorf("@orbit_session = %q, want sess-123", got.SessionID)
	}
	if got.Agent != "codex" {
		t.Errorf("@orbit_agent = %v, want codex", got.Agent)
	}
	if got.Title != "selftest · title" {
		t.Errorf("@orbit_title = %q", got.Title)
	}
	if got.Cwd != cwd {
		t.Errorf("session_path = %q, want %q", got.Cwd, cwd)
	}
	if got.Created.IsZero() {
		t.Error("session_created not parsed")
	}
	// echo exits immediately, so the pane is back at a shell — which is exactly
	// the signal ShellOnly relies on.
	if got.AgentRunning {
		t.Errorf("expected a shell after echo exits, got pane command running")
	}

	var pane string
	for i := 0; i < 40; i++ {
		if pane = tmux.Capture(name, 40); strings.Contains(pane, "ORBIT_SELFTEST_MARKER") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(pane, "ORBIT_SELFTEST_MARKER") {
		t.Errorf("capture-pane missing command output:\n%s", pane)
	}

	tmux.Retitle(name, "renamed")
	for _, s := range tmux.List() {
		if s.Name == name && s.Title != "renamed" {
			t.Errorf("retitle failed, title = %q", s.Title)
		}
	}

	if err := tmux.Kill(name); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	for _, s := range tmux.List() {
		if s.Name == name {
			t.Error("session survived kill")
		}
	}
}

func TestTmuxConfIsValid(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if err := tmux.InstallConf(); err != nil {
		t.Fatal(err)
	}
	// A bad option makes tmux write to stderr while still exiting 0, so a clean
	// server start is the real signal that the shipped config parses.
	if err := tmux.KillServerForTest(); err != nil {
		_ = err // no server running is fine
	}
	if _, err := os.Stat(tmux.ConfPath()); err != nil {
		t.Fatalf("config not installed: %v", err)
	}
	if err := tmux.Spawn("orbit-conf-check", t.TempDir(), "true", "check", "claude", ""); err != nil {
		t.Errorf("tmux rejected the shipped config: %v", err)
	}
	tmux.Kill("orbit-conf-check")
}

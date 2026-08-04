package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Exercises the real tmux plumbing on the orbit socket: config load, session
// creation, the @orbit_* option round-trip, pane capture and teardown. Uses a
// harmless echo rather than starting an actual agent.
func TestTmuxRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if err := installConf(); err != nil {
		t.Fatalf("installConf: %v", err)
	}
	if _, err := os.Stat(confPath()); err != nil {
		t.Fatalf("config not written: %v", err)
	}

	const name = "orbit-selftest"
	cwd, _ := os.Getwd()
	tmuxCmd("kill-session", "-t", name).Run() // in case a previous run leaked
	defer tmuxCmd("kill-session", "-t", name).Run()

	if err := tmuxSpawn(name, cwd, "echo ORBIT_SELFTEST_MARKER", "selftest · title", Codex, "sess-123"); err != nil {
		t.Fatalf("tmuxSpawn: %v", err)
	}

	var got *Tmux
	for i := 0; i < 40 && got == nil; i++ {
		for _, s := range TmuxList() {
			if s.Name == name {
				got = s
			}
		}
		if got == nil {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if got == nil {
		t.Fatal("session not found in TmuxList")
	}
	if got.SessionID != "sess-123" {
		t.Errorf("@orbit_session = %q, want sess-123", got.SessionID)
	}
	if got.Agent != Codex {
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
		if pane = TmuxCapture(name, 40); strings.Contains(pane, "ORBIT_SELFTEST_MARKER") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(pane, "ORBIT_SELFTEST_MARKER") {
		t.Errorf("capture-pane missing command output:\n%s", pane)
	}

	TmuxRetitle(name, "renamed")
	for _, s := range TmuxList() {
		if s.Name == name && s.Title != "renamed" {
			t.Errorf("retitle failed, title = %q", s.Title)
		}
	}

	if err := TmuxKill(name); err != nil {
		t.Fatalf("TmuxKill: %v", err)
	}
	for _, s := range TmuxList() {
		if s.Name == name {
			t.Error("session survived kill")
		}
	}
}

func TestTmuxConfIsValid(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if err := installConf(); err != nil {
		t.Fatal(err)
	}
	// A bad option makes tmux write to stderr while still exiting 0, so check output.
	out, err := tmuxCmd("-C", "kill-server").CombinedOutput()
	if err == nil && len(out) > 0 && strings.Contains(string(out), "error") {
		t.Errorf("tmux rejected the config: %s", out)
	}
}

package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadrig91/orbit/internal/tmux"
)

// Agents rewrite old transcripts in batches, so mtime is not the session's age.
// This is the guard against sliding back to it.
func TestEventTimePrefersRecordedTimestamp(t *testing.T) {
	mtime := time.Now()
	real := "2026-07-14T09:30:00.000Z"

	got := EventTime(real, mtime)
	want, _ := time.Parse(time.RFC3339, real)
	if !got.Equal(want) {
		t.Errorf("got %v, want the recorded timestamp %v", got, want)
	}
	if time.Since(got) < 24*time.Hour {
		t.Error("a three-week-old session must not look recent")
	}
	if got := EventTime("", mtime); !got.Equal(mtime) {
		t.Errorf("with no timestamp it should fall back to mtime, got %v", got)
	}
	if got := EventTime("not a date", mtime); !got.Equal(mtime) {
		t.Errorf("an unparseable timestamp should fall back to mtime, got %v", got)
	}
	// Codex writes fractional seconds; Claude sometimes doesn't.
	for _, s := range []string{"2026-07-14T09:30:00Z", "2026-07-14T09:30:00.074Z"} {
		if EventTime(s, mtime).Equal(mtime) {
			t.Errorf("failed to parse %q", s)
		}
	}
}

// Claude Code appends bare `system` records to transcripts long after the
// conversation ended. Only real turns may date a session — this is the second
// layer of the same trap that mtime was the first layer of.
func TestOnlyConversationTurnsDateTheSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	lines := []string{
		`{"type":"user","timestamp":"2026-07-30T09:29:26.000Z","cwd":"/tmp/proj","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","timestamp":"2026-07-30T09:29:50.000Z","cwd":"/tmp/proj","message":{"role":"assistant","content":[{"type":"text"}]}}`,
		// housekeeping written five days later — must not count
		`{"type":"system","timestamp":"2026-08-04T14:28:41.000Z","cwd":"/tmp/proj"}`,
		// a subagent turn, also not the user's conversation
		`{"type":"assistant","timestamp":"2026-08-04T15:00:00.000Z","isSidechain":true,"cwd":"/tmp/proj","message":{"role":"assistant","content":[{"type":"text"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := parseClaude(path, time.Now()) // mtime is "now" and must be ignored
	if s == nil {
		t.Fatal("transcript did not parse")
	}
	want, _ := time.Parse(time.RFC3339, "2026-07-30T09:29:50.000Z")
	if !s.Modified.Equal(want) {
		t.Errorf("dated %v, want the last real turn %v", s.Modified, want)
	}
}

func TestSortModesOrderCorrectly(t *testing.T) {
	now := time.Now()
	mk := func(id string, ag Agent, cwd string, tok int64, ago time.Duration) *Session {
		return &Session{ID: id, Agent: ag, Cwd: cwd, Tokens: tok, Modified: now.Add(-ago)}
	}
	ss := []*Session{
		mk("a", Claude, "/z", 10, time.Hour),
		mk("b", Codex, "/a", 900, 48*time.Hour),
		mk("c", Copilot, "/m", 50, time.Minute),
	}
	SortSessionsBy(ss, SortTokens)
	if ss[0].ID != "b" {
		t.Errorf("by tokens: got %s first, want b", ss[0].ID)
	}
	SortSessionsBy(ss, SortProject)
	if ss[0].Cwd != "/a" {
		t.Errorf("by project: got %s first", ss[0].Cwd)
	}
	SortSessionsBy(ss, SortAge)
	if ss[0].ID != "c" {
		t.Errorf("by age: got %s first, want the newest (c)", ss[0].ID)
	}

	// A live session outranks everything regardless of sort, or the one thing
	// demanding attention could sort to the bottom.
	ss[0].Tmux, ss[0].State = &tmux.Session{AgentRunning: true}, NeedsApproval
	live := ss[0].ID
	SortSessionsBy(ss, SortTokens)
	if ss[0].ID != live {
		t.Errorf("a session needing attention must stay pinned to the top")
	}
}

// Summarising runs an agent CLI, and those CLIs record the run as a session in
// their working directory. orbit gives them a directory of its own so those
// phantom conversations don't land in a real project — and they must not reach
// the dashboard either, or you can summarise a summary.
func TestScanDropsOrbitsOwnSessions(t *testing.T) {
	real1 := &Session{ID: "a", Cwd: "/home/u/work/api", Title: "Refactor the runner"}
	phantom := &Session{ID: "b", Cwd: ScratchDir(), Title: "8;45;51hhello"}
	real2 := &Session{ID: "c", Cwd: "/home/u/work/docs", Title: "Fix the changelog"}

	got := snapshot([]*Session{real1, phantom, real2})

	if len(got) != 2 {
		t.Fatalf("snapshot kept %d sessions, want 2", len(got))
	}
	for _, s := range got {
		if s.Cwd == ScratchDir() {
			t.Errorf("a session from orbit's scratch directory reached the dashboard: %+v", s)
		}
	}
	if got[0].ID != "a" || got[1].ID != "c" {
		t.Errorf("kept %q and %q, want a and c", got[0].ID, got[1].ID)
	}
}

// The copies are what let the UI write Tmux and State onto what it got while
// the next scan is already running.
func TestSnapshotCopies(t *testing.T) {
	orig := &Session{ID: "a", Cwd: "/home/u/work/api"}
	got := snapshot([]*Session{orig})
	if len(got) != 1 {
		t.Fatalf("snapshot returned %d sessions", len(got))
	}
	if got[0] == orig {
		t.Error("snapshot handed back the cached value instead of a copy")
	}
	got[0].State = NeedsApproval
	if orig.State == NeedsApproval {
		t.Error("writing to the snapshot mutated the cached session")
	}
}

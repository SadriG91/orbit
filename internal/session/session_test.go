package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadrig91/orbit/internal/dispatch"
	"github.com/sadrig91/orbit/internal/hooks"
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

func plantHook(t *testing.T, agent, id string, status hooks.Status, at time.Time) {
	t.Helper()
	plant(t, agent, id, hooks.State{Status: status, Event: "test", At: at})
}

func plant(t *testing.T, agent, id string, st hooks.State) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".cache", "orbit", "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(dir, agent+"-"+id+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func liveSession(ag Agent, hint Hint, modified time.Time) *Session {
	return &Session{
		Agent: ag, ID: "hooked-1", hint: hint, Modified: modified,
		Tmux: &tmux.Session{Name: "x", AgentRunning: true},
	}
}

// The 1-in-9 fix. A tool that runs past 12 seconds used to read as "needs
// you" for its whole duration, because a running tool and a parked prompt
// write the same nothing to the transcript. The hook knows the difference: a
// prompt would have fired PermissionRequest, so a Working entry during a
// long tool call means exactly what it says.
func TestResolveHookWorkingOutlivesTheTwelveSecondGuess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	s := liveSession(Claude, HintMaybeApproval, now.Add(-90*time.Second))
	plantHook(t, "claude", s.ID, hooks.Working, now.Add(-90*time.Second))

	s.Resolve(now)
	if s.State != Working {
		t.Errorf("state = %v, want Working — the hook said so and no prompt event followed", s.State)
	}

	// Without the hook, the same session reads as needing you: the old guess.
	bare := liveSession(Claude, HintMaybeApproval, now.Add(-90*time.Second))
	bare.ID = "unhooked-1"
	bare.Resolve(now)
	if bare.State != NeedsApproval {
		t.Fatalf("the inference fallback changed underneath this test: %v", bare.State)
	}
}

// And the converse: a prompt reads as needing you the moment it exists, not
// twelve seconds later.
func TestResolveHookNeedsYouIsImmediate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	s := liveSession(Claude, HintMaybeApproval, now.Add(-1*time.Second))
	plantHook(t, "claude", s.ID, hooks.NeedsYou, now)

	s.Resolve(now)
	if s.State != NeedsApproval {
		t.Errorf("state = %v, want NeedsApproval with zero wait", s.State)
	}
}

func TestResolveHookYourTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	s := liveSession(Claude, HintBusy, now)
	plantHook(t, "claude", s.ID, hooks.YourTurn, now)
	s.Resolve(now)
	if s.State != YourTurn {
		t.Errorf("state = %v, want YourTurn", s.State)
	}
}

// A session resumed by hand carries no hooks, but its old state file is still
// there, claiming whatever was true last run. The transcript moving on past
// the file is how that gets noticed.
func TestResolveIgnoresAStateFileTheTranscriptHasOutrun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	s := liveSession(Claude, HintDone, now)
	plantHook(t, "claude", s.ID, hooks.NeedsYou, now.Add(-10*time.Minute))

	s.Resolve(now)
	if s.State != YourTurn {
		t.Errorf("state = %v, want the transcript's YourTurn — the hook state is from a previous run", s.State)
	}
}

// Copilot has no approval event, so its Working entries only mean "a tool
// started" — which is the ambiguous state. Fresh they are fine; sitting, they
// must hand back to the stillness inference or a parked approval would read
// as Working forever.
func TestResolveCopilotWorkingGoesSoft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	fresh := liveSession(Copilot, HintMaybeApproval, now.Add(-5*time.Second))
	plant(t, "copilot", fresh.ID, hooks.State{Status: hooks.Working, Soft: true, At: now.Add(-5 * time.Second)})
	fresh.Resolve(now)
	if fresh.State != Working {
		t.Errorf("fresh copilot working = %v, want Working", fresh.State)
	}

	sitting := liveSession(Copilot, HintMaybeApproval, now.Add(-30*time.Second))
	sitting.ID = "hooked-2"
	plant(t, "copilot", sitting.ID, hooks.State{Status: hooks.Working, Soft: true, At: now.Add(-30 * time.Second)})
	sitting.Resolve(now)
	if sitting.State != NeedsApproval {
		t.Errorf("sitting copilot working = %v, want the inference to take over", sitting.State)
	}
}

// Hook state speaks only for a running agent. A dead pane is a dead pane.
func TestResolveHookStateNeverRevivesADeadAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	s := liveSession(Claude, HintBusy, now)
	s.Tmux.AgentRunning = false
	plantHook(t, "claude", s.ID, hooks.NeedsYou, now)

	s.Resolve(now)
	if s.State != ShellOnly {
		t.Errorf("state = %v, want ShellOnly regardless of hook state", s.State)
	}
}

// A lost Stop event must not pin "working" forever. The transcript's HintDone
// means a finished turn was written — a definite fact that contradicts the
// file — so the inference takes over, which is the self-correction the old
// path always had.
func TestResolveLostStopFallsBackToTheTranscript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	s := liveSession(Claude, HintDone, now.Add(-10*time.Second))
	plantHook(t, "claude", s.ID, hooks.Working, now.Add(-15*time.Second))

	s.Resolve(now)
	if s.State != YourTurn {
		t.Errorf("state = %v, want YourTurn — the transcript finished the turn the hook missed", s.State)
	}
}

// HintApproval is codex's explicit approval marker in the transcript; a hook
// Working claim (codex's PermissionRequest is unverified) must not override
// something that definite.
func TestResolveHintApprovalBeatsHookWorking(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	s := liveSession(Codex, HintApproval, now.Add(-5*time.Second))
	plantHook(t, "codex", s.ID, hooks.Working, now.Add(-5*time.Second))

	s.Resolve(now)
	if s.State != NeedsApproval {
		t.Errorf("state = %v, want NeedsApproval from the transcript's definite marker", s.State)
	}
}

// The softness judgement travels in the State — Resolve must not need to
// know which agent wrote it. A soft claude entry would decay identically.
func TestResolveSoftDecayIsAgentBlind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	s := liveSession(Claude, HintMaybeApproval, now.Add(-30*time.Second))
	plant(t, "claude", s.ID, hooks.State{Status: hooks.Working, Soft: true, At: now.Add(-30 * time.Second)})

	s.Resolve(now)
	if s.State != NeedsApproval {
		t.Errorf("state = %v — a sitting soft Working should hand back to inference regardless of agent", s.State)
	}
}

// A dispatch is not inference. orbit started the process and read the CLI's
// own event stream, so its record outranks both the hook state files and the
// transcript — and, unlike either, it has to be believed on a session with no
// tmux at all, because a finished dispatch takes its pane with it.

func dispatched(status dispatch.Status, ended time.Time) *dispatch.Record {
	return &dispatch.Record{
		ID: "d1", Agent: "claude", SessionID: "disp-1",
		Status: status, Ended: ended,
	}
}

func TestResolveDispatchStates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	tests := []struct {
		name   string
		status dispatch.Status
		tmux   bool
		want   State
	}{
		{"running with its runner alive", dispatch.Running, true, Working},
		{"stopped at an approval", dispatch.NeedsYou, false, NeedsApproval},
		// A dispatch that stopped is exactly the thing worth walking over to,
		// and ▲ is how orbit says so — including the desktop notification.
		{"failed", dispatch.Failed, false, NeedsApproval},
		{"finished", dispatch.Done, false, YourTurn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{Agent: Claude, ID: "disp-1", hint: HintBusy, Modified: now}
			s.Dispatch = dispatched(tt.status, now)
			if tt.tmux {
				s.Tmux = &tmux.Session{Name: "x", AgentRunning: true}
			}
			s.Resolve(now)
			if s.State != tt.want {
				t.Errorf("state = %v, want %v", s.State, tt.want)
			}
		})
	}
}

// The trap the hooks work already fell into once: killing a session SIGHUPs
// whatever was inside it, so the runner never gets to write its own ending. A
// record left saying "running" would spin a dot forever.
func TestResolveDistrustsARunningDispatchWithNoRunner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	s := &Session{Agent: Claude, ID: "disp-1", hint: HintDone, Modified: now}
	s.Dispatch = dispatched(dispatch.Running, time.Time{})
	s.Resolve(now)
	if s.State != Dormant {
		t.Errorf("state = %v, want Dormant — the runner lives in tmux and there is none", s.State)
	}
}

// Resuming a dispatched session interactively is the intended next step. The
// moment someone types into it, the record is describing a previous chapter.
func TestResolveDropsADispatchTheConversationHasMovedPast(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	ended := now.Add(-10 * time.Minute)

	s := &Session{Agent: Claude, ID: "disp-1", hint: HintBusy, Modified: now}
	s.Dispatch = dispatched(dispatch.NeedsYou, ended)
	s.Tmux = &tmux.Session{Name: "x", AgentRunning: true}
	s.Resolve(now)
	if s.State != Working {
		t.Errorf("state = %v, want Working from the transcript, not the stale record", s.State)
	}

	// Within the margin the record still speaks: the transcript's clock and
	// orbit's are not the same clock.
	fresh := &Session{Agent: Claude, ID: "disp-1", hint: HintBusy, Modified: now}
	fresh.Dispatch = dispatched(dispatch.NeedsYou, now.Add(-5*time.Second))
	fresh.Tmux = &tmux.Session{Name: "x", AgentRunning: true}
	fresh.Resolve(now)
	if fresh.State != NeedsApproval {
		t.Errorf("state = %v, want NeedsApproval — the record only just finished", fresh.State)
	}
}

// A finished dispatch has no tmux, and Dormant is what the tmux checks would
// have said. Getting this wrong would hide every completed dispatch behind a
// grey dot in the "not running" pile.
func TestResolveDispatchOutranksTheTmuxChecks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	s := &Session{Agent: Claude, ID: "disp-1", hint: HintBusy, Modified: now}
	s.Dispatch = dispatched(dispatch.Done, now)
	s.Resolve(now)
	if s.State != YourTurn {
		t.Errorf("state = %v, want YourTurn with no tmux at all", s.State)
	}
	if !s.Live() {
		t.Error("a finished dispatch should stay pinned to the top of the list")
	}
}

// And a session with no dispatch at all is untouched by any of this.
func TestResolveWithoutADispatchIsUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	s := liveSession(Claude, HintDone, now)
	s.Resolve(now)
	if s.State != YourTurn {
		t.Errorf("state = %v, want YourTurn from the transcript", s.State)
	}
}

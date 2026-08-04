package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The embedded default config is the single source of truth for defaults, so
// a typo in it would silently degrade every install.
func TestDefaultConfigIsUsable(t *testing.T) {
	cfg, err := LoadConfigDefaults()
	if err != nil {
		t.Fatalf("shipped config.toml does not parse: %v", err)
	}
	if cfg.RecentDays <= 0 {
		t.Error("recent_days must be positive or nothing shows")
	}
	if cfg.spawnDelay() <= 0 || cfg.tabDelay() <= 0 {
		t.Error("delays must parse as durations")
	}
	for _, a := range AllAgents {
		argv := cfg.Summary.For(a)
		if len(argv) == 0 {
			t.Errorf("%s: no summary command configured", a)
			continue
		}
		// The command must be the agent's own CLI, or summaries bill the wrong
		// provider — and must not be a shell string, which wouldn't exec.
		if !strings.Contains(argv[0], a.String()) {
			t.Errorf("%s: summary command is %q", a, argv[0])
		}
	}
	if cfg.sortMode() != SortAge {
		t.Errorf("default sort should be age, got %v", cfg.sortMode())
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
	sortSessionsBy(ss, SortTokens)
	if ss[0].ID != "b" {
		t.Errorf("by tokens: got %s first, want b", ss[0].ID)
	}
	sortSessionsBy(ss, SortProject)
	if ss[0].Cwd != "/a" {
		t.Errorf("by project: got %s first", ss[0].Cwd)
	}
	sortSessionsBy(ss, SortAge)
	if ss[0].ID != "c" {
		t.Errorf("by age: got %s first, want the newest (c)", ss[0].ID)
	}

	// A live session outranks everything regardless of sort, or the one thing
	// demanding attention could sort to the bottom.
	ss[0].Tmux, ss[0].State = &Tmux{AgentRunning: true}, NeedsApproval
	live := ss[0].ID
	sortSessionsBy(ss, SortTokens)
	if ss[0].ID != live {
		t.Errorf("a session needing attention must stay pinned to the top")
	}
}

func TestHumanTokens(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{{0, ""}, {-5, ""}, {940, "940"}, {12_400, "12k"}, {1_500_000, "1.5M"}, {664_500_000, "664M"}} {
		if got := humanTokens(c.in); got != c.want {
			t.Errorf("humanTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSearchFindsBodyTextNotJustTitles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	os.WriteFile(path, []byte(strings.Join([]string{
		`{"type":"user","timestamp":"2026-07-30T09:29:26.000Z","cwd":"/tmp/p","message":{"role":"user","content":"the ivanti tunnel keeps dropping"}}`,
		`{"type":"assistant","timestamp":"2026-07-30T09:29:50.000Z","cwd":"/tmp/p","message":{"role":"assistant","content":[{"type":"text","text":"check the scutil key"}]}}`,
	}, "\n")+"\n"), 0o644)

	s := parseClaude(path, time.Now())
	if s == nil {
		t.Fatal("did not parse")
	}
	s.Title = "Unrelated title"

	got := SearchTranscripts([]*Session{s}, "ivanti")
	if len(got) != 1 {
		t.Fatalf("expected a body-text match, got %d", len(got))
	}
	if !strings.Contains(strings.ToLower(got[s.ID].Snippet), "ivanti") {
		t.Errorf("snippet missing the term: %q", got[s.ID].Snippet)
	}
	if len(SearchTranscripts([]*Session{s}, "nonexistentterm")) != 0 {
		t.Error("matched something it shouldn't")
	}
	if len(SearchTranscripts([]*Session{s}, "a")) != 0 {
		t.Error("single-character queries should be ignored")
	}
}

func TestSummaryCacheKeyTracksSessionState(t *testing.T) {
	base := &Session{Agent: Claude, ID: "abc", Modified: time.Unix(1000, 0)}
	moved := &Session{Agent: Claude, ID: "abc", Modified: time.Unix(2000, 0)}
	if summaryFile(base) == summaryFile(moved) {
		t.Error("a continued session must not reuse the old summary")
	}
	same := &Session{Agent: Claude, ID: "abc", Modified: time.Unix(1000, 0)}
	if summaryFile(base) != summaryFile(same) {
		t.Error("unchanged session should hit the cache")
	}
	if summaryFile(base) == summaryFile(&Session{Agent: Codex, ID: "abc", Modified: time.Unix(1000, 0)}) {
		t.Error("different agents must not share a cache entry")
	}
}

// The provider CLIs report no progress, so the bar is elapsed-vs-estimate. It
// must never reach full while still working — a bar sitting at 100% reads as a
// hang — and the estimate must adapt rather than stay a hardcoded guess.
func TestSummaryProgressStaysHonest(t *testing.T) {
	m := newModel(testConfig(), attachInline)
	m.pending["x"] = time.Now().Add(-2 * time.Second)
	pct, elapsed, running := m.summaryProgress("x")
	if !running || elapsed < time.Second {
		t.Fatalf("expected a running job, got running=%v elapsed=%v", running, elapsed)
	}
	if pct <= 0 || pct >= 1 {
		t.Errorf("progress %.2f out of range", pct)
	}

	// Far past the estimate it must still be short of full.
	m.pending["x"] = time.Now().Add(-10 * time.Minute)
	if pct, _, _ := m.summaryProgress("x"); pct > 0.95 {
		t.Errorf("overrunning job showed %.2f, must cap below full", pct)
	}
	if _, _, running := m.summaryProgress("nosuch"); running {
		t.Error("reported progress for a job that isn't running")
	}
}

// Cheap models have the smallest context windows, so the excerpt budget is the
// setting that keeps a huge session from overflowing one.
func TestSummaryInputBudgetIsPerProvider(t *testing.T) {
	cfg, _ := LoadConfigDefaults()
	for _, a := range AllAgents {
		if got := cfg.Summary.InputBudget(a); got < 1000 || got > 200_000 {
			t.Errorf("%s: budget %d is not a sane default", a, got)
		}
	}
	cfg.Summary.Codex.MaxInputChars = 4000
	if got := cfg.Summary.InputBudget(Codex); got != 4000 {
		t.Errorf("per-provider override ignored: got %d", got)
	}
	if cfg.Summary.InputBudget(Claude) == 4000 {
		t.Error("a codex override must not affect claude")
	}
	var empty Summary
	if got := empty.InputBudget(Claude); got <= 0 {
		t.Error("an unset budget must still resolve to something usable")
	}
}

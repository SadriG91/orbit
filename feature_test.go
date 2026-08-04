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

// The global bar measures work completed, not time passed: it advances only
// when a summary finishes, so it can never imply progress that hasn't happened.
func TestSummaryCoverageAdvancesOnCompletion(t *testing.T) {
	m := newModel(testConfig(), attachInline)
	m.w, m.h = 120, 40
	now := time.Now()
	for _, id := range []string{"a", "b", "c", "d"} {
		m.all = append(m.all, &Session{Agent: Claude, ID: id, Cwd: home("w", id),
			Title: "Session " + id, Modified: now})
	}
	m.rebuild()

	done, total, inflight := m.summaryCoverage()
	if done != 0 || total != 4 || inflight != 0 {
		t.Fatalf("fresh state: got %d/%d, %d in flight", done, total, inflight)
	}

	// Starting a job must NOT move the bar — only finishing one does.
	m.pending["a"] = now
	if d, _, f := m.summaryCoverage(); d != 0 || f != 1 {
		t.Errorf("a started job moved the bar: done=%d inflight=%d", d, f)
	}
	delete(m.pending, "a")
	m.summaries["a"] = "done"
	if d, _, _ := m.summaryCoverage(); d != 1 {
		t.Errorf("a completed job did not move the bar: done=%d", d)
	}

	// Queued work counts as in flight so the label is honest.
	m.queue = []string{"b", "c"}
	if _, _, f := m.summaryCoverage(); f != 2 {
		t.Errorf("queued jobs not counted: %d", f)
	}
	if bar := m.coverageBar(); !strings.Contains(stripANSI(bar), "1/4 summarised") {
		t.Errorf("bar label wrong: %q", stripANSI(bar))
	}
}

// Each job is a whole agent process, so they must not all start at once.
func TestSummariseAllRespectsConcurrencyLimit(t *testing.T) {
	m := newModel(testConfig(), attachInline)
	m.w, m.h = 120, 40
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		m.all = append(m.all, &Session{Agent: Claude, ID: id, Cwd: t.TempDir(),
			Title: "Session " + id, Modified: time.Now()})
	}
	m.rebuild()
	m.summariseAll()

	if len(m.pending) > maxSummaryJobs {
		t.Errorf("started %d jobs at once, limit is %d", len(m.pending), maxSummaryJobs)
	}
	if len(m.pending)+len(m.queue) != 5 {
		t.Errorf("expected all 5 accounted for, got %d running + %d queued", len(m.pending), len(m.queue))
	}
	// Re-queuing must not duplicate work already in hand.
	before := len(m.pending) + len(m.queue)
	m.summariseAll()
	if got := len(m.pending) + len(m.queue); got != before {
		t.Errorf("re-queued duplicates: %d -> %d", before, got)
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

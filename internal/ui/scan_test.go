package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sadrig91/orbit/internal/session"
)

// Bubble Tea runs batched commands concurrently, and the tick that starts a
// scan re-arms itself whether or not the previous scan has landed. Before
// scanCmd was single-flight, a cold start over a few hundred transcripts put
// two scans into Index.files at once — a fatal concurrent map write, not a
// benign race.
func TestScanIsSingleFlight(t *testing.T) {
	m := New(testConfig(), "inline")

	if first := m.scanCmd(); first == nil {
		t.Fatal("the first scan should have been issued")
	}
	if second := m.scanCmd(); second != nil {
		t.Error("a second scan started while one was still in flight")
	}

	m.Update(scanMsg{gen: m.scanGen}) // the scan lands
	if m.scanning {
		t.Error("scanMsg should clear the in-flight flag")
	}
	if next := m.scanCmd(); next == nil {
		t.Error("a scan should be issuable once the previous one landed")
	}
}

// Dropping a tick costs 2.5s of staleness and nobody notices. Dropping the
// refresh that follows a kill or an attach leaves the list visibly wrong, so
// those are remembered instead of discarded.
func TestRefreshDuringScanIsQueuedNotDropped(t *testing.T) {
	m := New(testConfig(), "inline")
	m.scanCmd() // something is now in flight

	if cmd := m.refreshCmd(); cmd != nil {
		t.Error("refresh must not start a scan alongside the running one")
	}
	if !m.rescan {
		t.Fatal("refresh during a scan should have been remembered")
	}

	m.Update(scanMsg{gen: m.scanGen})
	if m.rescan {
		t.Error("the queued refresh should have been consumed")
	}
	if !m.scanning {
		t.Error("the queued refresh should have reissued a scan")
	}
}

// The UI renders and sorts the sessions it was handed while the next scan is
// already running, so the index must not hand out the values it caches and
// reuses across scans.
func TestScanHandsOutCopies(t *testing.T) {
	fakeHome(t)
	ix := session.NewIndex()

	a, b := ix.Scan(), ix.Scan()
	if len(a) == 0 {
		t.Fatal("fixture transcript did not parse; the test would prove nothing")
	}
	if len(a) != len(b) {
		t.Fatalf("two scans disagreed on session count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] == b[i] {
			t.Fatalf("scan handed out the cached *Session for %s, which the caller mutates and renders", a[i].ID)
		}
	}
}

// The arrangement the guards exist for, driven the way Bubble Tea drives it:
// Update runs on one goroutine, the commands it returns run on others. Under
// -race this fails if the scan reads the sort mode off the model, or if the
// index hands out the sessions the UI is rendering.
func TestScanRunsSafelyAlongsideTheUI(t *testing.T) {
	fakeHome(t)
	m := New(testConfig(), "inline")
	m.w, m.h = 120, 30

	// Seed the model, so what the UI renders below is a previous scan's output.
	m.Update(scan(m.scanGen, session.NewIndex()))

	cmd := m.scanCmd()
	if cmd == nil {
		t.Fatal("scan was not issued")
	}
	landed := make(chan tea.Msg, 1)
	go func() { landed <- cmd() }()

	// Meanwhile the UI carries on: `o` rebinds the sort, and every frame reads
	// the sessions the scan is busy resolving state onto.
	for i := 0; i < 200; i++ {
		m.sort = session.AllSorts[i%len(session.AllSorts)]
		m.rebuild()
		_ = m.render()
	}
	m.Update(<-landed)
}

// fakeHome points the session stores at a temp directory holding one real
// Claude transcript, so scans have something to find.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".claude", "projects", "-tmp-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := `{"type":"user","timestamp":"2026-07-30T09:29:26.000Z","cwd":"/tmp/proj","message":{"role":"user","content":"hi"}}
{"type":"assistant","timestamp":"2026-07-30T09:29:50.000Z","cwd":"/tmp/proj","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}
{"type":"ai-title","timestamp":"2026-07-30T09:29:51.000Z","cwd":"/tmp/proj","aiTitle":"Refactor batch runner"}
`
	path := filepath.Join(dir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// Prune had no callers, so the summary cache grew a file per session forever.
// It now runs once, off the first scan — but never against an empty list, since
// a store that failed to read looks exactly like every session being deleted.
func TestPruneRunsOnceAndNeverOnAnEmptyList(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // never point a prune at a real cache
	m := New(testConfig(), "inline")

	if cmd := m.pruneCmd(); cmd != nil {
		t.Error("pruned against an empty session list; a failed store read would wipe the cache")
	}
	if m.pruned {
		t.Error("an empty list must not count as having pruned")
	}

	m.all = []*session.Session{{Agent: session.Claude, ID: "a", Cwd: "/tmp"}}
	if cmd := m.pruneCmd(); cmd == nil {
		t.Fatal("expected a prune once sessions were known")
	}
	if cmd := m.pruneCmd(); cmd != nil {
		t.Error("prune should run once per process, not on every scan")
	}
}

// The call site, not just the guard: a scan landing with sessions in it has to
// be what triggers the sweep. Prune spent this whole time being correct,
// tested-adjacent and simply never called.
func TestScanLandingTriggersThePrune(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // never point a prune at a real cache
	m := New(testConfig(), "inline")
	if !m.cfg.Summary.Enabled {
		t.Skip("summaries disabled in the shipped default")
	}

	m.Update(scanMsg{gen: m.scanGen, sessions: []*session.Session{
		{Agent: session.Claude, ID: "a", Cwd: "/tmp", Title: "t", Modified: time.Now()},
	}})
	if !m.pruned {
		t.Error("a scan landing with sessions should have swept the summary cache")
	}
}

// The scan no longer sorts; the result is ordered on arrival. Sorting at issue
// time meant a scan in flight carried a stale mode, and pressing `o` or `p`
// while one was running was silently undone for up to 2.5s when it landed —
// with grouping on, leaving the list out of project order under repeated
// group headers.
func TestSortChosenDuringAScanSurvivesItsArrival(t *testing.T) {
	m := New(testConfig(), "inline")
	m.w, m.h = 120, 30

	cmd := m.scanCmd() // issued while sorting by age
	if cmd == nil {
		t.Fatal("scan was not issued")
	}
	m.sort = session.SortTokens // `o`, while it is in flight

	now := time.Now()
	m.Update(scanMsg{gen: m.scanGen, sessions: []*session.Session{
		{Agent: session.Claude, ID: "small", Cwd: "/a", Title: "s", Tokens: 10, Modified: now},
		{Agent: session.Codex, ID: "big", Cwd: "/b", Title: "b", Tokens: 9000, Modified: now.Add(-time.Hour)},
	}})

	if len(m.view) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(m.view))
	}
	if m.view[0].ID != "big" {
		t.Errorf("landed sorted by %v, not the mode chosen during the scan: got %s first",
			m.sort, m.view[0].ID)
	}
}

// pruneCmd and the search command both read the session slice on another
// goroutine, while `o` and `p` sort m.all in place on the UI goroutine —
// swapping elements of the same backing array they are ranging over.
func TestCommandsDoNotShareTheSortedBackingArray(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := New(testConfig(), "inline")
	now := time.Now()
	m.all = []*session.Session{
		{Agent: session.Claude, ID: "a", Cwd: "/a", Title: "a", Tokens: 1, Modified: now},
		{Agent: session.Codex, ID: "b", Cwd: "/b", Title: "b", Tokens: 2, Modified: now},
		{Agent: session.Copilot, ID: "c", Cwd: "/c", Title: "c", Tokens: 3, Modified: now},
	}

	cmd := m.pruneCmd()
	if cmd == nil {
		t.Fatal("prune was not issued")
	}
	done := make(chan struct{})
	go func() { defer close(done); cmd() }()

	// `o`, repeatedly, against the array the command was handed.
	for i := 0; i < 500; i++ {
		session.SortSessionsBy(m.all, session.AllSorts[i%len(session.AllSorts)])
	}
	<-done
}

package ui

import (
	"os"
	"path/filepath"
	"testing"

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

	m.Update(scanMsg(nil)) // the scan lands
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

	m.Update(scanMsg(nil))
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
	m.Update(scan(session.NewIndex(), m.sort))

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

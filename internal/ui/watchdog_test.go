package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"
)

// scanning is only cleared when a result arrives, so anything that stops one
// arriving freezes the dashboard with no error and no way back.
func TestStuckScanRecovers(t *testing.T) {
	m := New(testConfig(), "inline", "dev")

	if cmd := m.scanCmd(); cmd == nil {
		t.Fatal("first scan should have been issued")
	}
	if !m.scanning {
		t.Fatal("expected a scan in flight")
	}
	// A scan that will never deliver.
	if cmd := m.scanCmd(); cmd != nil {
		t.Error("single-flight should have suppressed the second scan")
	}
	if cmd := m.recoverStuckScan(); cmd != nil {
		t.Error("a scan that has only just started must not be abandoned")
	}

	stuckGen, stuckIx := m.scanGen, m.ix
	m.scanStart = time.Now().Add(-scanStuck - time.Second)

	cmd := m.recoverStuckScan()
	if cmd == nil {
		t.Fatal("watchdog did not restart the scan")
	}
	if !m.scanning {
		t.Error("recovery should leave a fresh scan in flight")
	}

	// The abandoned scan must be disowned, not merely forgotten: reusing its
	// Index is the concurrent map write single-flight exists to prevent.
	if m.ix == stuckIx {
		t.Error("recovery reused the stuck scan's Index")
	}
	if m.scanGen == stuckGen {
		t.Error("generation did not move on, so the stale result would be accepted")
	}

	// And when the abandoned scan finally does land, it must be ignored.
	before := len(m.all)
	m.Update(scanMsg{gen: stuckGen, sessions: []*session.Session{
		{Agent: session.Claude, ID: "stale", Cwd: "/tmp", Title: "stale", Modified: time.Now()},
	}})
	if len(m.all) != before {
		t.Error("a result from the abandoned scan was accepted")
	}
	if !m.scanning {
		t.Error("the stale result cleared the flag guarding the live scan")
	}

	// The live one still lands normally.
	m.Update(scanMsg{gen: m.scanGen, sessions: []*session.Session{
		{Agent: session.Claude, ID: "fresh", Cwd: "/tmp", Title: "fresh", Modified: time.Now()},
	}})
	if m.scanning {
		t.Error("the current scan's result should have cleared the flag")
	}
	if len(m.all) != 1 || m.all[0].ID != "fresh" {
		t.Errorf("expected the live result, got %+v", m.all)
	}
}

// Recovery deliberately lets two scans overlap — the abandoned one keeps
// running. Index therefore has to be safe on its own rather than relying on
// callers to serialise.
//
// Scans a synthetic store rather than the developer's real one: this needs to
// exercise the cache read/write paths, not take twelve seconds and depend on
// whatever happens to be on the machine.
func TestIndexIsSafeUnderConcurrentScans(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, ".claude", "projects", "-tmp-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		line := `{"type":"user","timestamp":"2026-07-30T09:29:26.000Z","cwd":"/tmp/proj",` +
			`"message":{"role":"user","content":"hello ` + format.Itoa(i) + `"}}`
		name := fmt.Sprintf("aaaaaaaa-bbbb-cccc-dddd-%012d.jsonl", i)
		if err := os.WriteFile(filepath.Join(proj, name), []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ix := session.NewIndex()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); ix.Scan() }()
	}
	wg.Wait()

	if got := len(ix.Scan()); got != 12 {
		t.Errorf("scanned %d sessions, want 12", got)
	}
}

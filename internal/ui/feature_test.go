package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/sadrig91/orbit/internal/config"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"
	"github.com/sadrig91/orbit/internal/summary"
	"github.com/sadrig91/orbit/internal/tmux"
)

// The global bar measures work completed, not time passed: it advances only
// when a summary finishes, so it can never imply progress that hasn't happened.
func TestSummaryCoverageAdvancesOnCompletion(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 120, 40
	now := time.Now()
	for _, id := range []string{"a", "b", "c", "d"} {
		m.all = append(m.all, &session.Session{Agent: session.Claude, ID: id, Cwd: format.Home("w", id),
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
	m.summaries["a"] = summary.Record{Text: "done", CoveredMsgs: 99}
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
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 120, 40
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		m.all = append(m.all, &session.Session{Agent: session.Claude, ID: id, Cwd: t.TempDir(),
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

// Opening a session that's already showing in a Ghostty tab must focus that
// tab, not stack up a second one. tmux's attached flag is the ground truth;
// inline is the deliberate exception (a second client there just mirrors).
func TestAlreadyOpenDecidesWhenToFocus(t *testing.T) {
	detached := &session.Session{Tmux: &tmux.Session{Name: "cl-w-1"}}
	attached := &session.Session{Tmux: &tmux.Session{Name: "cl-w-1", Attached: true}}

	if alreadyOpen(detached, attachTab) {
		t.Error("a detached session has no tab to focus")
	}
	if alreadyOpen(&session.Session{}, attachTab) {
		t.Error("a session with no tmux at all cannot be open anywhere")
	}
	if !alreadyOpen(attached, attachTab) {
		t.Error("an attached session opened as a tab should focus, not reopen")
	}
	if !alreadyOpen(attached, attachWindow) {
		t.Error("an attached session opened as a window should focus, not reopen")
	}
	if alreadyOpen(attached, attachInline) {
		t.Error("inline attach mirrors on purpose and must not be redirected")
	}
}

// Updating ends with orbit replacing itself, so the sequence has to be
// visible on the way through and must only hand main a binary to exec when
// the install actually succeeded.
func TestUpdateSequence(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 120, 40

	// A failed update leaves this orbit running and says so — no relaunch.
	m.Update(updateFoundMsg{version: "v9.9.9"})
	if !m.updating {
		t.Error("the update is not marked in flight, so the spinner would stop")
	}
	if s := stripANSI(m.footer()); !strings.Contains(s, "updating orbit to v9.9.9") {
		t.Errorf("footer does not show the update: %q", s)
	}
	m.Update(updateDoneMsg{version: "v9.9.9", err: "brew upgrade: exit 1"})
	if m.updating {
		t.Error("still marked in flight after finishing")
	}
	if m.Relaunch() != "" {
		t.Error("a failed update must not trigger a restart")
	}
	if s := stripANSI(m.footer()); !strings.Contains(s, "failed") {
		t.Errorf("a failed update was not reported: %q", s)
	}

	// A successful one arms the relaunch and says what will happen.
	m.Update(updateFoundMsg{version: "v9.9.9"})
	m.Update(updateDoneMsg{version: "v9.9.9", exe: "/opt/homebrew/bin/orbit"})
	if got := m.Relaunch(); got != "/opt/homebrew/bin/orbit" {
		t.Errorf("Relaunch = %q, want the new binary", got)
	}
	if s := stripANSI(m.footer()); !strings.Contains(s, "restarting") {
		t.Errorf("the restart was not announced: %q", s)
	}
}

// The check must not fire when it's been turned off — a dashboard that phones
// home after being told not to is a bug people don't forgive.
func TestUpdateCheckRespectsConfig(t *testing.T) {
	cfg := testConfig()
	cfg.Update.Auto = false
	if cmd := newTestModel(cfg, attachInline).updateCheckCmd(); cmd != nil {
		t.Error("auto = false still scheduled a check")
	}

	cfg.Update.Auto = true
	t.Setenv("ORBIT_NO_UPDATE", "1")
	if cmd := newTestModel(cfg, attachInline).updateCheckCmd(); cmd != nil {
		t.Error("ORBIT_NO_UPDATE did not stop the check")
	}
	t.Setenv("ORBIT_NO_UPDATE", "")
	if cmd := newTestModel(cfg, attachInline).updateCheckCmd(); cmd == nil {
		t.Error("auto = true scheduled no check")
	}
}

// Automatic regeneration is the only path that spends money unprompted, so its
// guards matter more than the feature.
func TestAutoSummariseGuardsSpending(t *testing.T) {
	cfg, _ := config.LoadDefaults()
	cfg.Summary.Auto = true
	cfg.Summary.AutoMinNew = 8
	m := newTestModel(cfg, attachInline)

	s := &session.Session{Agent: session.Claude, ID: "a", Msgs: 20, Modified: time.Now()}
	if !m.shouldAutoSummarise(s) {
		t.Error("a session with no summary at all should be summarised")
	}

	m.summaries["a"] = summary.Record{Text: "x", CoveredMsgs: 20}
	if m.shouldAutoSummarise(s) {
		t.Error("a current summary must not be regenerated")
	}

	s.Msgs = 23 // three new turns
	if m.shouldAutoSummarise(s) {
		t.Error("regenerating after a few turns would bill per prompt")
	}
	s.Msgs = 28 // past the threshold
	if !m.shouldAutoSummarise(s) {
		t.Error("should refresh once far enough behind")
	}

	// Never mid-turn: the transcript is still being written.
	s.Tmux, s.State = &tmux.Session{AgentRunning: true}, session.Working
	if m.shouldAutoSummarise(s) {
		t.Error("must not summarise a session that is mid-turn")
	}
	s.State = session.NeedsApproval
	if m.shouldAutoSummarise(s) {
		t.Error("must not summarise a session sitting on a prompt")
	}

	// And not at all unless asked for.
	off, _ := config.LoadDefaults()
	if newTestModel(off, attachInline).shouldAutoSummarise(&session.Session{Agent: session.Claude, ID: "b"}) {
		t.Error("auto is off by default and must stay off")
	}
}

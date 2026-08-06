package ui

import (
	tea "charm.land/bubbletea/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sadrig91/orbit/internal/config"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/pane"
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
	if header := stripANSI(m.header()); strings.Contains(header, "summarised") {
		t.Errorf("summary progress still crowds the header: %q", header)
	}
	if footer := stripANSI(m.footer()); !strings.Contains(footer, "1/4 summarised") {
		t.Errorf("summary progress did not move to the footer: %q", footer)
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
	if s := stripANSI(m.header()); !strings.Contains(s, "failed") {
		t.Errorf("a failed update was not reported in the header: %q", s)
	}

	// A successful one arms the relaunch and says what will happen.
	m.Update(updateFoundMsg{version: "v9.9.9"})
	m.Update(updateDoneMsg{version: "v9.9.9", exe: "/opt/homebrew/bin/orbit"})
	if got := m.Relaunch(); got != "/opt/homebrew/bin/orbit" {
		t.Errorf("Relaunch = %q, want the new binary", got)
	}
	if s := stripANSI(m.header()); !strings.Contains(s, "restarting") {
		t.Errorf("the restart was not announced in the header: %q", s)
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

// fakePane stands in for a control client so the paths that matter — a
// connection that dies — can be exercised without a tmux server.
type fakePane struct {
	session  string
	text     string
	dirty    chan struct{}
	done     chan struct{}
	closed   bool
	render   string
	keys     []string
	texts    []string
	wheels   []fakeWheel
	scrolled bool
	w, h     int
}

type fakeWheel struct {
	x, y      int
	direction pane.WheelDirection
}

func newFakePane(session string) *fakePane {
	return &fakePane{
		session: session,
		text:    "live output",
		render:  "live output",
		dirty:   make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
}

func (f *fakePane) Session() string       { return f.session }
func (f *fakePane) Text() string          { return f.text }
func (f *fakePane) Render() string        { return f.render }
func (f *fakePane) Switch(s string) error { f.session = s; return nil }
func (f *fakePane) Resize(w, h int) error { f.w, f.h = w, h; return nil }
func (f *fakePane) SendKeyTo(_ string, key string) error {
	f.keys = append(f.keys, key)
	return nil
}
func (f *fakePane) SendTextTo(_ string, value string) error {
	f.texts = append(f.texts, value)
	return nil
}
func (f *fakePane) SendWheelTo(_ string, x, y int, direction pane.WheelDirection) error {
	f.wheels = append(f.wheels, fakeWheel{x: x, y: y, direction: direction})
	return nil
}
func (f *fakePane) Scrolled() bool { return f.scrolled }
func (f *fakePane) ScrollOffset() int {
	if f.scrolled {
		return 3
	}
	return 0
}
func (f *fakePane) FollowTail()            { f.scrolled = false }
func (f *fakePane) Dirty() <-chan struct{} { return f.dirty }
func (f *fakePane) Done() <-chan struct{}  { return f.done }
func (f *fakePane) Close() error           { f.closed = true; return nil }
func (f *fakePane) die()                   { close(f.done) }

// Before a stream is up, a poll is the only source of a preview and must fill
// the gap — capture-pane is issued alongside the dial precisely so the pane
// isn't blank while connecting.
func TestPollFillsTheGapBeforeTheStreamIsUp(t *testing.T) {
	m := New(config.Config{}, "", "test")
	m.Update(previewMsg{name: "cl-work-1", text: "polled"})
	if m.preview != "polled" {
		t.Errorf("preview = %q, want the poll to fill the gap", m.preview)
	}
}

// …but once the stream is up that same poll can still land, and painting a
// stale capture over live output would make the preview jump backwards in
// time. This is the guard the previous version of this test claimed to cover
// and did not.
func TestStalePollDoesNotOverwriteTheStream(t *testing.T) {
	m := New(config.Config{}, "", "test")
	f := newFakePane("cl-work-1")
	m.stream = f
	m.preview, m.previewName = "live output", "cl-work-1"

	m.Update(previewMsg{name: "cl-work-1", text: "stale poll"})
	if m.preview != "live output" {
		t.Errorf("preview = %q, want the stream to win", m.preview)
	}

	// A poll for a different session is not stale — it is the only source for
	// one the stream is not watching, so it must still land.
	m.Update(previewMsg{name: "cl-other", text: "polled"})
	if m.preview != "polled" {
		t.Errorf("preview = %q, want a poll for another session to land", m.preview)
	}
}

// Moving from a live row to a dormant transcript clears the cache but leaves
// the one streaming client where it is. Returning to that live row must copy
// its existing screen back immediately: an idle agent may never emit another
// Dirty event to do it for us.
func TestReturningToAlreadyWatchedSessionRestoresPreview(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	f := newFakePane("cl-live")
	f.text = "idle agent prompt"
	m.stream = f
	m.all = []*session.Session{
		{Agent: session.Claude, ID: "live", Cwd: "/work/live", Title: "live", Modified: time.Now(), State: session.YourTurn, Tmux: &tmux.Session{Name: "cl-live"}},
		{Agent: session.Claude, ID: "dormant", Cwd: "/work/old", Title: "dormant", Modified: time.Now().Add(-time.Hour)},
	}
	m.rebuild()
	m.preview, m.previewName = f.text, f.session

	_, clear := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if clear == nil {
		t.Fatal("moving to dormant row did not issue a preview clear")
	}
	m.Update(clear())
	if m.preview != "" {
		t.Fatalf("dormant row kept live preview %q", m.preview)
	}

	_, restore := m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if restore == nil {
		t.Fatal("returning to watched row did not request its snapshot")
	}
	msg := restore()
	if _, ok := msg.(paneSnapshotMsg); !ok {
		t.Fatalf("restore produced %T, want paneSnapshotMsg", msg)
	}
	m.Update(msg)
	if m.preview != f.text || m.previewName != f.session {
		t.Errorf("restored preview = %q/%q, want %q/%q", m.preview, m.previewName, f.text, f.session)
	}
}

// A control client that dies sends one last wakeup and then nothing. Waiting
// only on Dirty would block forever on a screen that cannot move, and because
// the Model still held a stream, capture would never fall back to polling —
// the preview frozen on its last frame for the rest of the session, silently.
func TestDeadStreamFallsBackToPolling(t *testing.T) {
	m := New(config.Config{}, "", "test")
	f := newFakePane("cl-work-1")
	m.stream = f
	m.view = []*session.Session{{Tmux: &tmux.Session{Name: "cl-work-1"}}}
	m.cursor = 0

	// The wait has to notice death rather than hanging on Dirty.
	f.die()
	msg := waitForPaneCmd(f)()
	if _, ok := msg.(paneGoneMsg); !ok {
		t.Fatalf("a dead pane produced %T, want paneGoneMsg", msg)
	}

	m.Update(msg)
	if m.stream != nil {
		t.Error("the dead stream was kept")
	}
	if !f.closed {
		t.Error("the dead stream was dropped without being closed")
	}
	// The last screen it managed stays up rather than the pane going blank.
	if m.preview != "live output" {
		t.Errorf("preview = %q, want the last frame kept", m.preview)
	}
	// And polling resumes, which it could not while a stream was held.
	cmd := m.capture()
	if cmd == nil {
		t.Fatal("no capture after the stream died")
	}
	if _, ok := cmd().(previewMsg); !ok && !m.streamOpening {
		t.Error("capture neither polled nor redialled after the stream died")
	}
}

// A live pane must not be mistaken for a dead one.
func TestLiveStreamKeepsWaiting(t *testing.T) {
	f := newFakePane("cl-work-1")
	f.dirty <- struct{}{}
	if _, ok := waitForPaneCmd(f)().(paneDirtyMsg); !ok {
		t.Error("a live pane did not report a screen change")
	}
}

// Failing to open the control client is a capability difference, not a fault:
// the dashboard has to carry on polling, and must not retry on every keypress.
func TestStreamFailureLatchesToPolling(t *testing.T) {
	m := New(config.Config{}, "", "test")
	if m.streamOff {
		t.Fatal("streaming starts disabled")
	}
	m.Update(paneOpenMsg{err: "no pty available"})
	if !m.streamOff {
		t.Error("a failed open did not latch streaming off")
	}
	if m.stream != nil {
		t.Error("a failed open left a stream behind")
	}
	if !strings.Contains(m.status, "polling instead") {
		t.Errorf("status = %q, want it to explain the fallback", m.status)
	}
}

// With streaming off, capture must go back to the polling path rather than
// returning nothing — otherwise the preview pane goes permanently blank.
func TestCaptureFallsBackToPollingWhenStreamingIsOff(t *testing.T) {
	m := New(config.Config{}, "", "test")
	m.streamOff = true
	m.view = []*session.Session{{Tmux: &tmux.Session{Name: "cl-work-1"}}}
	m.cursor = 0

	cmd := m.capture()
	if cmd == nil {
		t.Fatal("capture returned no command with streaming off")
	}
	msg := cmd()
	pm, ok := msg.(previewMsg)
	if !ok {
		t.Fatalf("capture produced %T, want previewMsg", msg)
	}
	if pm.name != "cl-work-1" {
		t.Errorf("previewMsg.name = %q, want the selected session", pm.name)
	}
}

// Nothing selected must not panic or leave a stale preview on screen.
func TestCaptureWithNoSelection(t *testing.T) {
	m := New(config.Config{}, "", "test")
	msg := m.capture()()
	if pm, ok := msg.(previewMsg); !ok || pm.name != "" {
		t.Errorf("capture with no selection = %#v, want an empty previewMsg", msg)
	}
}

// capture runs on every tick, so a handshake slower than one tick must not
// start a second control client — that would leak a process and a pty, and
// leave two clients attached to the same tmux server.
func TestDialIsSingleFlighted(t *testing.T) {
	m := New(config.Config{}, "", "test")
	m.view = []*session.Session{{Tmux: &tmux.Session{Name: "cl-work-1"}}}
	m.cursor = 0

	if m.capture(); !m.streamOpening {
		t.Fatal("the first capture did not mark a dial in flight")
	}
	// A second capture while the first is still connecting must fall back to
	// polling rather than dialling again.
	msg := m.capture()()
	if _, ok := msg.(previewMsg); !ok {
		t.Errorf("capture during a dial produced %T, want a poll", msg)
	}

	// Once the dial resolves, the flag clears so a later failure can retry.
	m.Update(paneOpenMsg{err: "nope"})
	if m.streamOpening {
		t.Error("the in-flight flag survived the open result")
	}
}

// `o` and `p` are the only settings you can change from the keyboard, and the
// change used to be thrown away on quit: you found the arrangement you wanted
// and then had to reproduce it by hand in a file.
func TestSortAndGroupKeysPersist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "orbit", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("sort = \"age\"\ngroup = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(config.Config{Sort: "age"}, "", "test")
	runCmd(runKey(m, "o")) // the write happens off the UI goroutine
	if m.sort.String() == "age" {
		t.Fatal("`o` did not change the sort at all")
	}
	want := m.sort.String()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sort != want {
		t.Errorf("config says sort = %q, want %q", cfg.Sort, want)
	}

	runCmd(runKey(m, "p"))
	if cfg, err = config.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Group != (m.group != groupNone) {
		t.Errorf("config says group = %v, want mode %v", cfg.Group, m.group)
	}
	if cfg.GroupBy != m.group.String() {
		t.Errorf("config says group_by = %q, want %q", cfg.GroupBy, m.group.String())
	}
}

// A config that cannot be written must not swallow the keypress — the sort
// still changes on screen, and the failure is said rather than hidden.
func TestSortStillWorksWhenTheConfigCannotBeWritten(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no config file at all

	m := New(config.Config{Sort: "age"}, "", "test")
	before := m.sort
	cmd := runKey(m, "o")
	if m.sort == before {
		t.Fatal("the sort did not change when saving was impossible")
	}
	if cmd == nil {
		t.Fatal("no save was attempted")
	}
	msg := runCmd(cmd)
	sm, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("a failed save produced %T, want a statusMsg", msg)
	}
	if !strings.Contains(string(sm), "could not save") {
		t.Errorf("status = %q, want it to mention the failure", sm)
	}
}

// runKey feeds one keypress through Update and hands back the command it
// produced, so a test can run the side effect deliberately.
func runKey(m *Model, key string) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	return cmd
}

// runCmd executes a command the way the runtime would, unwrapping a batch
// rather than stopping at the BatchMsg it hands back. Returns the last
// non-batch message, which is enough for these tests.
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return msg
	}
	var last tea.Msg
	for _, sub := range batch {
		if got := runCmd(sub); got != nil {
			last = got
		}
	}
	return last
}

package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sadrig91/orbit/internal/pane"
	"github.com/sadrig91/orbit/internal/session"
	"github.com/sadrig91/orbit/internal/tmux"
)

func TestPendingSessionExistsBeforeTranscript(t *testing.T) {
	now := time.Now()
	s := pendingFromTmux(&tmux.Session{
		Name: "cx-work-orbit", Agent: "codex", AgentRunning: true,
		Cwd: "/work/orbit", Created: now, Activity: now,
	})
	if s == nil || !pendingSession(s) {
		t.Fatalf("pendingFromTmux = %#v, want a pending session", s)
	}
	if s.Agent != session.Codex || s.Tmux == nil || s.Tmux.Name != "cx-work-orbit" {
		t.Errorf("pending session lost its identity: %#v", s)
	}
}

func TestPendingSessionOnlyLinksANewTranscript(t *testing.T) {
	created := time.Now()
	oldButActive := &session.Session{
		Agent: session.Codex, ID: "already-here", Cwd: "/work/orbit",
		Modified: created.Add(time.Second),
	}
	newSession := &session.Session{
		Agent: session.Codex, ID: "genuinely-new", Cwd: "/work/orbit",
		Modified: created.Add(2 * time.Second),
	}
	tmuxSession := &tmux.Session{
		Name: "cx-work-orbit", Agent: "codex", Cwd: "/work/orbit",
		Created: created, Pending: true, Known: "already-here",
	}
	got := unlinkedCandidate(tmuxSession, []*session.Session{oldButActive, newSession}, map[string]bool{})
	if got != newSession {
		t.Errorf("linked %#v, want the transcript absent at launch", got)
	}

	tmuxSession.Known = ""
	if got := unlinkedCandidate(tmuxSession, []*session.Session{newSession}, map[string]bool{}); got != nil {
		t.Errorf("linked without a launch baseline: %#v", got)
	}
	// Two new sessions are genuinely ambiguous and must remain visible as a
	// pending row instead of falling back to recency.
	tmuxSession.Known = "already-here"
	otherNew := &session.Session{Agent: session.Codex, ID: "also-new", Cwd: "/work/orbit", Modified: created.Add(3 * time.Second)}
	if got := unlinkedCandidate(tmuxSession, []*session.Session{oldButActive, newSession, otherNew}, map[string]bool{}); got != nil {
		t.Errorf("guessed between two new transcripts: %#v", got)
	}
}

func TestStartedSessionStaysInOrbitAndOpensEmbedded(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 100, 30
	f := newFakePane("cl-work-orbit")
	m.stream = f

	_, cmd := m.Update(startedMsg{name: "cl-work-orbit", cwd: "/work/orbit", agent: session.Claude})
	if m.embedded != "cl-work-orbit" {
		t.Fatalf("embedded = %q, want the new tmux session", m.embedded)
	}
	if len(m.all) != 1 || !pendingSession(m.all[0]) {
		t.Fatalf("new session did not appear before its transcript: %#v", m.all)
	}
	if cmd == nil {
		t.Fatal("new session did not activate its embedded terminal")
	}
	msg := cmd()
	if ready, ok := msg.(paneReadyMsg); !ok || ready.err != "" {
		t.Fatalf("activation = %#v, want a successful paneReadyMsg", msg)
	}
	if f.w != 49 || f.h != 23 {
		t.Errorf("focused pane size = %dx%d, want right-pane content 49x23", f.w, f.h)
	}
}

func TestFreshSessionRevealsRowAndTerminalTogether(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 100, 30
	m.preparing = &drivePreparation{
		cwd: "/work/orbit", title: "new codex", agent: session.Codex, fresh: true, started: time.Now(),
	}
	f := newFakePane("another-session")
	f.render = "Codex agent ready\n› Ask anything"
	m.stream = f

	_, cmd := m.Update(startedMsg{name: "cx-work-orbit", cwd: "/work/orbit", agent: session.Codex})
	if cmd == nil || m.preparing == nil || m.preparing.name != "cx-work-orbit" {
		t.Fatalf("fresh session did not remain in preparation through pane activation: %#v", m.preparing)
	}
	if len(m.all) != 0 {
		t.Fatalf("fresh row appeared before its terminal was ready: %#v", m.all)
	}
	frame := stripANSI(m.render())
	if !strings.Contains(frame, "PREPARING CODEX SESSION") || strings.Contains(frame, "connecting to tmux") {
		t.Errorf("fresh startup exposed an intermediate state:\n%s", frame)
	}

	startedWaiting := time.Now()
	ready := cmd()
	if elapsed := time.Since(startedWaiting); elapsed < terminalFrameStable {
		t.Errorf("terminal revealed after %s, before a stable frame", elapsed)
	}
	m.Update(ready)
	if m.preparing != nil || len(m.all) != 1 || !pendingSession(m.all[0]) {
		t.Fatalf("ready fresh terminal was not revealed atomically: prep=%#v sessions=%#v", m.preparing, m.all)
	}
}

func TestScanDoesNotMutateDashboardDuringPreparation(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	original := m.all[0]
	m.preparing = &drivePreparation{id: original.ID, agent: original.Agent, started: time.Now()}
	m.scanning = true
	m.scanGen = 4
	live := &session.Session{Agent: session.Codex, ID: original.ID, Cwd: original.Cwd, Title: original.Title,
		Tmux: &tmux.Session{Name: "cx-work", AgentRunning: true}}

	m.Update(scanMsg{gen: 4, sessions: []*session.Session{live}})
	if m.all[0] != original || m.all[0].Tmux != nil {
		t.Error("scan changed the session row before the prepared terminal reveal")
	}
	if m.scanning {
		t.Error("discarded preparation scan left single-flight latched")
	}
}

func TestEmbeddedInputIsOrderedAndCtrlGEscapes(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 100, 30
	m.embedded = "cl-work-orbit"
	m.terminalFocused = true
	f := newFakePane(m.embedded)
	m.stream = f

	_, first := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	_, second := m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	if first == nil || second != nil {
		t.Fatalf("input serialization: first=%v second=%v", first != nil, second != nil)
	}
	_, next := m.Update(first())
	if next == nil {
		t.Fatal("second queued character was not pumped")
	}
	m.Update(next())
	if got := strings.Join(f.texts, ""); got != "ab" {
		t.Errorf("forwarded text = %q, want ab", got)
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter was not forwarded")
	}
	m.Update(cmd())
	if len(f.keys) != 1 || f.keys[0] != "Enter" {
		t.Errorf("forwarded keys = %q, want Enter", f.keys)
	}

	m.all = []*session.Session{{
		Agent: session.Claude, ID: pendingIDPrefix + m.embedded, Cwd: "/work/orbit",
		Tmux: &tmux.Session{Name: m.embedded},
	}}
	m.rebuild()
	f.scrolled = true
	m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if m.terminalFocused || m.embedded != "cl-work-orbit" {
		t.Error("Ctrl+g did not move focus to Sessions while keeping the terminal mounted")
	}
	if f.scrolled {
		t.Error("returning to Sessions left the terminal viewport in scrollback")
	}
	if len(f.keys) != 1 {
		t.Error("Ctrl+g leaked through to the agent")
	}
}

func TestTabSwitchesPaneAndShiftTabReachesAgent(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	m.all[0].Tmux = &tmux.Session{Name: "cx-work", AgentRunning: true}
	m.rebuild()
	f := newFakePane("cx-work")
	m.stream = f

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.embedded != "cx-work" || !m.terminalFocused || cmd == nil {
		t.Fatalf("Tab did not focus live pane: embedded=%q cmd=%v", m.embedded, cmd != nil)
	}
	m.Update(cmd())

	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if cmd == nil {
		t.Fatal("Shift+Tab was not forwarded to the agent")
	}
	m.Update(cmd())
	if len(f.keys) != 1 || f.keys[0] != "BTab" {
		t.Errorf("forwarded keys = %q, want BTab", f.keys)
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.terminalFocused || m.embedded != "cx-work" {
		t.Error("plain Tab did not focus Sessions while keeping the terminal mounted")
	}
	if frame := stripANSI(m.render()); !strings.Contains(frame, "TERMINAL · UNFOCUSED") || !strings.Contains(frame, "live output") {
		t.Errorf("unfocused terminal fell back to the broken detail preview:\n%s", frame)
	}
	if len(f.keys) != 1 {
		t.Error("plain Tab leaked through to the agent")
	}
}

func TestMovingToAnotherSessionReleasesTheUnfocusedTerminal(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	m.all[0].Tmux = &tmux.Session{Name: "cx-first", AgentRunning: true}
	m.all = append(m.all, &session.Session{
		Agent: session.Claude, ID: "second", Cwd: "/work/second", Title: "second", Modified: time.Now().Add(-time.Minute),
	})
	m.rebuild()
	m.embedded, m.embeddedName, m.embeddedCwd = "cx-first", "first", m.all[0].Cwd
	m.terminalFocused = false
	m.stream = newFakePane("cx-first")

	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.embedded != "" || m.cursor != 1 {
		t.Errorf("navigation retained the old terminal: embedded=%q cursor=%d", m.embedded, m.cursor)
	}
	frame := stripANSI(m.render())
	if !strings.Contains(frame, "second") || strings.Contains(frame, "TERMINAL · UNFOCUSED") {
		t.Errorf("right pane did not follow the new selection:\n%s", frame)
	}
}

func TestBrowsingALiveSessionUsesItsOwnTerminalPreview(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	m.all[0].Tmux = &tmux.Session{Name: "cx-first", AgentRunning: true}
	m.all = append(m.all, &session.Session{
		Agent: session.Claude, ID: "second", Cwd: "/work/second", Title: "second", Modified: time.Now().Add(-time.Minute),
		Tmux: &tmux.Session{Name: "cl-second", AgentRunning: true},
	})
	m.rebuild()
	f := newFakePane("cx-first")
	f.render = "first terminal"
	m.stream = f
	m.embedded, m.embeddedName, m.embeddedCwd = "cx-first", "first", m.all[0].Cwd

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if cmd == nil {
		t.Fatal("moving to a live row did not switch its preview")
	}
	f.render, f.text = "\x1b[32msecond terminal\x1b[0m", "second terminal"
	m.Update(cmd())
	frame := stripANSI(m.render())
	if !strings.Contains(frame, "PREVIEW · WORK/SECOND") || !strings.Contains(frame, "second terminal") {
		t.Errorf("selected live session did not render its own terminal:\n%s", frame)
	}
	if strings.Contains(frame, "first terminal") {
		t.Errorf("previous terminal leaked into the selected session preview:\n%s", frame)
	}
}

func TestUnfocusedTerminalDropsAgentColours(t *testing.T) {
	lines := mutedTerminalLines([]string{"\x1b[31magent prompt\x1b[0m", "plain"})
	if len(lines) != 2 || !strings.Contains(stripANSI(lines[0]), "agent prompt") {
		t.Fatalf("muted lines lost terminal content: %#v", lines)
	}
	if strings.Contains(lines[0], "\x1b[31m") {
		t.Errorf("muted terminal retained the agent's active red style: %q", lines[0])
	}
}

func TestFocusedAndScrolledTerminalUseExplicitLabels(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	m.w, m.h = 100, 30
	m.all[0].Tmux = &tmux.Session{Name: "cx-work", AgentRunning: true}
	m.rebuild()
	f := newFakePane("cx-work")
	m.stream = f
	m.embedded, m.embeddedName, m.embeddedCwd = "cx-work", "agent session", m.all[0].Cwd
	m.terminalFocused = true

	frame := stripANSI(m.render())
	for _, want := range []string{"▶ TERMINAL · FOCUSED"} {
		if !strings.Contains(frame, want) {
			t.Errorf("focused terminal missing %q:\n%s", want, frame)
		}
	}

	f.scrolled = true
	frame = stripANSI(m.render())
	if !strings.Contains(frame, "SCROLLBACK · 3 LINES UP") {
		t.Errorf("scrollback distance missing:\n%s", frame)
	}
}

func TestTabResumesADormantSessionForTheLivePane(t *testing.T) {
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := modelWithSession(t, session.Claude)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if cmd == nil {
		t.Fatal("Tab did not start resuming the dormant session")
	}
	if m.embedded != "" {
		t.Error("pane focused before the asynchronous resume was ready")
	}
	if m.preparing == nil || m.preparing.id != m.all[0].ID {
		t.Fatalf("preparation state = %#v, want the selected transcript", m.preparing)
	}
	frame := stripANSI(m.render())
	for _, want := range []string{"preparing", "PREPARING CLAUDE SESSION", "live terminal", "will appear when the agent is ready", "focus automatically"} {
		if !strings.Contains(frame, want) {
			t.Errorf("preparation frame missing %q:\n%s", want, frame)
		}
	}
	if _, duplicate := m.Update(tea.KeyPressMsg{Code: tea.KeyTab}); duplicate != nil {
		t.Error("a second Tab launched a duplicate resume")
	}
}

func TestResumedSessionBecomesLiveAndFocused(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	s := m.all[0]
	f := newFakePane("another-session")
	f.render = "Codex agent ready\n› Ask anything"
	m.stream = f
	m.preparing = &drivePreparation{id: s.ID, agent: s.Agent, cwd: s.Cwd, title: s.Name(), started: time.Now()}
	_, cmd := m.Update(driveReadyMsg{
		name: "cx-work-proj", cwd: s.Cwd, id: s.ID,
		title: s.Title, agent: s.Agent,
	})
	if m.embedded != "cx-work-proj" {
		t.Errorf("focused pane = %q, want resumed tmux session", m.embedded)
	}
	if s.Tmux != nil {
		t.Errorf("resumed row changed before the terminal was ready: %#v", s.Tmux)
	}
	if cmd == nil {
		t.Fatal("resumed session did not start activating its live stream")
	}
	if m.preparing == nil || m.preparing.name != "cx-work-proj" {
		t.Fatalf("preparation did not stay visible through pane connection: %#v", m.preparing)
	}
	if frame := stripANSI(m.render()); !strings.Contains(frame, "PREPARING CODEX SESSION") || strings.Contains(frame, "connecting to tmux") {
		t.Errorf("pane handoff exposed an intermediate terminal:\n%s", frame)
	}
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("dashboard mouse mode = %v, want wheel events captured during preparation", got)
	}

	ready := cmd()
	if msg, ok := ready.(paneReadyMsg); !ok || msg.err != "" {
		t.Fatalf("activation = %#v, want successful paneReadyMsg", ready)
	}
	m.Update(ready)
	if m.preparing != nil {
		t.Error("ready live pane left the preparation indicator active")
	}
	if s.Tmux == nil || s.Tmux.Name != "cx-work-proj" || !s.Tmux.AgentRunning {
		t.Errorf("ready pane did not atomically make the row live: %#v", s.Tmux)
	}
	if strings.Contains(m.status, "start or resume") {
		t.Errorf("old prerequisite error survived: %q", m.status)
	}
}

func TestFailedResumeClearsPreparation(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	s := m.all[0]
	m.preparing = &drivePreparation{id: s.ID, agent: s.Agent, started: time.Now()}
	m.Update(driveFailedMsg{id: s.ID, err: "tmux unavailable"})
	if m.preparing != nil {
		t.Error("failed resume left the preparation indicator active")
	}
	if !strings.Contains(m.status, "tmux unavailable") {
		t.Errorf("failure status = %q", m.status)
	}
}

func TestFocusedTerminalRendersInsideDashboard(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 80, 24
	m.embedded, m.embeddedName, m.embeddedCwd = "cx-work", "new codex", "/work"
	m.terminalFocused = true
	f := newFakePane(m.embedded)
	f.render = "codex terminal"
	m.stream = f

	out := m.render()
	plain := stripANSI(out)
	if !strings.Contains(plain, "TERMINAL · FOCUSED") || !strings.Contains(out, "codex terminal") || !strings.Contains(plain, "SESSIONS") {
		t.Errorf("dashboard missing its focused terminal or session pane:\n%s", plain)
	}
	if got := lipgloss.Height(out); got != 24 {
		t.Errorf("focused dashboard is %d rows, want 24", got)
	}
	if cursor := m.View().Cursor; cursor != nil {
		t.Errorf("Orbit exposed a second native cursor at %+v", cursor)
	}
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Errorf("focused terminal mouse mode = %v, want cell motion", got)
	}
}

func TestWheelOverFocusedTerminalScrollsAgent(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 100, 30
	m.embedded = "cx-work"
	m.terminalFocused = true
	f := newFakePane(m.embedded)
	m.stream = f

	listW, _, _ := m.dashboardPaneSizes()
	bodyTop := lipgloss.Height(m.header())
	_, cmd := m.Update(tea.MouseWheelMsg{X: listW + 3, Y: bodyTop + 1, Button: tea.MouseWheelUp})
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
	if len(f.wheels) != 1 || f.wheels[0] != (fakeWheel{direction: pane.WheelUp}) {
		t.Errorf("wheel up forwarded %#v, want one pane-local wheel event", f.wheels)
	}

	_, cmd = m.Update(tea.MouseWheelMsg{X: listW + 3, Y: bodyTop + 1, Button: tea.MouseWheelDown})
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
	if len(f.wheels) != 2 || f.wheels[1] != (fakeWheel{direction: pane.WheelDown}) {
		t.Errorf("wheel down forwarded %#v, want one pane-local wheel event", f.wheels)
	}

	_, cmd = m.Update(tea.MouseWheelMsg{X: 2, Y: bodyTop + 1, Button: tea.MouseWheelUp})
	if cmd != nil || len(f.wheels) != 2 {
		t.Error("wheel over the Sessions pane leaked into the agent")
	}
}

func TestWheelDoesNotBecomeSessionNavigationAfterTab(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	m.all = append(m.all, &session.Session{Agent: session.Claude, ID: "second", Cwd: "/work/second", Title: "second", Modified: time.Now()})
	m.rebuild()
	m.cursor = 0

	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("Sessions view mouse mode = %v, want cell motion", got)
	}
	_, cmd := m.Update(tea.MouseWheelMsg{X: 5, Y: 10, Button: tea.MouseWheelDown})
	if cmd != nil || m.cursor != 0 {
		t.Errorf("wheel leaked into Sessions: cmd=%v cursor=%d", cmd != nil, m.cursor)
	}
}

func TestFocusedTerminalCanResizeAndZoom(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 100, 30
	m.embedded, m.embeddedName, m.embeddedCwd = "cx-work", "new codex", "/work"
	m.terminalFocused = true
	f := newFakePane(m.embedded)
	f.render = "codex terminal"
	m.stream = f

	_, before, _ := m.dashboardPaneSizes()
	_, cmd := m.Update(tea.KeyPressMsg{Code: '+', Mod: tea.ModCtrl | tea.ModAlt})
	if m.detailWidth != before+detailResizeStep || cmd == nil {
		t.Fatal("Ctrl+Alt++ did not resize while the terminal owned input")
	}
	m.Update(cmd())

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	if !m.terminalZoom || cmd == nil {
		t.Fatal("Ctrl+F did not zoom and resize the terminal pane")
	}
	m.Update(cmd())
	if f.w != 100 || f.h != 28 {
		t.Errorf("zoomed pane = %dx%d, want 100x28", f.w, f.h)
	}
	plain := stripANSI(m.render())
	if !strings.Contains(plain, "TERMINAL FOCUSED") || strings.Contains(plain, "SESSIONS · RECENT") {
		t.Errorf("zoomed terminal did not own the body:\n%s", plain)
	}
	if got := lipgloss.Height(m.render()); got != 30 {
		t.Errorf("zoomed frame is %d rows, want 30", got)
	}
}

func TestDetailPaneCanWidenWhileSessionsOwnFocus(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 100, 30
	m.embedded, m.embeddedName, m.embeddedCwd = "cx-work", "codex", "/work"
	m.terminalFocused = false
	f := newFakePane(m.embedded)
	m.stream = f

	normalListW, normalDetailW, _ := m.dashboardPaneSizes()
	_, cmd := m.Update(tea.KeyPressMsg{Code: '+', Mod: tea.ModCtrl | tea.ModAlt})
	if m.detailWidth == 0 || cmd == nil {
		t.Fatal("+ did not widen the detail pane from Sessions focus")
	}
	wideListW, wideDetailW, _ := m.dashboardPaneSizes()
	if wideListW >= normalListW || wideDetailW <= normalDetailW {
		t.Fatalf("pane split did not change: normal=%d/%d wide=%d/%d", normalListW, normalDetailW, wideListW, wideDetailW)
	}
	m.Update(cmd())
	if f.w != wideDetailW-4 {
		t.Errorf("tmux pane width = %d, want rendered content width %d", f.w, wideDetailW-4)
	}
}

func TestDetailResizeKeysAndClamps(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 80, 24

	for _, tt := range []struct {
		code rune
		want int
	}{
		{'-', -detailResizeStep}, {'+', detailResizeStep},
	} {
		if got, ok := detailResizeDelta(tea.KeyPressMsg{Code: tt.code, Mod: tea.ModCtrl | tea.ModAlt}); !ok || got != tt.want {
			t.Errorf("%c = %d,%v; want %d,true", tt.code, got, ok, tt.want)
		}
	}
	if _, ok := detailResizeDelta(tea.KeyPressMsg{Code: '+', Mod: tea.ModCtrl}); ok {
		t.Error("Ctrl++ still owns the Ghostty font-size shortcut")
	}
	if got, ok := detailResizeDelta(tea.KeyPressMsg{Code: '=', Text: "+", Mod: tea.ModCtrl | tea.ModAlt | tea.ModShift}); !ok || got != detailResizeStep {
		t.Errorf("shifted + = %d,%v; want %d,true", got, ok, detailResizeStep)
	}

	for range 20 {
		m.adjustDetailWidth(detailResizeStep)
	}
	listW, detailW, _ := m.dashboardPaneSizes()
	if listW != minSessionPaneWidth || detailW != m.w-minSessionPaneWidth-1 {
		t.Errorf("wide clamp = %d/%d", listW, detailW)
	}
	for range 20 {
		m.adjustDetailWidth(-detailResizeStep)
	}
	listW, detailW, _ = m.dashboardPaneSizes()
	if detailW != minDetailPaneWidth || listW != m.w-minDetailPaneWidth-1 {
		t.Errorf("narrow clamp = %d/%d", listW, detailW)
	}
	assertFrameSize(t, m, m.render())
}

func TestPaneInputMapping(t *testing.T) {
	tests := []struct {
		msg  tea.KeyPressMsg
		text string
		key  string
	}{
		{tea.KeyPressMsg{Code: 'å', Text: "å"}, "å", ""},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, "", "Escape"},
		{tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, "", "BTab"},
		{tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, "", "C-c"},
		{tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}, "", "M-Left"},
	}
	for _, tt := range tests {
		got, ok := paneInputFor(tt.msg)
		if !ok || got.text != tt.text || got.key != tt.key {
			t.Errorf("paneInputFor(%s) = %#v, %v; want text=%q key=%q", tt.msg.String(), got, ok, tt.text, tt.key)
		}
	}
}

func TestTerminalFrameReadyRejectsStartupFrames(t *testing.T) {
	tests := []struct {
		name, screen string
		want         bool
	}{
		{"blank", "\x1b[2J\x1b[H", false},
		{"single spinner", "starting…", false},
		{"shell cursor", "› ", false},
		{"agent frame", "Claude Code\n› Ask anything", true},
		{"wide agent frame", "界界界界界界界界\n›", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := terminalFrameReady(tt.screen); got != tt.want {
				t.Errorf("terminalFrameReady(%q) = %v, want %v", tt.screen, got, tt.want)
			}
		})
	}
}

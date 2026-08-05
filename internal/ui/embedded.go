package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sadrig91/orbit/internal/pane"
	"github.com/sadrig91/orbit/internal/session"
	"github.com/sadrig91/orbit/internal/tmux"
)

const pendingIDPrefix = "tmux:"

type drivePreparation struct {
	id, cwd, title string
	// name is filled once tmux has started the resumed agent. Keeping the
	// preparation alive through that second phase avoids briefly replacing the
	// progress surface with an empty "connecting" terminal.
	name    string
	agent   session.Agent
	fresh   bool
	started time.Time
}

// paneInput is one ordered unit of keyboard input. Text and key are mutually
// exclusive: text is literal UTF-8, key is a tmux key name such as Enter or
// C-c. target makes queued input immune to the dashboard preview moving.
type paneInput struct {
	target string
	text   string
	key    string
	wheel  pane.WheelDirection
	x, y   int
}

func pendingSession(s *session.Session) bool {
	return s != nil && strings.HasPrefix(s.ID, pendingIDPrefix)
}

func pendingFromTmux(t *tmux.Session) *session.Session {
	var agent session.Agent
	switch t.Agent {
	case "claude":
		agent = session.Claude
	case "codex":
		agent = session.Codex
	case "copilot":
		agent = session.Copilot
	default:
		return nil
	}
	state := session.Working
	if !t.AgentRunning {
		state = session.ShellOnly
	}
	modified := t.Activity
	if modified.IsZero() {
		modified = t.Created
	}
	return &session.Session{
		Agent: agent, ID: pendingIDPrefix + t.Name, Cwd: t.Cwd,
		Title: "new " + agent.String(), Modified: modified, Tmux: t, State: state,
	}
}

// unlinkedCandidate joins a tmux session to the transcript it created.
//
// Older sessions have no launch baseline, so they retain the original
// best-recent-match behavior. New sessions are marked Pending and carry every
// conversation id that existed before launch; for those, guessing is forbidden
// until exactly one new transcript exists. This prevents an unrelated active
// agent in the same directory from stealing the pane.
func unlinkedCandidate(t *tmux.Session, sessions []*session.Session, claimed map[string]bool) *session.Session {
	known := map[string]bool{}
	if t.Pending {
		if t.Known == "" {
			return nil // the launcher has not recorded its baseline yet
		}
		for _, id := range strings.Split(t.Known, ",") {
			if id != "-" {
				known[id] = true
			}
		}
	}

	var matches []*session.Session
	for _, s := range sessions {
		if claimed[s.ID] || s.Agent.String() != t.Agent || s.Cwd != t.Cwd {
			continue
		}
		if s.Modified.Before(t.Created) {
			continue
		}
		if t.Pending && known[s.ID] {
			continue
		}
		matches = append(matches, s)
	}
	if t.Pending {
		if len(matches) == 1 {
			return matches[0]
		}
		return nil // ambiguity is visible as a pending row, never guessed away
	}
	var best *session.Session
	for _, s := range matches {
		if best == nil || s.Modified.After(best.Modified) {
			best = s
		}
	}
	return best
}

func (m *Model) addPendingSession(agent session.Agent, name, cwd string) {
	now := time.Now()
	s := &session.Session{
		Agent: agent, ID: pendingIDPrefix + name, Cwd: cwd,
		Title: "new " + agent.String(), Modified: now, State: session.Working,
		Tmux: &tmux.Session{
			Name: name, Agent: agent.String(), AgentRunning: true,
			Activity: now, Created: now, Cwd: cwd,
		},
	}
	for i, old := range m.all {
		if old.Tmux != nil && old.Tmux.Name == name {
			m.all[i] = s
			m.rebuild()
			m.selectTmux(name)
			return
		}
	}
	m.all = append(m.all, s)
	session.SortSessionsBy(m.all, m.sort)
	m.rebuild()
	m.selectTmux(name)
}

// addResumedSession makes a resumed transcript live immediately, instead of
// waiting for the next filesystem/tmux scan before Tab can return to it.
func (m *Model) addResumedSession(msg driveReadyMsg) {
	now := time.Now()
	for _, s := range m.all {
		if s.ID != msg.id || s.Agent != msg.agent {
			continue
		}
		s.Tmux = &tmux.Session{
			Name: msg.name, Agent: msg.agent.String(), AgentRunning: true,
			Activity: now, Created: now, Cwd: msg.cwd,
		}
		s.State = session.Working
		s.Modified = now
		break
	}
	session.SortSessionsBy(m.all, m.sort)
	m.rebuild()
	m.selectTmux(msg.name)
}

func (m *Model) selectTmux(name string) bool {
	for i, s := range m.view {
		if s.Tmux != nil && s.Tmux.Name == name {
			m.cursor = i
			return true
		}
	}
	return false
}

func (m *Model) enterEmbedded() tea.Cmd {
	s := m.sel()
	if s == nil {
		m.say("select a session to open its live pane")
		return nil
	}
	if m.preparing != nil {
		m.say("already preparing " + m.preparing.agent.String() + " for the live pane")
		return nil
	}
	if s.Tmux != nil {
		return m.focusEmbedded(s.Tmux.Name, s.Cwd, s.Name())
	}
	if !s.Agent.Installed() {
		return func() tea.Msg { return statusMsg(s.Agent.String() + " is not installed") }
	}
	m.preparing = &drivePreparation{
		id: s.ID, cwd: s.Cwd, title: s.Name(), agent: s.Agent, started: time.Now(),
	}
	sess := s
	resume := func() tea.Msg {
		name, err := resumeSession(sess)
		if err != nil {
			return driveFailedMsg{id: sess.ID, err: err.Error()}
		}
		return driveReadyMsg{
			name: name, cwd: sess.Cwd, id: sess.ID,
			title: sess.Name(), agent: sess.Agent,
		}
	}
	return tea.Batch(resume, m.spin.Tick)
}

func (m *Model) focusEmbedded(name, cwd, title string) tea.Cmd {
	m.embedded = name
	m.embeddedCwd = cwd
	m.embeddedName = title
	m.terminalFocused = true
	m.terminalZoom = false
	m.terminalW, m.terminalH = 0, 0
	// An explicit attempt to drive a pane gets another chance even if the
	// optional read-only preview failed earlier in this dashboard run.
	m.streamOff = false
	return m.activateEmbeddedCmd()
}

func (m *Model) leaveEmbedded() tea.Cmd {
	name := m.embedded
	if m.stream != nil {
		m.stream.FollowTail()
	}
	m.terminalFocused = false
	m.terminalZoom = false
	m.terminalW, m.terminalH = 0, 0
	m.selectTmux(name)
	m.say("Sessions focused · terminal remains live")
	return m.resizeEmbeddedCmd()
}

// unmountEmbedded releases the terminal that was retained across Tab once the
// list selection actually moves. The shared stream stays open so capture can
// switch it directly to the newly selected live session without reconnecting.
func (m *Model) unmountEmbedded() {
	if m.terminalFocused || m.embedded == "" {
		return
	}
	if m.stream != nil {
		m.stream.FollowTail()
	}
	m.embedded, m.embeddedCwd, m.embeddedName = "", "", ""
	m.terminalZoom = false
	m.terminalW, m.terminalH = 0, 0
}

func (m *Model) terminalSize() (int, int) {
	if m.terminalZoom {
		return max(20, m.w), max(5, m.h-2) // one title row and one help row
	}
	_, detW, bodyH := m.dashboardPaneSizes()
	// Same chrome calculation used by titledPane: two border columns, two
	// padding columns, the title row, and the box's top/bottom edges.
	return max(20, detW-4), max(5, bodyH-3)
}

// activateEmbeddedCmd switches the existing control client, if any, and then
// sizes it to the focused terminal. The two operations are sequential because
// resizing while a switch is in flight can reseed the emulator from the old
// session.
func (m *Model) activateEmbeddedCmd() tea.Cmd {
	if m.embedded == "" {
		return nil
	}
	if m.stream == nil {
		return m.capture()
	}
	p, target := m.stream, m.embedded
	w, h := m.terminalSize()
	waitForFrame := m.preparing != nil && m.preparing.name == target
	m.terminalW, m.terminalH = w, h
	return func() tea.Msg {
		if p.Session() != target {
			if err := p.Switch(target); err != nil {
				return paneReadyMsg{name: target, err: err.Error()}
			}
		}
		return resizePane(p, target, w, h, waitForFrame)
	}
}

func (m *Model) resizeEmbeddedCmd() tea.Cmd {
	if m.embedded == "" || m.stream == nil || m.stream.Session() != m.embedded {
		return nil
	}
	w, h := m.terminalSize()
	if w == m.terminalW && h == m.terminalH {
		return nil
	}
	m.terminalW, m.terminalH = w, h
	p, target := m.stream, m.embedded
	waitForFrame := m.preparing != nil && m.preparing.name == target
	return func() tea.Msg {
		return resizePane(p, target, w, h, waitForFrame)
	}
}

const (
	terminalFrameTimeout = 20 * time.Second
	terminalFrameStable  = 160 * time.Millisecond
	terminalFramePoll    = 40 * time.Millisecond
)

func resizePane(p livePane, target string, w, h int, waitForFrame bool) paneReadyMsg {
	if err := p.Resize(w, h); err != nil {
		return paneReadyMsg{name: target, err: err.Error()}
	}
	if !waitForFrame {
		return paneReadyMsg{name: target}
	}

	deadline := time.Now().Add(terminalFrameTimeout)
	var readySince time.Time
	for time.Now().Before(deadline) {
		if p.Session() != target {
			return paneReadyMsg{name: target, err: "terminal switched while waiting for its first frame"}
		}
		if terminalFrameReady(p.Render()) {
			if readySince.IsZero() {
				readySince = time.Now()
			} else if time.Since(readySince) >= terminalFrameStable {
				return paneReadyMsg{name: target}
			}
		} else {
			readySince = time.Time{}
		}
		time.Sleep(terminalFramePoll)
	}
	return paneReadyMsg{name: target, err: "agent started, but its terminal did not render a ready frame"}
}

// terminalFrameReady rejects the blank clear/redraw frames produced between
// an agent process taking over and its TUI being usable. Requiring a small
// amount of structure avoids revealing a lone startup spinner or shell prompt.
func terminalFrameReady(screen string) bool {
	plain := ansi.Strip(screen)
	visible, lines := 0, 0
	for _, line := range strings.Split(plain, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines++
		visible += len([]rune(line))
	}
	return lines >= 2 && visible >= 16
}

func (m *Model) queuePaneInput(input paneInput) tea.Cmd {
	if input.text == "" && input.key == "" && input.wheel == 0 {
		return nil
	}
	m.inputQueue = append(m.inputQueue, input)
	return m.pumpPaneInput()
}

// pumpPaneInput serializes tmux commands. Returning one command per raw key
// without a queue lets Bubble Tea run them concurrently, which can reorder
// characters under scheduler pressure.
func (m *Model) pumpPaneInput() tea.Cmd {
	if m.inputSending || m.stream == nil || len(m.inputQueue) == 0 {
		return nil
	}
	in := m.inputQueue[0]
	m.inputQueue = m.inputQueue[1:]
	m.inputSending = true
	p := m.stream
	return func() tea.Msg {
		var err error
		if in.text != "" {
			err = p.SendTextTo(in.target, in.text)
		} else if in.wheel != 0 {
			err = p.SendWheelTo(in.target, in.x, in.y, in.wheel)
		} else {
			err = p.SendKeyTo(in.target, in.key)
		}
		if err != nil {
			return paneInputDoneMsg{err: err.Error()}
		}
		return paneInputDoneMsg{}
	}
}

func paneInputFor(msg tea.KeyPressMsg) (paneInput, bool) {
	k := msg.Key()
	// Shifted printable characters already arrive in Text in their final form.
	if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper|tea.ModHyper) == 0 {
		return paneInput{text: k.Text}, true
	}

	name := msg.String()
	base := map[string]string{
		"enter": "Enter", "tab": "Tab", "shift+tab": "BTab",
		"esc": "Escape", "backspace": "BSpace", "delete": "DC",
		"insert": "IC", "up": "Up", "down": "Down", "left": "Left", "right": "Right",
		"home": "Home", "end": "End", "pgup": "PPage", "pgdown": "NPage",
	}
	if v, ok := base[name]; ok {
		return paneInput{key: v}, true
	}
	if len(name) >= 2 && name[0] == 'f' {
		allDigits := true
		for _, r := range name[1:] {
			allDigits = allDigits && r >= '0' && r <= '9'
		}
		if allDigits {
			return paneInput{key: strings.ToUpper(name)}, true
		}
	}

	parts := strings.Split(name, "+")
	if len(parts) < 2 {
		return paneInput{}, false
	}
	last := parts[len(parts)-1]
	key := base[last]
	if key == "" && len([]rune(last)) == 1 {
		key = last
	}
	if key == "" {
		return paneInput{}, false
	}
	var prefix string
	for _, mod := range parts[:len(parts)-1] {
		switch mod {
		case "ctrl":
			prefix += "C-"
		case "alt", "meta":
			prefix += "M-"
		case "shift":
			prefix += "S-"
		default:
			return paneInput{}, false
		}
	}
	return paneInput{key: prefix + key}, true
}

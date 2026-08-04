package main

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	cBright = lipgloss.Color("48")
	cGreen  = lipgloss.Color("35")
	cDim    = lipgloss.Color("240")
	cMid    = lipgloss.Color("245")
	cFg     = lipgloss.Color("252")
	cAmber  = lipgloss.Color("214")
	cRed    = lipgloss.Color("203")
	cCyan   = lipgloss.Color("80")

	sBar    = lipgloss.NewStyle().Foreground(cBright)
	sTitle  = lipgloss.NewStyle().Foreground(cBright).Bold(true)
	sName   = lipgloss.NewStyle().Foreground(cFg)
	sNameOn = lipgloss.NewStyle().Foreground(cBright).Bold(true)
	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sMid    = lipgloss.NewStyle().Foreground(cMid)
	sHead   = lipgloss.NewStyle().Foreground(cGreen)
	sRule   = lipgloss.NewStyle().Foreground(lipgloss.Color("236"))
	sErr    = lipgloss.NewStyle().Foreground(cRed)
)

func stateStyle(s State) lipgloss.Style {
	switch s {
	case Working:
		return lipgloss.NewStyle().Foreground(cBright)
	case NeedsApproval:
		return lipgloss.NewStyle().Foreground(cAmber).Bold(true)
	case YourTurn:
		return lipgloss.NewStyle().Foreground(cCyan)
	case ShellOnly:
		return lipgloss.NewStyle().Foreground(cGreen)
	}
	return sDim
}

func agentStyle(a Agent) lipgloss.Style {
	switch a {
	case Codex:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	case Copilot:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("176"))
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("173"))
}

type (
	tickMsg    time.Time
	scanMsg    []*Session
	previewMsg struct{ name, text string }
	statusMsg  string
	// readyMsg says a tmux session now exists and is waiting to be attached.
	readyMsg struct {
		name, cwd string
		mode      attachMode
	}
)

type model struct {
	ix     *Index
	all    []*Session
	view   []*Session
	cursor int
	top    int
	w, h   int

	filter    textinput.Model
	filtering bool
	showAll   bool

	preview     string
	previewName string

	status      string
	statusUntil time.Time
	mode        attachMode
	notify      *Notifier
}

func newModel(mode attachMode) *model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter"
	ti.CharLimit = 60
	return &model{ix: NewIndex(), filter: ti, mode: mode}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(tea.SetWindowTitle("orbit"), m.scan, tick())
}

func tick() tea.Cmd {
	return tea.Tick(2500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// scan reads every transcript store, joins it with live tmux state, and links
// sessions started with `n` to whatever transcript they turned out to write.
func (m *model) scan() tea.Msg {
	sessions := m.ix.Scan()
	byID := map[string]*Session{}
	for _, s := range sessions {
		// Sessions come from a cache and are reused across ticks, so last
		// tick's tmux link has to be cleared or a killed session stays "live".
		s.Tmux = nil
		byID[s.ID] = s
	}

	claimed := map[string]bool{}
	var unlinked []*Tmux
	for _, t := range TmuxList() {
		if t.SessionID == "" {
			unlinked = append(unlinked, t)
			continue
		}
		if s, ok := byID[t.SessionID]; ok {
			s.Tmux = t
			claimed[s.ID] = true
			if want := s.TabTitle(); want != t.Title {
				TmuxRetitle(t.Name, want)
			}
		}
	}
	for _, t := range unlinked {
		var best *Session
		for _, s := range sessions {
			if claimed[s.ID] || s.Agent != t.Agent || s.Cwd != t.Cwd {
				continue
			}
			if s.Modified.Before(t.Created) {
				continue // predates the tmux session, so it isn't the one it started
			}
			if best == nil || s.Modified.After(best.Modified) {
				best = s
			}
		}
		if best != nil {
			best.Tmux = t
			claimed[best.ID] = true
			TmuxLink(t.Name, best.ID)
			TmuxRetitle(t.Name, best.TabTitle())
		}
	}

	now := time.Now()
	for _, s := range sessions {
		s.resolve(now)
	}
	sortSessions(sessions)
	return scanMsg(sessions)
}

func (m *model) capture() tea.Cmd {
	s := m.sel()
	if s == nil || s.Tmux == nil {
		return func() tea.Msg { return previewMsg{} }
	}
	name := s.Tmux.Name
	return func() tea.Msg { return previewMsg{name, TmuxCapture(name, 60)} }
}

func (m *model) sel() *Session {
	if m.cursor < 0 || m.cursor >= len(m.view) {
		return nil
	}
	return m.view[m.cursor]
}

func (m *model) rebuild() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	cutoff := time.Now().AddDate(0, 0, -30)
	var keep *Session
	if s := m.sel(); s != nil {
		keep = s
	}
	m.view = nil
	for _, s := range m.all {
		if !m.showAll && !s.Live() {
			if s.Modified.Before(cutoff) || s.Title == "" {
				continue
			}
		}
		if q != "" {
			hay := strings.ToLower(s.Name() + " " + s.Cwd + " " + s.Branch + " " + s.Agent.String())
			if !strings.Contains(hay, q) {
				continue
			}
		}
		m.view = append(m.view, s)
	}
	// Keep the cursor on the same session across refreshes and re-sorts.
	if keep != nil {
		for i, s := range m.view {
			if s.ID == keep.ID {
				m.cursor = i
				return
			}
		}
	}
	if m.cursor >= len(m.view) {
		m.cursor = max(0, len(m.view)-1)
	}
}

func (m *model) say(s string) {
	m.status = s
	m.statusUntil = time.Now().Add(6 * time.Second)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.scan, m.capture(), tick())

	case scanMsg:
		m.all = msg
		m.notify.Update(m.all)
		m.rebuild()
		return m, nil

	case previewMsg:
		m.preview, m.previewName = msg.text, msg.name
		return m, nil

	case statusMsg:
		m.say(string(msg))
		return m, m.scan

	case readyMsg:
		return m, m.attach(msg.name, msg.cwd, msg.mode)

	case tea.KeyMsg:
		if m.filtering {
			switch msg.String() {
			case "esc":
				m.filtering = false
				m.filter.SetValue("")
				m.filter.Blur()
				m.rebuild()
			case "enter":
				m.filtering = false
				m.filter.Blur()
			default:
				var cmd tea.Cmd
				m.filter, cmd = m.filter.Update(msg)
				m.rebuild()
				return m, cmd
			}
			return m, nil
		}
		return m.key(msg)
	}
	return m, nil
}

func (m *model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.view)-1 {
			m.cursor++
		}
		return m, m.capture()
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, m.capture()
	case "g", "home":
		m.cursor = 0
		return m, m.capture()
	case "G", "end":
		m.cursor = max(0, len(m.view)-1)
		return m, m.capture()
	case "/":
		m.filtering = true
		m.filter.Focus()
		return m, textinput.Blink
	case "a":
		m.showAll = !m.showAll
		m.rebuild()
		if m.showAll {
			m.say("showing all sessions")
		} else {
			m.say("showing titled sessions from the last 30 days")
		}
		return m, nil
	case "r":
		m.say("refreshing")
		return m, tea.Batch(m.scan, m.capture())
	case "enter":
		return m, m.open(m.mode)
	case "t":
		return m, m.open(attachTab)
	case "w":
		return m, m.open(attachWindow)
	case "i":
		return m, m.open(attachInline)
	case "x":
		return m, m.kill()
	case "n":
		s := m.sel()
		if s == nil {
			return m, nil
		}
		return m, m.spawn(s.Agent, s.Cwd)
	case "1", "2", "3":
		s := m.sel()
		if s == nil {
			return m, nil
		}
		return m, m.spawn(AllAgents[int(msg.String()[0]-'1')], s.Cwd)
	}
	return m, nil
}

// open attaches the selected session, starting it first if it isn't running.
// Starting is slow enough (a shell has to come up) that it happens in a command
// and reports back through readyMsg rather than blocking the UI.
func (m *model) open(mode attachMode) tea.Cmd {
	s := m.sel()
	if s == nil {
		return nil
	}
	if s.Tmux != nil {
		return m.attach(s.Tmux.Name, s.Cwd, mode)
	}
	m.say("resuming " + s.Agent.String() + "…")
	sess := s
	return func() tea.Msg {
		name, err := TmuxResume(sess)
		if err != nil {
			return statusMsg("resume failed: " + err.Error())
		}
		return readyMsg{name, sess.Cwd, mode}
	}
}

func (m *model) spawn(ag Agent, cwd string) tea.Cmd {
	if !ag.Installed() {
		return func() tea.Msg { return statusMsg(ag.String() + " is not installed") }
	}
	mode := m.mode
	m.say("starting " + ag.String() + " in " + cwd + "…")
	return func() tea.Msg {
		name, err := TmuxNew(ag, cwd)
		if err != nil {
			return statusMsg("start failed: " + err.Error())
		}
		return readyMsg{name, cwd, mode}
	}
}

// attach hands the session to a tab, a window, or this very terminal. The
// in-place path suspends orbit and restores it when you detach, so it works in
// any terminal and needs no permissions — it's the fallback everywhere Ghostty
// tab-spawning isn't available.
func (m *model) attach(name, cwd string, mode attachMode) tea.Cmd {
	switch mode.resolve() {
	case attachTab:
		return func() tea.Msg {
			if err := OpenTab(name); err != nil {
				return statusMsg("tab failed: " + err.Error())
			}
			return statusMsg("attached " + name + " in a new tab")
		}
	case attachWindow:
		return func() tea.Msg {
			if err := OpenWindow(name, cwd); err != nil {
				return statusMsg("window failed: " + err.Error())
			}
			return statusMsg("attached " + name + " in a new window")
		}
	default:
		return tea.ExecProcess(attachCommand(name), func(err error) tea.Msg {
			if err != nil {
				return statusMsg("attach failed: " + err.Error())
			}
			return statusMsg("detached from " + name)
		})
	}
}

func (m *model) kill() tea.Cmd {
	s := m.sel()
	if s == nil || s.Tmux == nil {
		return func() tea.Msg { return statusMsg("nothing running for that session") }
	}
	name := s.Tmux.Name
	return func() tea.Msg {
		if err := TmuxKill(name); err != nil {
			return statusMsg("kill failed: " + err.Error())
		}
		return statusMsg("killed " + name)
	}
}

func (m *model) View() string {
	if m.w == 0 {
		return "starting orbit…"
	}
	listW := min(64, max(34, m.w*46/100))
	if m.w < 80 {
		listW = m.w - 2
	}
	detW := m.w - listW - 3
	bodyH := m.h - 4 // header, rule, footer, status

	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n")
	b.WriteString(sRule.Render(strings.Repeat("─", m.w)))
	b.WriteString("\n")

	left := m.list(listW, bodyH)
	if detW < 24 {
		b.WriteString(strings.Join(left, "\n"))
	} else {
		right := m.detail(detW, bodyH)
		for i := 0; i < bodyH; i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			b.WriteString(pad(l, listW) + sRule.Render(" │ ") + r + "\n")
		}
	}

	b.WriteString(sRule.Render(strings.Repeat("─", m.w)))
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m *model) header() string {
	var working, needs, turn int
	for _, s := range m.all {
		switch s.State {
		case Working:
			working++
		case NeedsApproval:
			needs++
		case YourTurn:
			turn++
		}
	}
	parts := []string{sDim.Render(itoa(len(m.view)) + " sessions")}
	if working > 0 {
		parts = append(parts, stateStyle(Working).Render(itoa(working)+" working"))
	}
	if turn > 0 {
		parts = append(parts, stateStyle(YourTurn).Render(itoa(turn)+" your turn"))
	}
	if needs > 0 {
		parts = append(parts, stateStyle(NeedsApproval).Render(itoa(needs)+" needs you"))
	}
	head := " " + sTitle.Render("orbit") + "  " + strings.Join(parts, sDim.Render(" · "))
	if m.filtering || m.filter.Value() != "" {
		head += "   " + m.filter.View()
	}
	return head
}

func (m *model) list(w, h int) []string {
	rows := h / 2
	if rows < 1 {
		rows = 1
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+rows {
		m.top = m.cursor - rows + 1
	}
	if m.top > max(0, len(m.view)-rows) {
		m.top = max(0, len(m.view)-rows)
	}

	var out []string
	if len(m.view) == 0 {
		out = append(out, "", sDim.Render("  no sessions match — press a for all"))
		return out
	}
	for i := m.top; i < len(m.view) && i < m.top+rows; i++ {
		s := m.view[i]
		sel := i == m.cursor

		bar := "  "
		if sel {
			bar = sBar.Render("▌") + " "
		}
		t := relTime(s.Modified)
		headW := w - 2 - len([]rune(t)) - 1
		cwd := pad(truncate(s.ShortCwd(), headW-5), headW-5)
		line1 := bar + stateStyle(s.State).Render(s.State.Icon()) + " " +
			agentStyle(s.Agent).Render(s.Agent.Tag()) + " " +
			sMid.Render(cwd) + " " + sDim.Render(t)

		label := s.State.Label()
		nameW := w - 4 - len([]rune(label))
		if label != "" {
			nameW--
		}
		ns := sName
		if sel {
			ns = sNameOn
		}
		line2 := bar + "  " + ns.Render(pad(truncate(clean(s.Name()), nameW), nameW))
		if label != "" {
			line2 += " " + stateStyle(s.State).Render(label)
		}
		out = append(out, line1, line2)
	}
	return out
}

func (m *model) detail(w, h int) []string {
	s := m.sel()
	if s == nil {
		return nil
	}
	var out []string
	add := func(ss ...string) { out = append(out, ss...) }

	add(sNameOn.Render(truncate(clean(s.Name()), w)))
	add(sDim.Render(truncate(s.Cwd, w)))

	meta := []string{s.Agent.String()}
	if s.Branch != "" {
		meta = append(meta, s.Branch)
	}
	if s.Msgs > 0 {
		meta = append(meta, itoa(s.Msgs)+" msgs")
	}
	meta = append(meta, relTime(s.Modified)+" ago")
	line := sMid.Render(truncate(strings.Join(meta, " · "), w))
	if lbl := s.State.Label(); lbl != "" {
		line += "  " + stateStyle(s.State).Render(lbl)
	}
	add(line, "")

	if s.Last != "" {
		add(sHead.Render("last prompt"))
		for _, l := range wrap(clean(s.Last), w-2) {
			add("  " + sName.Render(l))
		}
		add("")
	}

	if s.Tmux != nil && m.previewName == s.Tmux.Name && m.preview != "" {
		add(sHead.Render("live output"))
		lines := strings.Split(m.preview, "\n")
		// Trailing blank lines are just the unused bottom of the pane.
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		room := h - len(out) - 1
		if room > 0 && len(lines) > room {
			lines = lines[len(lines)-room:]
		}
		for _, l := range lines {
			add("  " + sMid.Render(truncate(clean(l), w-2)))
		}
	} else if s.Tmux == nil {
		add(sDim.Render("not running — enter to resume it"))
	}
	return out
}

func (m *model) footer() string {
	if m.status != "" && time.Now().Before(m.statusUntil) {
		st := sHead
		if strings.Contains(m.status, "failed") || strings.Contains(m.status, "not installed") {
			st = sErr
		}
		return " " + st.Render(m.status)
	}
	keys := [][2]string{
		{"⏎", "attach"}, {"i", "here"}, {"n", "new"}, {"1/2/3", "cl/cx/cp"},
		{"x", "kill"}, {"/", "filter"}, {"a", "all"}, {"q", "quit"},
	}
	if canSpawnTab() {
		keys = append(keys[:2], append([][2]string{{"w", "window"}}, keys[2:]...)...)
	}
	var ps []string
	for _, k := range keys {
		ps = append(ps, sBar.Render(k[0])+" "+sDim.Render(k[1]))
	}
	return " " + strings.Join(ps, sRule.Render(" · "))
}

func wrap(s string, w int) []string {
	if w < 8 {
		return []string{truncate(s, w)}
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= w:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
		if len(out) == 4 {
			return append(out[:4], truncate(line, w))
		}
	}
	if line != "" {
		out = append(out, truncate(line, w))
	}
	return out
}

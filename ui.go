package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	searchMsg  struct {
		query   string
		matches map[string]Match
	}
	summaryMsg struct {
		id, text, err string
	}
	// sendLogosMsg fires once the alt screen exists — see logosCmd.
	sendLogosMsg struct{}
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
	spin      spinner.Model
	filtering bool
	showAll   bool

	preview     string
	previewName string

	status      string
	statusUntil time.Time
	mode        attachMode
	icons       IconMode
	logosSent   bool
	cfg         Config
	sort        SortMode
	group       bool

	searching bool
	query     string               // the committed full-text query
	matches   map[string]Match     // session id -> where it matched
	summaries map[string]string    // session id -> cached or generated summary
	pending   map[string]time.Time // summaries in flight -> when they started
	prog      progress.Model
	summaryE  time.Duration // rolling estimate of how long a summary takes
	notify    *Notifier
}

func newModel(cfg Config, mode attachMode) *model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter"
	ti.CharLimit = 80
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(cBright)
	return &model{
		ix: NewIndex(), filter: ti, spin: sp, mode: mode,
		cfg: cfg, icons: cfg.iconMode(), sort: cfg.sortMode(), group: cfg.Group,
		summaries: map[string]string{}, pending: map[string]time.Time{},
		prog:     progress.New(progress.WithColors(lipgloss.Color("#1f8a54"), lipgloss.Color("#00ff87")), progress.WithoutPercentage()),
		summaryE: 12 * time.Second, // seeded from observation; adapts as it runs
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.scan, tick(), m.spin.Tick)
}

// logosCmd waits for the first frame to have been painted — and with it the
// alt screen switch — before uploading the marks.
func logosCmd() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return sendLogosMsg{} })
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
	sortSessionsBy(sessions, m.sort)
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
	cutoff := time.Now().AddDate(0, 0, -m.cfg.RecentDays)
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
		if m.query != "" {
			if _, hit := m.matches[s.ID]; !hit {
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

// summarise kicks off a provider CLI in the background. It takes seconds, so
// the UI stays responsive and the result arrives as a message.
func (m *model) summarise(s *Session) tea.Cmd {
	if !m.cfg.Summary.Enabled {
		return func() tea.Msg { return statusMsg("summaries are disabled in config") }
	}
	if _, have := m.summaries[s.ID]; have {
		return nil
	}
	if _, running := m.pending[s.ID]; running {
		return nil
	}
	m.pending[s.ID] = time.Now()
	m.say("summarising with " + s.Agent.String() + "…")
	cfg, sess := m.cfg.Summary, s
	gen := func() tea.Msg {
		text, err := GenerateSummary(sess, cfg)
		out := summaryMsg{id: sess.ID, text: text}
		if err != nil {
			out.err = err.Error()
		}
		return out
	}
	return tea.Batch(gen, m.spin.Tick) // make sure the bar animates
}

// summaryProgress estimates how far along a generation is. The provider CLIs
// report nothing, so this is elapsed time against a rolling estimate, capped
// short of full — a bar that sits at 100% while still working reads as hung.
func (m *model) summaryProgress(id string) (float64, time.Duration, bool) {
	started, ok := m.pending[id]
	if !ok {
		return 0, 0, false
	}
	elapsed := time.Since(started)
	pct := float64(elapsed) / float64(max(m.summaryE, time.Second))
	return min(pct, 0.95), elapsed, true
}

func (m *model) anyWorking() bool {
	for _, s := range m.all {
		if s.State == Working {
			return true
		}
	}
	return false
}

func (m *model) say(s string) {
	m.status = s
	m.statusUntil = time.Now().Add(6 * time.Second)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.icons == IconLogo && !m.logosSent {
			m.logosSent = true
			return m, logosCmd()
		}
		return m, nil

	case sendLogosMsg:
		// Kitty virtual placements belong to the screen they were created on.
		// Sending these before Bubble Tea switches to the alt screen puts them
		// on the primary one, where the dashboard never draws — the images then
		// exist but every placeholder cell resolves to nothing and renders
		// blank. Hence: transmit only once the alt screen is already up.
		if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
			TransmitLogos(tty)
			tty.Close()
		}
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.scan, m.capture(), tick())

	case searchMsg:
		m.query, m.matches = msg.query, msg.matches
		m.rebuild()
		m.say(itoa(len(msg.matches)) + " sessions mention " + strconv.Quote(msg.query))
		return m, nil

	case summaryMsg:
		// Let the estimate track reality, so the bar is honest on this machine
		// with these models rather than to a hardcoded guess.
		if started, ok := m.pending[msg.id]; ok && msg.err == "" {
			took := time.Since(started)
			m.summaryE = (m.summaryE*3 + took) / 4
		}
		delete(m.pending, msg.id)
		if msg.err != "" {
			m.say("summary failed: " + msg.err)
			return m, nil
		}
		m.summaries[msg.id] = msg.text
		return m, nil

	case scanMsg:
		wasIdle := !m.anyWorking()
		m.all = msg
		m.notify.Update(m.all)
		m.rebuild()
		for _, s := range m.all {
			if _, have := m.summaries[s.ID]; have {
				continue
			}
			if text, ok := CachedSummary(s); ok {
				m.summaries[s.ID] = text
			}
		}
		if m.cfg.Summary.Auto {
			if sel := m.sel(); sel != nil {
				if _, have := m.summaries[sel.ID]; !have {
					return m, m.summarise(sel)
				}
			}
		}
		if wasIdle && m.anyWorking() {
			return m, m.spin.Tick // restart the animation loop
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		// Only keep animating while something is actually working, so an idle
		// dashboard costs nothing.
		if len(m.pending) > 0 || m.anyWorking() {
			return m, cmd
		}
		return m, nil

	case previewMsg:
		m.preview, m.previewName = msg.text, msg.name
		return m, nil

	case statusMsg:
		m.say(string(msg))
		return m, m.scan

	case readyMsg:
		return m, m.attach(msg.name, msg.cwd, msg.mode)

	case tea.KeyPressMsg:
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
				if m.searching {
					q := strings.TrimSpace(m.filter.Value())
					m.searching = false
					m.filter.SetValue("")
					m.filter.Prompt = "/"
					if q == "" {
						m.query, m.matches = "", nil
						m.rebuild()
						return m, nil
					}
					all := m.all
					m.say("searching transcripts for " + strconv.Quote(q) + "…")
					return m, func() tea.Msg {
						return searchMsg{query: q, matches: SearchTranscripts(all, q)}
					}
				}
			default:
				var cmd tea.Cmd
				m.filter, cmd = m.filter.Update(msg)
				if !m.searching {
					m.rebuild() // quick filter is live; full-text waits for enter
				}
				return m, cmd
			}
			return m, nil
		}
		return m.key(msg)
	}
	return m, nil
}

func (m *model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		m.searching = false
		m.filtering = true
		m.filter.Prompt = "/"
		m.filter.Placeholder = "filter titles and paths"
		m.filter.Focus()
		return m, textinput.Blink
	case "f":
		m.searching = true
		m.filtering = true
		m.filter.Prompt = "search: "
		m.filter.Placeholder = "text inside transcripts"
		m.filter.SetValue("")
		m.filter.Focus()
		return m, textinput.Blink
	case "o":
		m.sort = AllSorts[(int(m.sort)+1)%len(AllSorts)]
		sortSessionsBy(m.all, m.sort)
		m.rebuild()
		m.say("sorted by " + m.sort.String())
		return m, nil
	case "p":
		m.group = !m.group
		if m.group {
			m.sort = SortProject
			sortSessionsBy(m.all, m.sort)
			m.rebuild()
		}
		return m, nil
	case "s":
		if sel := m.sel(); sel != nil {
			return m, m.summarise(sel)
		}
		return m, nil
	case "esc":
		if m.query != "" {
			m.query, m.matches = "", nil
			m.rebuild()
			m.say("search cleared")
		}
		return m, nil
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

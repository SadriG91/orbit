package ui

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
	"github.com/sadrig91/orbit/internal/config"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/search"
	"github.com/sadrig91/orbit/internal/session"
	"github.com/sadrig91/orbit/internal/summary"
	"github.com/sadrig91/orbit/internal/tmux"
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

func stateStyle(s session.State) lipgloss.Style {
	switch s {
	case session.Working:
		return lipgloss.NewStyle().Foreground(cBright)
	case session.NeedsApproval:
		return lipgloss.NewStyle().Foreground(cAmber).Bold(true)
	case session.YourTurn:
		return lipgloss.NewStyle().Foreground(cCyan)
	case session.ShellOnly:
		return lipgloss.NewStyle().Foreground(cGreen)
	}
	return sDim
}

func agentStyle(a session.Agent) lipgloss.Style {
	switch a {
	case session.Codex:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	case session.Copilot:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("176"))
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("173"))
}

type (
	tickMsg    time.Time
	scanMsg    []*session.Session
	previewMsg struct{ name, text string }
	statusMsg  string
	searchMsg  struct {
		query   string
		matches map[string]search.Match
	}
	summaryMsg struct {
		id, err string
		rec     summary.Record
	}
	// sendLogosMsg fires once the alt screen exists — see logosCmd.
	sendLogosMsg struct{}
	// readyMsg says a tmux session now exists and is waiting to be attached.
	readyMsg struct {
		name, cwd string
		mode      attachMode
	}
)

type Model struct {
	ix     *session.Index
	all    []*session.Session
	view   []*session.Session
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
	cfg         config.Config
	sort        session.SortMode
	group       bool

	scanning bool // a scan is in flight; see scanCmd
	rescan   bool // an explicit refresh arrived mid-scan and owes us another
	pruned   bool // the summary cache has been swept once, after the first scan

	searching bool
	query     string                    // the committed full-text query
	matches   map[string]search.Match   // session id -> where it matched
	summaries map[string]summary.Record // session id -> cached or generated summary
	pending   map[string]time.Time      // summaries in flight -> when they started
	prog      progress.Model
	queue     []string      // session ids waiting to be summarised
	summaryE  time.Duration // rolling estimate, shown as elapsed context only
	notify    *Notifier
}

// New builds the dashboard. The attach override comes from a flag; empty means
// use whatever the config says.
func New(cfg config.Config, attachOverride string) *Model {
	mode := parseAttachMode(cfg.Attach)
	if attachOverride != "" {
		mode = parseAttachMode(attachOverride)
	}
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter"
	ti.CharLimit = 80
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(cBright)
	return &Model{
		ix: session.NewIndex(), filter: ti, spin: sp, mode: mode,
		cfg: cfg, icons: ResolveIconMode(cfg.IconMode()), sort: session.ParseSortMode(cfg.Sort), group: cfg.Group,
		summaries: map[string]summary.Record{}, pending: map[string]time.Time{},
		notify:   NewNotifier(cfg.Notify),
		prog:     progress.New(progress.WithColors(lipgloss.Color("#1f8a54"), lipgloss.Color("#00ff87")), progress.WithoutPercentage()),
		summaryE: 12 * time.Second, // seeded from observation; adapts as it runs
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.scanCmd(), tick(), m.spin.Tick)
}

// logosCmd waits for the first frame to have been painted — and with it the
// alt screen switch — before uploading the marks.
func logosCmd() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return sendLogosMsg{} })
}

func tick() tea.Cmd {
	return tea.Tick(2500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// scanCmd issues a scan, unless one is already running.
//
// Bubble Tea runs batched commands concurrently, and the tick that starts a
// scan re-arms itself whether or not the previous scan finished — over a few
// hundred transcripts on a cold start, it won't have. Two scans in flight write
// Index.files at the same time, which is a fatal concurrent map write, not a
// benign race. One at a time; a dropped tick costs 2.5 seconds of staleness.
//
// Sorting is deliberately not done here. The result is ordered on arrival
// instead, with whatever mode is current then, so pressing `o` or `p` during a
// scan isn't undone when it lands.
func (m *Model) scanCmd() tea.Cmd {
	if m.scanning {
		return nil
	}
	m.scanning = true
	ix := m.ix
	return func() tea.Msg { return scan(ix) }
}

// refreshCmd is scanCmd for the cases where dropping the request would be
// visible — a manual `r`, or the settling scan after a kill or an attach. If a
// scan is already running it is remembered and reissued when that one lands.
func (m *Model) refreshCmd() tea.Cmd {
	if m.scanning {
		m.rescan = true
		return nil
	}
	return m.scanCmd()
}

// scan reads every transcript store, joins it with live tmux state, and links
// sessions started with `n` to whatever transcript they turned out to write.
func scan(ix *session.Index) tea.Msg {
	sessions := ix.Scan()
	byID := map[string]*session.Session{}
	for _, s := range sessions {
		// Sessions come from a cache and are reused across ticks, so last
		// tick's tmux link has to be cleared or a killed session stays "live".
		s.Tmux = nil
		byID[s.ID] = s
	}

	claimed := map[string]bool{}
	var unlinked []*tmux.Session
	for _, t := range tmux.List() {
		if t.SessionID == "" {
			unlinked = append(unlinked, t)
			continue
		}
		if s, ok := byID[t.SessionID]; ok {
			s.Tmux = t
			claimed[s.ID] = true
			if want := s.TabTitle(); want != t.Title {
				tmux.Retitle(t.Name, want)
			}
		}
	}
	for _, t := range unlinked {
		var best *session.Session
		for _, s := range sessions {
			if claimed[s.ID] || s.Agent.String() != t.Agent || s.Cwd != t.Cwd {
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
			tmux.Link(t.Name, best.ID)
			tmux.Retitle(t.Name, best.TabTitle())
		}
	}

	now := time.Now()
	for _, s := range sessions {
		s.Resolve(now)
	}
	return scanMsg(sessions)
}

func (m *Model) capture() tea.Cmd {
	s := m.sel()
	if s == nil || s.Tmux == nil {
		return func() tea.Msg { return previewMsg{} }
	}
	name := s.Tmux.Name
	return func() tea.Msg { return previewMsg{name, tmux.Capture(name, 60)} }
}

func (m *Model) sel() *session.Session {
	if m.cursor < 0 || m.cursor >= len(m.view) {
		return nil
	}
	return m.view[m.cursor]
}

func (m *Model) rebuild() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	cutoff := time.Now().AddDate(0, 0, -m.cfg.RecentDays)
	var keep *session.Session
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

// maxSummaryJobs bounds how many provider CLIs run at once. Each is a whole
// agent process; more in parallel makes every one of them slower.
const maxSummaryJobs = 2

// shouldAutoSummarise decides whether to spend money without being asked.
//
// A summary goes stale the moment a session gains a message, but regenerating
// then would bill a request per prompt on an active session. So automatic
// updates wait for a session to fall far enough behind, and never fire while a
// turn is in flight — the transcript is still being written, and whatever it
// says right now is about to change.
func (m *Model) shouldAutoSummarise(s *session.Session) bool {
	if !m.cfg.Summary.Enabled || !m.cfg.Summary.Auto {
		return false
	}
	if s.State == session.Working || s.State == session.NeedsApproval {
		return false
	}
	rec, have := m.summaries[s.ID]
	if !have {
		return true // never summarised; the first one is the point of auto
	}
	minNew := m.cfg.Summary.AutoMinNew
	if minNew <= 0 {
		minNew = 8
	}
	return rec.Behind(s) >= minNew
}

// pruneCmd sweeps cached summaries whose sessions are gone. It runs once, after
// the first scan has established what still exists, and never on an empty list:
// a store that failed to read would otherwise be indistinguishable from every
// session having been deleted, and the whole cache would go with it.
func (m *Model) pruneCmd() tea.Cmd {
	if m.pruned || !m.cfg.Summary.Enabled || len(m.all) == 0 {
		return nil
	}
	m.pruned = true
	all := snapshot(m.all)
	return func() tea.Msg {
		summary.Prune(all)
		return nil
	}
}

// snapshot copies the slice — not the sessions — for a command that will read
// it on another goroutine. `o` and `p` sort m.all in place, which swaps
// elements of the very array the command is ranging over. The sessions
// themselves are never mutated after a scan hands them over, so copying the
// header is enough.
func snapshot(ss []*session.Session) []*session.Session {
	out := make([]*session.Session, len(ss))
	copy(out, ss)
	return out
}

// summariseAll queues every visible session that has no summary yet. The global
// progress bar then advances as each finishes.
func (m *Model) summariseAll() tea.Cmd {
	if !m.cfg.Summary.Enabled {
		return func() tea.Msg { return statusMsg("summaries are disabled in config") }
	}
	n := 0
	for _, s := range m.view {
		if rec, have := m.summaries[s.ID]; have && !rec.Stale(s) {
			continue
		}
		if _, running := m.pending[s.ID]; running {
			continue
		}
		if slicesContains(m.queue, s.ID) {
			continue
		}
		m.queue = append(m.queue, s.ID)
		n++
	}
	if n == 0 {
		return func() tea.Msg { return statusMsg("every visible session is already summarised") }
	}
	m.say("queued " + format.Itoa(n) + " sessions to summarise")
	return m.pump()
}

func slicesContains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// pump starts queued jobs up to the concurrency limit.
func (m *Model) pump() tea.Cmd {
	var cmds []tea.Cmd
	for len(m.pending) < maxSummaryJobs && len(m.queue) > 0 {
		id := m.queue[0]
		m.queue = m.queue[1:]
		var target *session.Session
		for _, s := range m.all {
			if s.ID == id {
				target = s
				break
			}
		}
		if target == nil {
			continue
		}
		if cmd := m.summarise(target); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// summarise kicks off a provider CLI in the background. It takes seconds, so
// the UI stays responsive and the result arrives as a message.
func (m *Model) summarise(s *session.Session) tea.Cmd {
	if !m.cfg.Summary.Enabled {
		return func() tea.Msg { return statusMsg("summaries are disabled in config") }
	}
	if rec, have := m.summaries[s.ID]; have && !rec.Stale(s) {
		return nil // already current
	}
	if _, running := m.pending[s.ID]; running {
		return nil
	}
	m.pending[s.ID] = time.Now()
	m.say("summarising with " + s.Agent.String() + "…")
	cfg, sess := m.cfg.Summary, s
	gen := func() tea.Msg {
		rec, err := summary.Generate(sess, cfg)
		out := summaryMsg{id: sess.ID, rec: rec}
		if err != nil {
			out.err = err.Error()
		}
		return out
	}
	return tea.Batch(gen, m.spin.Tick) // make sure the spinner animates
}

// summaryElapsed reports how long a specific job has been running, for the
// detail pane. It is not progress — the provider CLIs report none.
func (m *Model) summaryElapsed(id string) (time.Duration, bool) {
	started, ok := m.pending[id]
	if !ok {
		return 0, false
	}
	return time.Since(started), true
}

// summaryCoverage is the global bar: how many of the sessions on screen have a
// summary. It only moves when one finishes, so it measures work completed
// rather than time passed.
func (m *Model) summaryCoverage() (done, total, inflight int) {
	for _, s := range m.view {
		total++
		if rec, have := m.summaries[s.ID]; have && !rec.Stale(s) {
			done++
		}
	}
	return done, total, len(m.pending) + len(m.queue)
}

func (m *Model) anyWorking() bool {
	for _, s := range m.all {
		if s.State == session.Working {
			return true
		}
	}
	return false
}

func (m *Model) say(s string) {
	m.status = s
	m.statusUntil = time.Now().Add(6 * time.Second)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		return m, tea.Batch(m.scanCmd(), m.capture(), tick())

	case searchMsg:
		m.query, m.matches = msg.query, msg.matches
		m.rebuild()
		m.say(format.Itoa(len(msg.matches)) + " sessions mention " + strconv.Quote(msg.query))
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
			return m, m.pump()
		}
		m.summaries[msg.id] = msg.rec
		return m, m.pump()

	case scanMsg:
		wasIdle := !m.anyWorking()
		m.scanning = false
		m.all = msg
		// Order it here rather than in the scan, so a sort or grouping chosen
		// while the scan was in flight survives its arrival instead of being
		// silently reverted until the next tick.
		session.SortSessionsBy(m.all, m.sort)
		m.notify.Update(m.all)
		m.rebuild()
		for _, s := range m.all {
			if _, have := m.summaries[s.ID]; have {
				continue
			}
			if rec, ok := summary.Load(s); ok {
				m.summaries[s.ID] = rec
			}
		}
		var cmds []tea.Cmd
		if cmd := m.pruneCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.rescan {
			m.rescan = false
			cmds = append(cmds, m.scanCmd())
		}
		if sel := m.sel(); sel != nil && m.shouldAutoSummarise(sel) {
			cmds = append(cmds, m.summarise(sel))
		}
		if wasIdle && m.anyWorking() {
			cmds = append(cmds, m.spin.Tick) // restart the animation loop
		}
		return m, tea.Batch(cmds...)

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
		return m, m.refreshCmd()

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
					all := snapshot(m.all)
					m.say("searching transcripts for " + strconv.Quote(q) + "…")
					return m, func() tea.Msg {
						return searchMsg{query: q, matches: search.Transcripts(all, q)}
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

func (m *Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		m.sort = session.AllSorts[(int(m.sort)+1)%len(session.AllSorts)]
		session.SortSessionsBy(m.all, m.sort)
		m.rebuild()
		m.say("sorted by " + m.sort.String())
		return m, nil
	case "p":
		m.group = !m.group
		if m.group {
			m.sort = session.SortProject
			session.SortSessionsBy(m.all, m.sort)
			m.rebuild()
		}
		return m, nil
	case "s":
		if sel := m.sel(); sel != nil {
			return m, m.summarise(sel)
		}
		return m, nil
	case "S":
		return m, m.summariseAll()
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
		return m, tea.Batch(m.refreshCmd(), m.capture())
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
		return m, m.spawn(session.AllAgents[int(msg.String()[0]-'1')], s.Cwd)
	}
	return m, nil
}

// open attaches the selected session, starting it first if it isn't running.
// Starting is slow enough (a shell has to come up) that it happens in a command
// and reports back through readyMsg rather than blocking the UI.
func (m *Model) open(mode attachMode) tea.Cmd {
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
		name, err := resumeSession(sess)
		if err != nil {
			return statusMsg("resume failed: " + err.Error())
		}
		return readyMsg{name, sess.Cwd, mode}
	}
}

func (m *Model) spawn(ag session.Agent, cwd string) tea.Cmd {
	if !ag.Installed() {
		return func() tea.Msg { return statusMsg(ag.String() + " is not installed") }
	}
	mode := m.mode
	m.say("starting " + ag.String() + " in " + cwd + "…")
	return func() tea.Msg {
		name, err := newSession(ag, cwd)
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
func (m *Model) attach(name, cwd string, mode attachMode) tea.Cmd {
	switch mode.resolve() {
	case attachTab:
		return func() tea.Msg {
			if err := tmux.OpenTab(name); err != nil {
				return statusMsg("tab failed: " + err.Error())
			}
			return statusMsg("attached " + name + " in a new tab")
		}
	case attachWindow:
		return func() tea.Msg {
			if err := tmux.OpenWindow(name, cwd); err != nil {
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

func (m *Model) kill() tea.Cmd {
	s := m.sel()
	if s == nil || s.Tmux == nil {
		return func() tea.Msg { return statusMsg("nothing running for that session") }
	}
	name := s.Tmux.Name
	return func() tea.Msg {
		if err := tmux.Kill(name); err != nil {
			return statusMsg("kill failed: " + err.Error())
		}
		return statusMsg("killed " + name)
	}
}

// Close releases the notifier's handle on the terminal.
func (m *Model) Close() { m.notify.Close() }

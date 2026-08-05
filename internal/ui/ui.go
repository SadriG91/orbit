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
	"github.com/sadrig91/orbit/internal/dispatch"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/hooks"
	"github.com/sadrig91/orbit/internal/pane"
	"github.com/sadrig91/orbit/internal/search"
	"github.com/sadrig91/orbit/internal/session"
	"github.com/sadrig91/orbit/internal/summary"
	"github.com/sadrig91/orbit/internal/term"
	"github.com/sadrig91/orbit/internal/tmux"
	"github.com/sadrig91/orbit/internal/update"
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

type groupMode int

const (
	groupNone groupMode = iota
	groupProject
	groupAgent
)

func parseGroupMode(enabled bool, name string) groupMode {
	if !enabled {
		return groupNone
	}
	if strings.EqualFold(name, "agent") {
		return groupAgent
	}
	return groupProject
}

func (g groupMode) String() string {
	switch g {
	case groupProject:
		return "project"
	case groupAgent:
		return "agent"
	}
	return "none"
}

func (g groupMode) next() groupMode {
	return (g + 1) % 3
}

func groupKey(s *session.Session, mode groupMode) string {
	switch mode {
	case groupProject:
		return s.Cwd
	case groupAgent:
		return s.Agent.String()
	}
	return ""
}

// groupSessions makes each section contiguous without throwing away the active
// sort. The first session a sort surfaces decides where its group lands, and
// the remaining sessions keep that same ordering inside the section. A project
// needing attention therefore stays near the top without splitting into a
// second, repeated heading when its dormant sessions appear later.
func groupSessions(ss []*session.Session, mode groupMode) []*session.Session {
	if mode == groupNone || len(ss) < 2 {
		return ss
	}
	type bucket struct {
		items []*session.Session
	}
	var groups []*bucket
	byKey := make(map[string]*bucket)
	for _, s := range ss {
		key := groupKey(s, mode)
		b := byKey[key]
		if b == nil {
			b = &bucket{}
			byKey[key] = b
			groups = append(groups, b)
		}
		b.items = append(b.items, s)
	}
	out := make([]*session.Session, 0, len(ss))
	for _, b := range groups {
		out = append(out, b.items...)
	}
	return out
}

type (
	tickMsg time.Time
	scanMsg struct {
		gen      int
		sessions []*session.Session
	}
	previewMsg struct{ name, text string }
	// The streaming preview: a connection opened (or failed to open), and a
	// wakeup saying the emulator's screen changed.
	paneOpenMsg struct {
		p   *pane.Pane
		err string
	}
	paneDirtyMsg struct{}
	// paneSnapshotMsg restores the cached detail preview when navigation
	// returns to the session the stream was already watching.
	paneSnapshotMsg struct{ name string }
	// paneGoneMsg says the control client has ended and will send nothing more.
	paneGoneMsg struct{}
	statusMsg   string
	searchMsg   struct {
		query   string
		matches map[string]search.Match
	}
	summaryMsg struct {
		id, err string
		rec     summary.Record
	}
	// sendLogosMsg fires once the alt screen exists — see logosCmd.
	sendLogosMsg struct{}
	// The update sequence: a release was found, it finished installing, and
	// the pause before restarting has elapsed.
	updateFoundMsg struct{ version string }
	updateDoneMsg  struct {
		version, exe, err string
	}
	relaunchMsg struct{}
	// readyMsg says a tmux session now exists and is waiting to be attached.
	readyMsg struct {
		name, cwd string
		mode      attachMode
	}
	// startedMsg is a fresh interactive agent waiting inside tmux. Unlike a
	// resumed session, a new one stays in Orbit and focuses its live pane
	// instead of immediately taking over another terminal.
	startedMsg struct {
		name, cwd string
		agent     session.Agent
	}
	startFailedMsg struct{ err string }
	// driveReadyMsg is a dormant transcript resumed by Tab. It becomes a live
	// row and receives pane focus, but never attaches an external terminal.
	driveReadyMsg struct {
		name, cwd, id, title string
		agent                session.Agent
	}
	driveFailedMsg struct{ id, err string }
	paneReadyMsg   struct {
		name, err string
	}
	paneInputDoneMsg struct{ err string }
)

type Model struct {
	ix     *session.Index
	all    []*session.Session
	view   []*session.Session
	cursor int
	top    int
	w, h   int

	filter      textinput.Model
	dispatchDir textinput.Model
	spin        spinner.Model
	filtering   bool
	showAll     bool
	showHelp    bool

	// The dispatch prompt. Target is captured when `d` is pressed rather than
	// read back when Enter is: a scan landing mid-typing re-sorts the list and
	// moves the cursor, and the task you were writing belongs to the session
	// you were looking at when you started writing it.
	dispatching        bool
	dispatchTo         session.Agent
	dispatchInto       string
	dispatchDirFocused bool
	dispatchDirCursor  int

	preview     string
	previewName string

	// stream is the live preview's control client, or nil when there isn't
	// one. streamOff latches after a failure so a tmux that won't do control
	// mode costs one attempt rather than one per cursor movement, and
	// streamOpening single-flights the dial — capture runs on every tick, and
	// a handshake that outlasts one would otherwise start a second client.
	stream        livePane
	streamOff     bool
	streamOpening bool

	// embedded is the tmux session mounted in the dashboard's right pane;
	// terminalFocused says whether that mounted pane owns input. Keeping these
	// separate lets Tab return to Sessions without replacing the terminal with
	// a lossy, truncated detail preview.
	embedded        string
	embeddedCwd     string
	embeddedName    string
	terminalFocused bool
	terminalZoom    bool
	terminalWide    bool
	inputQueue      []paneInput
	inputSending    bool
	terminalW       int
	terminalH       int

	// preparing is the dormant transcript currently being resumed for the
	// live pane. It is visible in the row, detail pane, and footer until tmux
	// is ready or the resume fails.
	preparing *drivePreparation

	status      string
	statusUntil time.Time
	mode        attachMode
	icons       IconMode
	logosSent   bool
	cfg         config.Config
	sort        session.SortMode
	group       groupMode

	scanning  bool      // a scan is in flight; see scanCmd
	rescan    bool      // an explicit refresh arrived mid-scan and owes us another
	scanGen   int       // increments per issued scan; stale results are discarded
	scanStart time.Time // when the in-flight scan began, for the watchdog
	pruned    bool      // the summary cache has been swept once, after the first scan

	searching bool
	query     string                    // the committed full-text query
	matches   map[string]search.Match   // session id -> where it matched
	summaries map[string]summary.Record // session id -> cached or generated summary
	pending   map[string]time.Time      // summaries in flight -> when they started
	prog      progress.Model
	queue     []string      // session ids waiting to be summarised
	summaryE  time.Duration // rolling estimate, shown as elapsed context only
	notify    *Notifier

	dumpPath string // where SIGUSR1 writes goroutine stacks; see internal/debug

	version  string // this build, for the update check
	updating bool   // an update is in flight; keeps the spinner running
	relaunch string // binary to exec once the program exits — see Relaunch
}

// New builds the dashboard. The attach override comes from a flag; empty means
// use whatever the config says.
func New(cfg config.Config, attachOverride, version string) *Model {
	mode := parseAttachMode(cfg.Attach)
	if attachOverride != "" {
		mode = parseAttachMode(attachOverride)
	}
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter"
	ti.CharLimit = 80
	dir := textinput.New()
	dir.Placeholder = "directory"
	dir.CharLimit = 4096
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(cBright)
	return &Model{
		ix: session.NewIndex(), filter: ti, dispatchDir: dir, spin: sp, mode: mode, version: version,
		cfg: cfg, icons: ResolveIconMode(cfg.IconMode()), sort: session.ParseSortMode(cfg.Sort), group: parseGroupMode(cfg.Group, cfg.GroupBy),
		summaries: map[string]summary.Record{}, pending: map[string]time.Time{},
		notify:   NewNotifier(cfg.Notify),
		prog:     progress.New(progress.WithColors(lipgloss.Color("#1f8a54"), lipgloss.Color("#00ff87")), progress.WithoutPercentage()),
		summaryE: 12 * time.Second, // seeded from observation; adapts as it runs
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.scanCmd(), tick(), m.spin.Tick, m.updateCheckCmd(),
		pruneHookStateCmd, pruneDispatchCmd)
}

// pruneHookStateCmd sweeps hook state files that outlived their sessions. Its
// own command rather than a rider on the summary prune, which is gated on the
// summary feature flag — a lifecycle these files do not share.
func pruneHookStateCmd() tea.Msg {
	hooks.Prune(7 * 24 * time.Hour)
	return nil
}

// updateCheckCmd asks whether a newer orbit exists, off the UI goroutine. The
// check is cached for a day inside the update package, so this is usually a
// file read; when it isn't, the dashboard is already drawing.
func (m *Model) updateCheckCmd() tea.Cmd {
	if !m.cfg.AutoUpdate() {
		return nil
	}
	version := m.version
	return func() tea.Msg {
		if v := update.Check(version); v != "" {
			return updateFoundMsg{version: v}
		}
		return nil
	}
}

// updateApplyCmd installs the release. It can take minutes — brew refreshes
// its taps first — so the status line says what is happening and the spinner
// keeps moving while it does.
func (m *Model) updateApplyCmd(version string) tea.Cmd {
	m.updating = true
	m.say("updating orbit to " + version + "…")
	return tea.Batch(func() tea.Msg {
		exe, err := update.Apply(version)
		out := updateDoneMsg{version: version, exe: exe}
		if err != nil {
			out.err = err.Error()
		}
		return out
	}, m.spin.Tick)
}

// restartPause is how long the "updated" message stays up before orbit
// replaces itself. Long enough to read, short enough not to feel stuck.
const restartPause = 2500 * time.Millisecond

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
	m.scanStart = time.Now()
	m.scanGen++
	gen, ix := m.scanGen, m.ix
	return func() tea.Msg { return scan(gen, ix) }
}

// scanStuck is how long a scan may run before the watchdog gives up on it.
// Well past a cold start over a few hundred transcripts, and well past the
// timeouts on the subprocesses a scan shells out to, so reaching it means
// something is genuinely wedged rather than slow.
const scanStuck = 90 * time.Second

// recoverStuckScan is the escape hatch for the single-flight flag.
//
// scanning is only cleared when a result arrives, so anything that stops one
// arriving leaves the dashboard silently frozen — no refresh, no error, no way
// back. The subprocess timeouts remove the likely causes; this covers the ones
// nobody thought of.
//
// Recovery cannot simply clear the flag. The stuck scan is still running, and
// starting another against the same Index is the concurrent map write that
// single-flight exists to prevent. So the abandoned scan is disowned instead:
// the generation moves on, which makes its eventual result stale and ignored,
// and it is left with the old Index while a fresh one is installed. Losing the
// parse cache costs one slow scan; sharing it costs a crash.
func (m *Model) recoverStuckScan() tea.Cmd {
	if !m.scanning || time.Since(m.scanStart) < scanStuck {
		return nil
	}
	m.scanGen++
	m.ix = session.NewIndex()
	m.scanning = false
	// The one moment worth mentioning the goroutine dump: something inside
	// orbit has stopped responding, and this is as close as it gets to
	// catching it while it is still wedged.
	msg := "a scan stopped responding after " +
		format.Itoa(int(time.Since(m.scanStart).Seconds())) + "s — restarting it"
	if m.dumpPath != "" {
		msg += "; kill -USR1 for stacks in " + m.dumpPath
	}
	m.say(msg)
	return m.scanCmd()
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
func scan(gen int, ix *session.Index) tea.Msg {
	sessions := ix.Scan()
	// One read of the dispatch directory for the whole scan, rather than a
	// lookup per session: there are a handful of dispatches and hundreds of
	// sessions. Joining here is the UI's job — it is the layer allowed to know
	// about both an agent and the machinery around it.
	dispatches := dispatch.Active()
	byID := map[string]*session.Session{}
	for _, s := range sessions {
		// Sessions come from a cache and are reused across ticks, so last
		// tick's tmux link has to be cleared or a killed session stays "live".
		s.Tmux = nil
		s.Dispatch = dispatches[dispatch.Key(s.Agent.String(), s.ID)]
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
		best := unlinkedCandidate(t, sessions, claimed)
		if best != nil {
			best.Tmux = t
			claimed[best.ID] = true
			tmux.Link(t.Name, best.ID)
			tmux.Retitle(t.Name, best.TabTitle())
		} else if pending := pendingFromTmux(t); pending != nil {
			// A freshly started agent has no transcript until its first prompt.
			// The tmux session is still real and controllable, so keep it in the
			// dashboard rather than making it disappear during that gap.
			sessions = append(sessions, pending)
		}
	}

	now := time.Now()
	for _, s := range sessions {
		s.Resolve(now)
	}
	return scanMsg{gen: gen, sessions: sessions}
}

// capture refreshes the preview for whatever is selected.
//
// The live stream is preferred and capture-pane is the fallback, not the other
// way round: a poll forks a process, arrives up to a tick late, and loses
// anything that appeared and scrolled away in between. The fallback still
// matters — control mode needs a pty and a tmux that cooperates, and neither
// is worth making the dashboard conditional on.
func (m *Model) capture() tea.Cmd {
	name := m.embedded
	if name == "" {
		s := m.sel()
		if s == nil || s.Tmux == nil {
			return func() tea.Msg { return previewMsg{} }
		}
		name = s.Tmux.Name
	}
	if name == "" {
		return func() tea.Msg { return previewMsg{} }
	}

	switch {
	case m.stream != nil:
		return m.followCmd(name)
	case m.streamOpening:
		// A dial is already in flight. Keep polling so the pane stays live
		// until it lands, but do not start a second client.
		return func() tea.Msg { return previewMsg{name, tmux.Capture(name, 60)} }
	case !m.streamOff:
		m.streamOpening = true
		return m.openStreamCmd(name)
	}
	return func() tea.Msg { return previewMsg{name, tmux.Capture(name, 60)} }
}

// openStreamCmd dials the control client. It runs off the UI goroutine because
// it spawns a process and waits for a handshake.
func (m *Model) openStreamCmd(name string) tea.Cmd {
	// Show something immediately rather than a blank pane while dialling.
	poll := func() tea.Msg { return previewMsg{name, tmux.Capture(name, 60)} }
	return tea.Batch(poll, func() tea.Msg {
		p, err := pane.Open(name)
		if err != nil {
			return paneOpenMsg{err: err.Error()}
		}
		return paneOpenMsg{p: p}
	})
}

// followCmd moves the existing client onto the selected session. Switching
// costs a command rather than a process, which is the reason one connection
// serves the whole dashboard.
func (m *Model) followCmd(name string) tea.Cmd {
	p := m.stream
	if p.Session() == name {
		// The cache may have been cleared while the cursor visited a dormant
		// session. An idle pane emits no Dirty event, so waiting for output here
		// would leave its preview blank indefinitely.
		return func() tea.Msg { return paneSnapshotMsg{name: name} }
	}
	return func() tea.Msg {
		if err := p.Switch(name); err != nil {
			return statusMsg("preview: " + err.Error())
		}
		// Switching marks the Pane dirty, so the one existing wait chain will
		// re-arm itself. This command only publishes the freshly seeded screen;
		// returning paneDirtyMsg here would create a second permanent waiter on
		// every cursor movement.
		return paneSnapshotMsg{name: name}
	}
}

// livePane is what the Model needs from a streaming pane.
//
// An interface rather than *pane.Pane so the failure paths can be exercised
// without a tmux server — a connection that dies mid-session is precisely
// where this went wrong, and precisely what a real pane makes hard to arrange.
type livePane interface {
	Session() string
	Text() string
	Render() string
	Switch(string) error
	Resize(int, int) error
	SendKeyTo(string, string) error
	SendTextTo(string, string) error
	SendWheelTo(string, int, int, pane.WheelDirection) error
	Scrolled() bool
	FollowTail()
	Dirty() <-chan struct{}
	Done() <-chan struct{}
	Close() error
}

// waitForPaneCmd blocks until the screen changes, or until there will never be
// another change. Re-issued on each wakeup, this is what replaces polling: the
// redraw rate follows the output rather than a timer.
//
// Waiting on Dirty alone would be a trap. A control client that dies sends one
// final wakeup and then nothing, so the next wait blocks forever on a screen
// that cannot move — and because the Model still holds a stream, capture never
// falls back to polling. The preview would stay frozen on its last frame for
// the rest of the session, with no error and nothing to notice.
func waitForPaneCmd(p livePane) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-p.Dirty():
			return paneDirtyMsg{}
		case <-p.Done():
			return paneGoneMsg{}
		}
	}
}

// saveSort and saveGroup keep what `o` and `p` did.
//
// These two are the only settings you can change from the keyboard, and until
// now the change was thrown away on quit — you found the arrangement you
// wanted in the UI and then had to reproduce it by editing a file. The value
// is captured rather than read inside the command, because by the time it runs
// the cursor may well have moved on.
func (m *Model) saveSort() tea.Cmd {
	mode := m.sort.String()
	return save("sort order", func() error { return config.SetString("", "sort", mode) })
}

func (m *Model) saveGroup() tea.Cmd {
	mode := m.group
	return save("grouping", func() error {
		if err := config.SetBool("", "group", mode != groupNone); err != nil {
			return err
		}
		// Keep the last concrete mode even while grouping is off, so the two
		// settings remain meaningful when edited directly as well as by `p`.
		if mode != groupNone {
			return config.SetString("", "group_by", mode.String())
		}
		return nil
	})
}

// save runs a config write off the UI goroutine. A failure is worth saying —
// silently not keeping a preference is how you learn to distrust the feature —
// but it is never worth refusing the keypress over.
func save(what string, write func() error) tea.Cmd {
	return func() tea.Msg {
		if err := write(); err != nil {
			return statusMsg("could not save " + what + ": " + err.Error())
		}
		return nil
	}
}

// resetPrompt puts the shared text input back to being the quick filter.
//
// The task textinput is shared by the quick filter, full-text search and
// dispatch composer. Each sets its own prompt, placeholder and length limit. Undoing
// that in one place is what stops the next `/` inheriting the last dispatch's
// 4000-character budget and the word "task" as its placeholder.
func (m *Model) resetPrompt() {
	m.searching = false
	m.dispatching = false
	m.dispatchDirFocused = false
	m.dispatchDirCursor = -1
	m.dispatchDir.Blur()
	m.filter.Prompt = "/"
	m.filter.Placeholder = "filter"
	m.filter.CharLimit = 80
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
	m.view = groupSessions(m.view, m.group)
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
	if pendingSession(s) || !m.cfg.Summary.Enabled || !m.cfg.Summary.Auto {
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
	if pendingSession(s) {
		m.say("send the first prompt before summarising this session")
		return nil
	}
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
		if pendingSession(s) {
			continue
		}
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
		var cmds []tea.Cmd
		if m.icons == IconLogo && !m.logosSent {
			m.logosSent = true
			cmds = append(cmds, logosCmd())
		}
		if cmd := m.resizeEmbeddedCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)

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
		if cmd := m.recoverStuckScan(); cmd != nil {
			return m, tea.Batch(cmd, m.capture(), tick())
		}
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
		// A result the watchdog already gave up on: its Index has been
		// replaced and a newer scan is in flight, so this is stale by
		// definition and must not clear the flag guarding that one.
		if msg.gen != m.scanGen {
			return m, nil
		}
		if m.preparing != nil {
			// Startup is one visual transaction. Applying a scan here would
			// reorder the selected row and change header counts as soon as tmux
			// exists, one frame before the prepared terminal is revealed.
			m.scanning = false
			m.rescan = false
			return m, nil
		}
		wasIdle := !m.anyWorking()
		m.scanning = false
		m.all = msg.sessions
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
		if len(m.pending) > 0 || m.updating || m.preparing != nil || m.anyWorking() {
			return m, cmd
		}
		return m, nil

	case previewMsg:
		// A poll result is only allowed to fill a gap. Once the stream is up it
		// is strictly fresher, and letting a late capture-pane land on top of it
		// would make the preview flicker backwards in time.
		if m.stream != nil && msg.name == m.stream.Session() {
			return m, nil
		}
		m.preview, m.previewName = msg.text, msg.name
		return m, nil

	case paneOpenMsg:
		m.streamOpening = false
		if msg.err != "" {
			if m.preparing != nil && m.embedded != "" {
				// The agent is safely running in tmux, but Orbit cannot make the
				// pane interactive without its control-mode PTY. Return to the
				// dashboard instead of leaving a preparation screen spinning.
				m.preparing = nil
				m.embedded, m.embeddedCwd, m.embeddedName = "", "", ""
				m.terminalFocused = false
				m.terminalZoom = false
				m.terminalW, m.terminalH = 0, 0
				m.streamOff = true
				m.say("session is running in tmux, but the live pane could not open: " + msg.err)
				return m, nil
			}
			// One attempt, then stay on polling for the rest of the session.
			// This is a capability difference, not a fault, so it goes to the
			// status line rather than being presented as an error.
			m.streamOff = true
			m.say("live preview unavailable, polling instead: " + msg.err)
			return m, nil
		}
		m.stream = msg.p
		if m.embedded != "" && msg.p.Session() == m.embedded {
			return m, tea.Batch(waitForPaneCmd(msg.p), m.resizeEmbeddedCmd())
		}
		return m, tea.Batch(waitForPaneCmd(msg.p), m.capture())

	case paneReadyMsg:
		revealed := false
		if msg.err != "" {
			m.say("live pane: " + msg.err)
		}
		if m.stream != nil && m.stream.Session() == msg.name {
			m.preview, m.previewName = m.stream.Text(), msg.name
		}
		if m.preparing != nil && m.preparing.name == msg.name {
			p := m.preparing
			if msg.err != "" {
				m.preparing = nil
				// A failed switch may leave the shared stream showing its old
				// session. Do not present that screen under the new session's name.
				m.embedded, m.embeddedCwd, m.embeddedName = "", "", ""
				m.terminalFocused = false
				m.terminalZoom = false
				m.terminalW, m.terminalH = 0, 0
				return m, nil
			}
			if p.fresh {
				m.addPendingSession(p.agent, p.name, p.cwd)
			} else {
				m.addResumedSession(driveReadyMsg{
					name: p.name, cwd: p.cwd, id: p.id, title: p.title, agent: p.agent,
				})
			}
			m.preparing = nil
			revealed = true
			m.say(p.agent.String() + " is ready in tmux — Tab returns to sessions")
		}
		if revealed {
			return m, tea.Batch(m.pumpPaneInput(), m.refreshCmd())
		}
		return m, m.pumpPaneInput()

	case paneInputDoneMsg:
		m.inputSending = false
		if msg.err != "" {
			m.say("could not send input: " + msg.err)
		}
		return m, m.pumpPaneInput()

	case paneDirtyMsg:
		if m.stream == nil {
			return m, nil
		}
		m.preview, m.previewName = m.stream.Text(), m.stream.Session()
		return m, waitForPaneCmd(m.stream)

	case paneSnapshotMsg:
		if m.stream == nil || m.stream.Session() != msg.name {
			return m, nil
		}
		m.preview, m.previewName = m.stream.Text(), msg.name
		return m, nil

	case paneGoneMsg:
		if m.stream == nil {
			return m, nil
		}
		// Keep the last screen it managed rather than blanking the pane, then
		// let go of it so capture starts polling again.
		m.preview, m.previewName = m.stream.Text(), m.stream.Session()
		m.stream.Close()
		m.stream = nil
		// Not latched off: a client dies when the tmux server goes away, and
		// dialling again is how the preview comes back if it returns. A server
		// that is really gone makes the next dial fail, which does latch — so
		// this recovers without being able to spin.
		return m, m.capture()

	case statusMsg:
		m.say(string(msg))
		return m, m.refreshCmd()

	case readyMsg:
		// Freshly resumed, so nothing can be attached yet — nothing to focus.
		return m, m.attach(msg.name, msg.cwd, focusTarget{}, msg.mode)

	case startedMsg:
		if m.preparing != nil && m.preparing.fresh && m.preparing.agent == msg.agent && m.preparing.cwd == msg.cwd {
			m.preparing.name = msg.name
		} else {
			m.addPendingSession(msg.agent, msg.name, msg.cwd)
		}
		return m, m.focusEmbedded(msg.name, msg.cwd, "new "+msg.agent.String())

	case startFailedMsg:
		if m.preparing != nil && m.preparing.fresh {
			m.preparing = nil
		}
		m.say("start failed: " + msg.err)
		return m, nil

	case driveReadyMsg:
		if m.preparing != nil && m.preparing.id == msg.id {
			m.preparing.name = msg.name
		}
		return m, m.focusEmbedded(msg.name, msg.cwd, msg.title)

	case driveFailedMsg:
		if m.preparing != nil && m.preparing.id == msg.id {
			m.preparing = nil
		}
		m.say("resume failed: " + msg.err)
		return m, nil

	case dispatchedMsg:
		// Deliberately no attach. The point of a dispatch is that it runs
		// without a terminal; the row appears in the list within a tick or two
		// and the live preview shows it working if you sit on it.
		if msg.err != "" {
			m.say("dispatch failed: " + msg.err)
			return m, nil
		}
		m.say(msg.agent + " is working on it in " + msg.cwd)
		return m, m.refreshCmd()

	case updateFoundMsg:
		return m, m.updateApplyCmd(msg.version)

	case updateDoneMsg:
		m.updating = false
		if msg.err != "" {
			// Nothing is broken — this orbit still runs. Say so and move on.
			m.say("update to " + msg.version + " failed: " + msg.err)
			return m, nil
		}
		m.relaunch = msg.exe
		m.say("updated to " + msg.version + " — restarting…")
		return m, tea.Tick(restartPause, func(time.Time) tea.Msg { return relaunchMsg{} })

	case relaunchMsg:
		// Quit cleanly so Bubble Tea puts the terminal back; main execs the
		// new binary once the program has returned.
		return m, tea.Quit

	case tea.PasteMsg:
		if m.terminalFocused && m.embedded != "" {
			// Preserve bracketed-paste semantics so a multi-line paste lands in
			// the agent's editor instead of submitting one command per line.
			return m, m.queuePaneInput(paneInput{
				target: m.embedded,
				text:   "\x1b[200~" + msg.Content + "\x1b[201~",
			})
		}
		if m.dispatching {
			return m, m.updateDispatchInput(msg)
		}
		if m.filtering {
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			if !m.searching {
				m.rebuild()
			}
			return m, cmd
		}
		return m, nil

	case tea.MouseWheelMsg:
		x, y, ok := m.terminalMousePosition(msg.X, msg.Y)
		if !m.terminalFocused || m.embedded == "" || m.preparing != nil || !ok {
			return m, nil
		}
		var direction pane.WheelDirection
		switch msg.Button {
		case tea.MouseWheelUp:
			direction = pane.WheelUp
		case tea.MouseWheelDown:
			direction = pane.WheelDown
		default:
			return m, nil
		}
		return m, m.queuePaneInput(paneInput{
			target: m.embedded, wheel: direction, x: x, y: y,
		})

	case tea.KeyPressMsg:
		if m.terminalFocused && m.embedded != "" {
			// The pane does not own input until the control-mode PTY has been
			// switched and sized. This also makes repeated Tab harmless during
			// the short tmux-to-pane handoff.
			if m.preparing != nil {
				return m, nil
			}
			switch msg.String() {
			case "tab", "ctrl+g":
				return m, m.leaveEmbedded()
			case "ctrl+f":
				m.terminalZoom = !m.terminalZoom
				m.terminalW, m.terminalH = 0, 0
				return m, m.resizeEmbeddedCmd()
			case "ctrl+e":
				m.terminalWide = !m.terminalWide
				m.terminalW, m.terminalH = 0, 0
				return m, m.resizeEmbeddedCmd()
			}
			if input, ok := paneInputFor(msg); ok {
				input.target = m.embedded
				return m, m.queuePaneInput(input)
			}
			return m, nil
		}
		if m.dispatching {
			return m, m.dispatchKey(msg)
		}
		if m.filtering {
			switch msg.String() {
			case "esc":
				m.filtering = false
				m.filter.SetValue("")
				m.filter.Blur()
				m.resetPrompt()
				m.rebuild()
			case "enter":
				m.filtering = false
				m.filter.Blur()
				if m.searching {
					q := strings.TrimSpace(m.filter.Value())
					m.filter.SetValue("")
					m.resetPrompt()
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
					m.rebuild() // quick filter is live; the other two wait for enter
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
	if m.showHelp {
		switch msg.String() {
		case "?", "esc":
			m.showHelp = false
		case "q", "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.view)-1 {
			m.unmountEmbedded()
			m.cursor++
		}
		return m, m.capture()
	case "k", "up":
		if m.cursor > 0 {
			m.unmountEmbedded()
			m.cursor--
		}
		return m, m.capture()
	case "g", "home":
		if m.cursor != 0 {
			m.unmountEmbedded()
		}
		m.cursor = 0
		return m, m.capture()
	case "G", "end":
		if m.cursor != max(0, len(m.view)-1) {
			m.unmountEmbedded()
		}
		m.cursor = max(0, len(m.view)-1)
		return m, m.capture()
	case "?":
		m.showHelp = true
		return m, nil
	case "/":
		m.unmountEmbedded()
		m.resetPrompt()
		m.filtering = true
		m.filter.Placeholder = "filter titles and paths"
		m.filter.Focus()
		return m, textinput.Blink
	case "f":
		m.unmountEmbedded()
		m.resetPrompt()
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
		return m, m.saveSort()
	case "p":
		m.group = m.group.next()
		m.rebuild()
		if m.group == groupNone {
			m.say("grouping off")
		} else {
			m.say("grouped by " + m.group.String())
		}
		return m, m.saveGroup()
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
	case "tab":
		return m, m.enterEmbedded()
	case "x":
		return m, m.kill()
	case "n":
		s := m.sel()
		if s == nil {
			return m, nil
		}
		return m, m.spawn(s.Agent, s.Cwd)
	case "d":
		ag, cwd := m.dispatchTarget()
		m.dispatchTo, m.dispatchInto = ag, cwd
		m.dispatching, m.filtering = true, true
		m.dispatchDirFocused = false
		m.dispatchDirCursor = -1
		m.status = ""
		m.filter.Prompt = "› "
		m.filter.Placeholder = "@codex can you check feature X?"
		// Long enough for a real brief. The quick filter's 80 is a sensible
		// cap on a substring match and an absurd one on a task description.
		m.filter.CharLimit = 4000
		m.filter.SetValue("")
		m.filter.Focus()
		m.dispatchDir.Prompt = "› "
		m.dispatchDir.SetValue(cwd)
		m.dispatchDir.Blur()
		return m, textinput.Blink
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
	if m.preparing != nil && m.preparing.id == s.ID && m.preparing.agent == s.Agent {
		m.say("this session is already being prepared for the live pane")
		return nil
	}
	if s.Tmux != nil {
		// A session that's already on screen somewhere shouldn't get a second
		// tab; pass what identifies its tab — the id recorded when it was
		// opened, and the title it carries now — so attach can switch to it.
		var focus focusTarget
		if alreadyOpen(s, mode.resolve()) {
			focus = focusTarget{id: s.Tmux.TabID, title: s.TabTitle()}
		}
		return m.attach(s.Tmux.Name, s.Cwd, focus, mode)
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
	if m.preparing != nil {
		m.say("already preparing " + m.preparing.agent.String() + " for the live pane")
		return nil
	}
	m.preparing = &drivePreparation{
		cwd: cwd, title: "new " + ag.String(), agent: ag, fresh: true, started: time.Now(),
	}
	// The new transcript does not exist yet. Remember every conversation that
	// could otherwise be mistaken for it; the tmux session carries this set
	// until a scan sees exactly one genuinely new candidate.
	var known []string
	for _, s := range m.all {
		if s.Agent == ag && s.Cwd == cwd && !pendingSession(s) {
			known = append(known, s.ID)
		}
	}
	start := func() tea.Msg {
		name, err := newSession(ag, cwd, known)
		if err != nil {
			return startFailedMsg{err: err.Error()}
		}
		return startedMsg{name: name, cwd: cwd, agent: ag}
	}
	return tea.Batch(start, m.spin.Tick)
}

// focusTarget identifies the tab a session is already showing in: the tab id
// recorded when orbit opened it (often empty — inherited sessions, terminals
// without ids) and the tab title it carries now. Zero value means "nothing to
// focus, just open".
type focusTarget struct{ id, title string }

func (f focusTarget) set() bool { return f.id != "" || f.title != "" }

// attach hands the session to a tab, a window, or this very terminal. The
// in-place path suspends orbit and restores it when you detach, so it works in
// any terminal and needs no permissions — it's the fallback everywhere tab
// and window spawning aren't available.
//
// When focus is set the session is already attached somewhere: switch to its
// tab instead of opening another. Focusing is best-effort — the client might
// be in a terminal we can't script, or the tab may have just closed — so a
// miss falls through to opening as asked, which is never worse than what
// happened before.
func (m *Model) attach(name, cwd string, focus focusTarget, mode attachMode) tea.Cmd {
	argv := tmux.AttachArgv(name)
	switch mode.resolve() {
	case attachTab:
		return func() tea.Msg {
			if focus.set() && term.Focus(focus.id, focus.title) == nil {
				return statusMsg("switched to the tab already showing " + name)
			}
			id, err := term.OpenTab(argv, cwd)
			if err != nil {
				return statusMsg("tab failed: " + err.Error())
			}
			if id != "" {
				tmux.SetTab(name, id)
			}
			return statusMsg("attached " + name + " in a new tab")
		}
	case attachWindow:
		return func() tea.Msg {
			if focus.set() && term.Focus(focus.id, focus.title) == nil {
				return statusMsg("switched to the window already showing " + name)
			}
			id, err := term.OpenWindow(argv, cwd)
			if err != nil {
				return statusMsg("window failed: " + err.Error())
			}
			if id != "" {
				tmux.SetTab(name, id)
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

// Relaunch is the binary to exec once the program has exited, or "" to just
// quit. Set when an update installed successfully: the process running now is
// the old version, and only an exec makes the new one take effect.
func (m *Model) Relaunch() string { return m.relaunch }

// Warn seeds the status line before the first frame, for startup notices —
// stderr would be wiped the moment the alt screen comes up.
func (m *Model) Warn(s string) {
	if s != "" {
		m.say(s)
	}
}

// Close releases the notifier's handle on the terminal.
func (m *Model) Close() {
	// The control client is a process holding a pty. Leaving it behind would
	// keep a client attached to the tmux server after orbit has gone.
	if m.stream != nil {
		m.stream.Close()
		m.stream = nil
	}
	m.notify.Close()
}

// SetDumpPath records where a goroutine dump would land, so a wedged dashboard
// can tell you how to get one out of it. See internal/debug.
func (m *Model) SetDumpPath(p string) { m.dumpPath = p }

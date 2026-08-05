package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sadrig91/orbit/internal/dispatch"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"
	"github.com/sadrig91/orbit/internal/tmux"
)

// Dispatching a session: you name a task, orbit starts an agent on it with no
// terminal attached, and the run appears in the list like anything else.
//
// The runner is orbit itself — `orbit dispatch <id>` — started inside a tmux
// session on orbit's private server, exactly the way an interactive agent is.
// That choice buys four things at once and is the reason dispatch needed
// almost no new machinery: the run survives quitting the dashboard, `x` kills
// it, the live preview shows it working, and tmux.List already reports which
// session it belongs to. A detached process would have needed all four built.
//
// The runner ends by killing its own tmux session. That is what makes a
// finished dispatch resolves to "finished" and Enter resumes the conversation
// interactively, rather than attaching you to a dead shell — see
// session.Resolve, which consults the dispatch record before it looks at tmux.

// dispatchedMsg reports a dispatch that has been started, or failed to start.
type dispatchedMsg struct {
	agent, cwd, err string
}

// dispatch starts a headless run of ag in cwd on the given task.
//
// The prompt never touches a shell command line. tmux.Spawn types what it is
// given into an interactive shell, and a prompt is arbitrary user text —
// newlines, quotes, backticks, `$(…)`. So the whole job is written to a record
// file first and only the id is typed, which is orbit's own and matches
// [a-zA-Z0-9-]. The same reasoning as the hook command in internal/hooks,
// applied to something far more dangerous than a path.
func (m *Model) dispatch(ag session.Agent, cwd, prompt string) tea.Cmd {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	if !ag.Installed() {
		return func() tea.Msg { return statusMsg(ag.String() + " is not installed") }
	}
	if ag == session.Copilot && !m.cfg.Dispatch.CopilotAllowAllTools {
		return func() tea.Msg {
			return statusMsg("copilot can only be dispatched with every tool allowed — " +
				"set dispatch.copilot_allow_all_tools if that is what you want")
		}
	}

	rec := &dispatch.Record{
		ID:      dispatch.NewID(),
		Agent:   ag.String(),
		Cwd:     cwd,
		Prompt:  prompt,
		Status:  dispatch.Running,
		Started: time.Now(),
	}
	// Claude and copilot let orbit choose the conversation id up front, so the
	// run is joined to a session from its first tick. Codex only reveals its
	// thread_id on the first event, and the runner fills it in then.
	if ag != session.Codex {
		rec.SessionID = rec.ID
	}

	m.say("dispatching " + ag.String() + " in " + cwd + "…")
	return func() tea.Msg {
		if err := startDispatch(rec); err != nil {
			return dispatchedMsg{agent: rec.Agent, cwd: cwd, err: err.Error()}
		}
		return dispatchedMsg{agent: rec.Agent, cwd: cwd}
	}
}

// startDispatch writes the record and puts a runner in front of it.
func startDispatch(rec *dispatch.Record) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// A `go run` binary is deleted the moment the run that built it exits, and
	// this one is about to be typed into a shell that will start seconds later
	// and outlive the dashboard. hooks.ensureClaudeSettings refuses the same
	// path for the same reason.
	if strings.HasPrefix(exe, os.TempDir()) {
		return errors.New("this orbit was built by `go run`, so there is no binary to dispatch with")
	}

	name := tmux.UniqueName(dispatchName(rec))
	rec.Tmux = name
	if err := dispatch.Save(rec); err != nil {
		return err
	}
	cmd := shellQuote(exe) + " dispatch " + rec.ID
	// The session id is set on the tmux session up front where there is one,
	// so the scan links the run to its transcript the instant that transcript
	// appears, rather than falling back to guessing by directory.
	if err := tmux.Spawn(name, rec.Cwd, cmd, dispatchTitle(rec), rec.Agent, rec.SessionID); err != nil {
		rec.Status, rec.Err, rec.Ended = dispatch.Failed, err.Error(), time.Now()
		dispatch.Save(rec)
		return err
	}
	return nil
}

// dispatchName is the tmux session name, in the same shape as an interactive
// one so the two sort and read alike.
func dispatchName(rec *dispatch.Record) string {
	base := strings.NewReplacer("/", "-", ".", "_", " ", "_").Replace(shortDir(rec.Cwd))
	tag := "cl"
	switch rec.Agent {
	case "codex":
		tag = "cx"
	case "copilot":
		tag = "cp"
	}
	return tag + "-" + base + "-d"
}

func dispatchTitle(rec *dispatch.Record) string {
	return shortDir(rec.Cwd) + " · ⇢ " + format.Truncate(format.FirstLine(rec.Prompt), 44)
}

// shortDir is Session.ShortCwd for a directory that has no session yet.
func shortDir(cwd string) string {
	stub := &session.Session{Cwd: cwd}
	return stub.ShortCwd()
}

// shellQuote makes a string safe on a shell command line. A copy of the one in
// internal/hooks rather than an export of it: both are three lines, and the
// alternative is a dependency from the UI on the hooks package for a string
// utility.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// dispatchTarget supplies defaults for the composer. They are deliberately
// only defaults: the directory field and a leading @agent mention can replace
// either one before the task starts.
func (m *Model) dispatchTarget() (session.Agent, string) {
	s := m.sel()
	if s != nil {
		return s.Agent, s.Cwd
	}
	ag := session.Claude
	for _, candidate := range session.AllAgents {
		if candidate.Installed() {
			ag = candidate
			break
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd, _ = os.UserHomeDir()
	}
	return ag, cwd
}

// parseDispatchPrompt reads an optional agent mention from the start of a
// task. Keeping the selector in the task line makes the common action one
// field and one Enter, while the composer still shows the fallback clearly.
func parseDispatchPrompt(value string, fallback session.Agent) (session.Agent, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, "", errors.New("describe the task before dispatching")
	}
	first := value
	if i := strings.IndexAny(value, " \t\r\n"); i >= 0 {
		first = value[:i]
	}
	if !strings.HasPrefix(first, "@") {
		return fallback, value, nil
	}

	var ag session.Agent
	switch strings.ToLower(first) {
	case "@claude":
		ag = session.Claude
	case "@codex":
		ag = session.Codex
	case "@copilot":
		ag = session.Copilot
	default:
		return fallback, "", fmt.Errorf("unknown agent %s — use @claude, @codex, or @copilot", first)
	}
	prompt := strings.TrimSpace(value[len(first):])
	if prompt == "" {
		return ag, "", fmt.Errorf("add a task after %s", first)
	}
	return ag, prompt, nil
}

// resolveDispatchDir applies ordinary shell-like path rules without invoking
// a shell: ~ means home, and relative paths start from Orbit's own cwd.
func resolveDispatchDir(value, base string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("choose a directory before dispatching")
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	} else if strings.HasPrefix(value, "~") {
		return "", errors.New("only ~ and ~/path home shortcuts are supported")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	value = filepath.Clean(value)
	info, err := os.Stat(value)
	if err != nil {
		return "", fmt.Errorf("directory %q: %w", value, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", value)
	}
	return value, nil
}

func (m *Model) updateDispatchInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if m.dispatchDirFocused {
		m.dispatchDir, cmd = m.dispatchDir.Update(msg)
		m.dispatchDirCursor = -1
	} else {
		m.filter, cmd = m.filter.Update(msg)
	}
	return cmd
}

func (m *Model) focusDispatchDirectory(on bool) tea.Cmd {
	m.dispatchDirFocused = on
	if on {
		m.filter.Blur()
		m.dispatchDirCursor = -1
		for i, dir := range m.dispatchDirectories() {
			if dir == m.dispatchDir.Value() {
				m.dispatchDirCursor = i
				break
			}
		}
		return m.dispatchDir.Focus()
	}
	m.dispatchDir.Blur()
	return m.filter.Focus()
}

// dispatchDirectories supplies the inline picker in recent-session order.
// The captured default stays first even if the list re-sorts while composing.
func (m *Model) dispatchDirectories() []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "." || dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	add(m.dispatchInto)
	for _, s := range m.view {
		add(s.Cwd)
	}
	for _, s := range m.all {
		add(s.Cwd)
	}
	return dirs
}

func (m *Model) moveDispatchDirectory(delta int) {
	choices := m.dispatchDirectories()
	if len(choices) == 0 {
		return
	}
	if m.dispatchDirCursor < 0 {
		m.dispatchDirCursor = 0
	} else {
		m.dispatchDirCursor = (m.dispatchDirCursor + delta + len(choices)) % len(choices)
	}
	m.dispatchDir.SetValue(choices[m.dispatchDirCursor])
	m.dispatchDir.CursorEnd()
}

func (m *Model) dispatchKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.filtering = false
		m.filter.SetValue("")
		m.dispatchDir.SetValue("")
		m.filter.Blur()
		m.resetPrompt()
		m.rebuild()
		return nil
	case "tab", "shift+tab":
		return m.focusDispatchDirectory(!m.dispatchDirFocused)
	case "up":
		if m.dispatchDirFocused {
			m.moveDispatchDirectory(-1)
			return nil
		}
		return m.updateDispatchInput(msg)
	case "down":
		if m.dispatchDirFocused {
			m.moveDispatchDirectory(1)
			return nil
		}
		return m.updateDispatchInput(msg)
	case "enter":
		if m.dispatchDirFocused {
			return m.focusDispatchDirectory(false)
		}
		ag, task, err := parseDispatchPrompt(m.filter.Value(), m.dispatchTo)
		if err != nil {
			m.say(err.Error())
			return nil
		}
		base, err := os.Getwd()
		if err != nil {
			base = m.dispatchInto
		}
		cwd, err := resolveDispatchDir(m.dispatchDir.Value(), base)
		if err != nil {
			m.say(err.Error())
			return m.focusDispatchDirectory(true)
		}
		m.filtering = false
		m.filter.SetValue("")
		m.dispatchDir.SetValue("")
		m.filter.Blur()
		m.resetPrompt()
		return m.dispatch(ag, cwd, task)
	default:
		return m.updateDispatchInput(msg)
	}
}

// dispatchLine is the one-line account of a dispatch for the detail pane.
func dispatchLine(d *dispatch.Record) (label, detail string) {
	switch d.Status {
	case dispatch.Running:
		detail = d.Activity
		if detail == "" {
			detail = "starting…"
		}
		return "dispatched · working", detail
	case dispatch.NeedsYou:
		return "dispatched · stopped for you", d.Pending
	case dispatch.Failed:
		return "dispatched · failed", d.Err
	}
	return "dispatched · done", ""
}

// pruneDispatchCmd sweeps records whose runs finished long ago. Seven days,
// matching the hook state files: long enough that a dispatch you left running
// over a weekend is still explained when you come back, short enough that the
// directory does not become an archive.
func pruneDispatchCmd() tea.Msg {
	dispatch.Prune(7 * 24 * time.Hour)
	return nil
}

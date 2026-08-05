package ui

import (
	"errors"
	"os"
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
// finished dispatch resolve to "your turn" and Enter resume the conversation
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

// dispatchTarget is where a new dispatch goes: the selected session's agent
// and directory, the same rule `n` follows. Dispatch always starts a fresh
// conversation — following up on an existing one means resuming it, which is
// what Enter is for.
func (m *Model) dispatchTarget() (session.Agent, string, bool) {
	s := m.sel()
	if s == nil {
		return 0, "", false
	}
	return s.Agent, s.Cwd, true
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

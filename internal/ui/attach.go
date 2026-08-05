package ui

import (
	"os"
	"os/exec"
	"strings"

	"github.com/sadrig91/orbit/internal/hooks"
	"github.com/sadrig91/orbit/internal/session"
	"github.com/sadrig91/orbit/internal/term"
	"github.com/sadrig91/orbit/internal/tmux"
)

// How a session gets put in front of you.
type attachMode int

const (
	attachSmart  attachMode = iota // a Ghostty tab where possible, otherwise in-place
	attachTab                      // new tab in the current Ghostty window
	attachWindow                   // new Ghostty window
	attachInline                   // hand this terminal over, come back on detach
)

func parseAttachMode(v string) attachMode {
	switch v {
	case "tab":
		return attachTab
	case "window":
		return attachWindow
	case "inline":
		return attachInline
	}
	return attachSmart
}

// Ghostty exports both of these; either alone is enough to be confident.
func isGhostty() bool {
	return os.Getenv("TERM_PROGRAM") == "ghostty" || os.Getenv("GHOSTTY_RESOURCES_DIR") != ""
}

// resolve turns the requested mode into one this machine can actually do.
func (m attachMode) resolve() attachMode {
	switch m {
	case attachSmart, attachTab:
		if term.CanTab() {
			return attachTab
		}
		return attachInline
	case attachWindow:
		if term.CanWindow() {
			return attachWindow
		}
		return attachInline
	}
	return attachInline
}

// alreadyOpen reports whether opening this session would duplicate a tab: a
// client is attached somewhere, and the resolved mode would spawn another
// one. Inline is exempt — attaching a second client in place just mirrors
// the session, and asking for it explicitly is a fine way to do that.
//
// "Attached" here means a terminal someone is looking at. orbit's own live
// preview holds a control client on whichever session is selected, and
// tmux.List excludes those precisely so that browsing a session does not make
// it look open.
func alreadyOpen(s *session.Session, resolved attachMode) bool {
	return s.Tmux != nil && s.Tmux.Attached && resolved != attachInline
}

// attachCommand is the command that puts you inside a session.
func attachCommand(name string) *exec.Cmd {
	return exec.Command("tmux", tmux.Args("attach", "-t", name)...)
}

// resumeSession and newSession compose an agent with tmux. They live here
// rather than in package tmux, which deliberately knows nothing about agents.
//
// Both append the hook injection here rather than inside ResumeCmd/NewCmd,
// because those also feed the detail pane — "⏎ resumes with claude --resume
// <id>" should stay something a person would type, not carry orbit's
// plumbing. Every live session passes through one of these two functions,
// which is what makes the hooks' coverage total.
func resumeSession(s *session.Session) (string, error) {
	// Whatever the state file claims is from a previous run — often one that
	// ended in a kill, where SessionEnd never fired — and a resumed agent at
	// an idle prompt emits nothing until you type, so a stale claim would
	// stand indefinitely. SessionStart re-establishes state when the hooks
	// are live; this covers the runs where they won't be.
	hooks.Forget(s.Agent.String(), s.ID)
	name := tmux.UniqueName(s.TmuxName())
	cmd := s.Agent.ResumeCmd(s.ID) + hooks.SpawnArgs(s.Agent.String())
	err := tmux.Spawn(name, s.Cwd, cmd, s.TabTitle(), s.Agent.String(), s.ID)
	return name, err
}

// newSession starts a fresh agent in cwd. It has no session id yet; the Model
// links it to a transcript once the agent writes one.
func newSession(ag session.Agent, cwd string) (string, error) {
	stub := &session.Session{Agent: ag, Cwd: cwd, Title: "new " + ag.String()}
	base := ag.Tag() + "-" + strings.NewReplacer("/", "-", ".", "_", " ", "_").Replace(stub.ShortCwd())
	name := tmux.UniqueName(base)
	cmd := ag.NewCmd() + hooks.SpawnArgs(ag.String())
	err := tmux.Spawn(name, cwd, cmd, stub.TabTitle(), ag.String(), "")
	return name, err
}

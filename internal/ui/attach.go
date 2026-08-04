package ui

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/sadrig91/orbit/internal/session"
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

// canSpawnTab is macOS-only: it drives Ghostty's own cmd+T through System
// Events because Ghostty has no CLI action for opening a tab there. On Linux
// `ghostty +new-window` exists, but it makes a window rather than a tab, so it
// is handled as the window case instead.
func canSpawnTab() bool { return runtime.GOOS == "darwin" && isGhostty() }

func canSpawnWindow() bool {
	if runtime.GOOS == "darwin" {
		_, err := os.Stat("/Applications/Ghostty.app")
		return err == nil
	}
	_, err := exec.LookPath("ghostty")
	return err == nil
}

// resolve turns the requested mode into one this machine can actually do.
func (m attachMode) resolve() attachMode {
	switch m {
	case attachSmart:
		if canSpawnTab() {
			return attachTab
		}
		return attachInline
	case attachTab:
		if canSpawnTab() {
			return attachTab
		}
		return attachInline
	case attachWindow:
		if canSpawnWindow() {
			return attachWindow
		}
		return attachInline
	}
	return attachInline
}

// attachCommand is the command that puts you inside a session.
func attachCommand(name string) *exec.Cmd {
	return exec.Command("tmux", "-L", tmux.Socket, "-f", tmux.ConfPath(), "attach", "-t", name)
}

// resumeSession and newSession compose an agent with tmux. They live here
// rather than in package tmux, which deliberately knows nothing about agents.
func resumeSession(s *session.Session) (string, error) {
	name := tmux.UniqueName(s.TmuxName())
	err := tmux.Spawn(name, s.Cwd, s.Agent.ResumeCmd(s.ID), s.TabTitle(), s.Agent.String(), s.ID)
	return name, err
}

// newSession starts a fresh agent in cwd. It has no session id yet; the Model
// links it to a transcript once the agent writes one.
func newSession(ag session.Agent, cwd string) (string, error) {
	stub := &session.Session{Agent: ag, Cwd: cwd, Title: "new " + ag.String()}
	base := ag.Tag() + "-" + strings.NewReplacer("/", "-", ".", "_", " ", "_").Replace(stub.ShortCwd())
	name := tmux.UniqueName(base)
	err := tmux.Spawn(name, cwd, ag.NewCmd(), stub.TabTitle(), ag.String(), "")
	return name, err
}

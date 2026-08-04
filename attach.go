package main

import (
	"os"
	"os/exec"
	"runtime"
)

// How a session gets put in front of you.
type attachMode int

const (
	attachSmart  attachMode = iota // a Ghostty tab where possible, otherwise in-place
	attachTab                      // new tab in the current Ghostty window
	attachWindow                   // new Ghostty window
	attachInline                   // hand this terminal over, come back on detach
)

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
	return exec.Command("tmux", "-L", socket, "-f", confPath(), "attach", "-t", name)
}

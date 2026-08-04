package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Everything runs on a private tmux server (-L orbit) with its own config, so
// none of this collides with a plain `tmux` you might start later.
const socket = "orbit"

type Tmux struct {
	Name         string
	SessionID    string // the agent's own session id, or "" until linked
	Agent        Agent
	Attached     bool
	AgentRunning bool
	Activity     time.Time
	Created      time.Time
	Title        string
	Cwd          string
}

func confPath() string { return home(".config", "orbit", "tmux.conf") }

// Both spawn paths type a command into a shell that is still starting up.
// zsh buffers keystrokes typed before its first prompt, so these delays only
// need to clear the window/pane creation itself — but a cold shell here takes
// ~0.6s, so they're generous, and overridable if your machine is slower.
func delay(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func tmuxCmd(args ...string) *exec.Cmd {
	return exec.Command("tmux", append([]string{"-L", socket, "-f", confPath()}, args...)...)
}

const listFmt = "#{session_name}\x1f#{@orbit_session}\x1f#{@orbit_agent}\x1f#{session_attached}\x1f" +
	"#{session_activity}\x1f#{pane_current_command}\x1f#{@orbit_title}\x1f#{session_path}\x1f#{session_created}"

var shells = map[string]bool{
	"zsh": true, "-zsh": true, "bash": true, "-bash": true, "sh": true,
	"fish": true, "login": true, "tmux": true, "": true,
}

// TmuxList returns every live orbit session. Sessions started with `n` have no
// @orbit_session until the agent writes a transcript and the model links them.
func TmuxList() []*Tmux {
	out, err := tmuxCmd("list-sessions", "-F", listFmt).Output()
	if err != nil {
		return nil // no server running yet is the normal case
	}
	var res []*Tmux
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) < 9 {
			continue
		}
		act, _ := strconv.ParseInt(f[4], 10, 64)
		created, _ := strconv.ParseInt(f[8], 10, 64)
		ag := Claude
		for _, a := range AllAgents {
			if a.String() == f[2] {
				ag = a
			}
		}
		res = append(res, &Tmux{
			Name:         f[0],
			SessionID:    f[1],
			Agent:        ag,
			Attached:     f[3] == "1",
			Activity:     time.Unix(act, 0),
			Created:      time.Unix(created, 0),
			AgentRunning: !shells[f[5]],
			Title:        f[6],
			Cwd:          f[7],
		})
	}
	return res
}

// tmuxSpawn creates a detached session and types cmd into it.
//
// The command is typed into an interactive login shell rather than passed to
// new-session, because agents are routinely shell functions or shims rather
// than plain executables — a wrapper that exports a token, a version-manager
// stub — and those don't exist in the bare `sh -c` new-session would use. It
// also leaves you at a usable prompt if the agent exits.
func tmuxSpawn(name, cwd, cmd, title string, ag Agent, sessionID string) error {
	if err := tmuxCmd("new-session", "-d", "-s", name, "-c", cwd, "-x", "220", "-y", "60").Run(); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}
	tmuxCmd("set-option", "-t", name, "@orbit_agent", ag.String()).Run()
	tmuxCmd("set-option", "-t", name, "@orbit_title", title).Run()
	if sessionID != "" {
		tmuxCmd("set-option", "-t", name, "@orbit_session", sessionID).Run()
	}
	time.Sleep(delay("ORBIT_SPAWN_DELAY", 900*time.Millisecond))
	return tmuxCmd("send-keys", "-t", name, cmd, "Enter").Run()
}

// TmuxResume starts an existing session back up in a fresh tmux session.
func TmuxResume(s *Session) (string, error) {
	name := uniqueName(s.TmuxName())
	err := tmuxSpawn(name, s.Cwd, s.Agent.ResumeCmd(s.ID), s.TabTitle(), s.Agent, s.ID)
	return name, err
}

// TmuxNew starts a fresh agent session in cwd. It has no session id yet; the
// model links it to a transcript once the agent writes one.
func TmuxNew(ag Agent, cwd string) (string, error) {
	stub := &Session{Agent: ag, Cwd: cwd, Title: "new " + ag.String()}
	name := uniqueName(ag.Tag() + "-" + strings.NewReplacer("/", "-", ".", "_", " ", "_").Replace(stub.ShortCwd()))
	err := tmuxSpawn(name, cwd, ag.NewCmd(), stub.TabTitle(), ag, "")
	return name, err
}

func uniqueName(base string) string {
	taken := map[string]bool{}
	for _, t := range TmuxList() {
		taken[t.Name] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		n := base + "-" + strconv.Itoa(i)
		if !taken[n] {
			return n
		}
	}
}

func TmuxKill(name string) error { return tmuxCmd("kill-session", "-t", name).Run() }

func TmuxCapture(name string, lines int) string {
	out, err := tmuxCmd("capture-pane", "-p", "-t", name, "-S", "-"+strconv.Itoa(lines)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// TmuxLink records which transcript a session turned out to be writing to.
func TmuxLink(name, sessionID string) {
	tmuxCmd("set-option", "-t", name, "@orbit_session", sessionID).Run()
}

// TmuxRetitle keeps the tab label in sync as the agent renames the session.
func TmuxRetitle(name, title string) {
	tmuxCmd("set-option", "-t", name, "@orbit_title", title).Run()
}

// OpenTab drives Ghostty's own cmd+T through System Events, landing the session
// as a tab in the current window. Ghostty has no CLI action for this on macOS
// (+new-window is Linux-only), so this is the only route that doesn't spawn a
// detached window. Needs Accessibility permission for the terminal.
func OpenTab(name string) error {
	attach := "tmux -L " + socket + " -f " + confPath() + " attach -t " + name
	wait := delay("ORBIT_TAB_DELAY", time.Second).Seconds()
	script := fmt.Sprintf(`
tell application "Ghostty" to activate
delay 0.35
tell application "System Events" to tell process "Ghostty"
	keystroke "t" using command down
	delay %.2f
	keystroke %s
	key code 36
end tell`, wait, applescriptString(attach))
	return exec.Command("osascript", "-e", script).Run()
}

// OpenWindow spawns a detached Ghostty window running the session.
func OpenWindow(name, cwd string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-na", "Ghostty.app", "--args",
			"--working-directory="+cwd, "-e", "tmux", "-L", socket, "-f", confPath(),
			"attach", "-t", name).Run()
	}
	return exec.Command("ghostty", "--working-directory="+cwd,
		"-e", "tmux", "-L", socket, "-f", confPath(), "attach", "-t", name).Start()
}

func applescriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

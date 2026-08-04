package tmux

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sadrig91/orbit/internal/format"
)

// Everything runs on a private tmux server (-L orbit) with its own config, so
// none of this collides with a plain `tmux` you might start later.
const Socket = "orbit"

// Session is a live tmux session. The agent is carried as a plain name: this
// package deliberately knows nothing about agent types or transcripts.
type Session struct {
	Name         string
	SessionID    string // the agent's own session id, or "" until linked
	Agent        string
	Attached     bool
	AgentRunning bool
	Activity     time.Time
	Created      time.Time
	Title        string
	Cwd          string
	TabID        string // terminal tab last opened for it, or "" — see SetTab
}

func ConfPath() string { return format.Home(".config", "orbit", "tmux.conf") }

// Both spawn paths type a command into a shell that is still starting up.
// zsh buffers keystrokes typed before its first prompt, so these delays only
// need to clear the window/pane creation itself — but a cold shell here takes
// ~0.6s, so they're generous, and overridable if your machine is slower.
func Delay(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// Args prefixes the flags every tmux invocation needs, so there is one place
// that decides how orbit talks to tmux.
//
// -u forces UTF-8 output. Without it tmux picks from the locale of whoever
// asked, and a client it doesn't consider UTF-8 gets multi-byte characters
// replaced with "_" on the way out. That mangles the "·" in a tab title, and
// worse: the title orbit writes never compares equal to the title it reads
// back, so every tick re-titles every session, forever.
func Args(rest ...string) []string {
	return append([]string{"-u", "-L", Socket, "-f", ConfPath()}, rest...)
}

// Every tmux invocation is bounded. A wedged server would otherwise block
// whichever scan called it for as long as it stayed wedged, and a scan has no
// way to interrupt a syscall — which is how a dashboard silently stops
// refreshing. run and output own the context so no cancel is left dangling.
const commandTimeout = 10 * time.Second

func withTimeout(fn func(*exec.Cmd) error, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	return fn(exec.CommandContext(ctx, "tmux", Args(args...)...))
}

// run executes a tmux command and discards its output.
func run(args ...string) error {
	return withTimeout(func(c *exec.Cmd) error { return c.Run() }, args...)
}

// output executes a tmux command and returns stdout.
func output(args ...string) ([]byte, error) {
	var out []byte
	err := withTimeout(func(c *exec.Cmd) error {
		var e error
		out, e = c.Output()
		return e
	}, args...)
	return out, err
}

const listFmt = "#{session_name}\x1f#{@orbit_session}\x1f#{@orbit_agent}\x1f#{session_attached}\x1f" +
	"#{session_activity}\x1f#{pane_current_command}\x1f#{@orbit_title}\x1f#{session_path}\x1f#{session_created}\x1f" +
	"#{@orbit_tab}"

// Field separator, and the number of fields listFmt asks for.
const (
	fieldSep   = "\x1f"
	listFields = 10
)

// Not every tmux hands the separator back the way it was sent. 3.4 — what
// Debian and Ubuntu 24.04 ship — escapes control characters in command output,
// so \x1f arrives as the literal four characters \037 and the whole line parses
// as one field. That failure is silent and total: List returns nothing, every
// session resolves to Dormant, and the dashboard shows a wall of `·` with no
// error anywhere. Accept both forms rather than trusting one.
const fieldSepEscaped = `\037`

func splitFields(line string) []string {
	if f := strings.Split(line, fieldSep); len(f) >= listFields {
		return f
	}
	return strings.Split(line, fieldSepEscaped)
}

var shells = map[string]bool{
	"zsh": true, "-zsh": true, "bash": true, "-bash": true, "sh": true,
	"fish": true, "login": true, "tmux": true, "": true,
}

// List returns every live orbit session. Sessions started with `n` have no
// @orbit_session until the agent writes a transcript and the model links them.
func List() []*Session {
	out, err := output("list-sessions", "-F", listFmt)
	if err != nil {
		return nil // no server running yet is the normal case
	}
	return parseList(string(out))
}

func parseList(out string) []*Session {
	var res []*Session
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if s, ok := parseListLine(line); ok {
			res = append(res, s)
		}
	}
	return res
}

func parseListLine(line string) (*Session, bool) {
	f := splitFields(line)
	if len(f) < listFields {
		return nil, false
	}
	act, _ := strconv.ParseInt(f[4], 10, 64)
	created, _ := strconv.ParseInt(f[8], 10, 64)
	return &Session{
		Name:         f[0],
		SessionID:    f[1],
		Agent:        f[2],
		Attached:     f[3] == "1",
		Activity:     time.Unix(act, 0),
		Created:      time.Unix(created, 0),
		AgentRunning: !shells[f[5]],
		Title:        f[6],
		Cwd:          f[7],
		TabID:        f[9],
	}, true
}

// Spawn creates a detached session and types cmd into it.
//
// The command is typed into an interactive login shell rather than passed to
// new-session, because agents are routinely shell functions or shims rather
// than plain executables — a wrapper that exports a token, a version-manager
// stub — and those don't exist in the bare `sh -c` new-session would use. It
// also leaves you at a usable prompt if the agent exits.
func Spawn(name, cwd, cmd, title, agent, sessionID string) error {
	if err := run("new-session", "-d", "-s", name, "-c", cwd, "-x", "220", "-y", "60"); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}
	run("set-option", "-t", name, "@orbit_agent", agent)
	run("set-option", "-t", name, "@orbit_title", title)
	if sessionID != "" {
		run("set-option", "-t", name, "@orbit_session", sessionID)
	}
	time.Sleep(Delay("ORBIT_SPAWN_DELAY", 900*time.Millisecond))
	return run("send-keys", "-t", name, cmd, "Enter")
}

func UniqueName(base string) string {
	taken := map[string]bool{}
	for _, t := range List() {
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

func Kill(name string) error { return run("kill-session", "-t", name) }

func Capture(name string, lines int) string {
	out, err := output("capture-pane", "-p", "-t", name, "-S", "-"+strconv.Itoa(lines))
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// Link records which transcript a session turned out to be writing to.
func Link(name, sessionID string) {
	run("set-option", "-t", name, "@orbit_session", sessionID)
}

// Retitle keeps the tab label in sync as the agent renames the session.
func Retitle(name, title string) {
	run("set-option", "-t", name, "@orbit_title", title)
}

// SetTab records the terminal tab a session was opened into, so a later open
// can switch back to it instead of spawning another. It lives here rather
// than in orbit's own state because the tmux server is exactly as durable as
// the sessions it describes: kill the session and the stale id goes with it.
func SetTab(name, tabID string) {
	run("set-option", "-t", name, "@orbit_tab", tabID)
}

// AttachArgv is the full command line a terminal runs to show a session.
func AttachArgv(name string) []string {
	return append([]string{"tmux"}, Args("attach", "-t", name)...)
}

//go:embed tmux.conf
var defaultConf string

const confMarker = "# orbit —"

// installConf keeps ~/.config/orbit/tmux.conf in sync with the embedded copy,
// but only overwrites a file orbit itself wrote — edits you make by hand stick.
func InstallConf() error {
	path := ConfPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	case string(existing) == defaultConf:
		return nil
	case !strings.HasPrefix(string(existing), confMarker):
		return nil // hand-edited or foreign; leave it alone
	}
	return os.WriteFile(path, []byte(defaultConf), 0o644)
}

// Available reports whether tmux is installed.
func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// KillServerForTest tears down the orbit tmux server. Only integration tests
// should need this; normal teardown is per-session via Kill.
func KillServerForTest() error { return run("kill-server") }

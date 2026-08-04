// Package term drives the terminal emulator around orbit: opening a tmux
// attach in a new tab or window, and switching back to a tab that already
// shows a session. tmux remains the source of truth for what is running;
// this package only decides where it appears on screen.
//
// Everything is best-effort by design. Terminals differ wildly in what they
// expose — Ghostty 1.3 has a real AppleScript dictionary, older Ghostty only
// takes keystrokes, iTerm2 has its own dictionary, Linux has a CLI that can
// make windows but not tabs — so each capability degrades independently and
// the UI falls back to attaching in place when nothing here applies.
package term

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is Focus saying the session's tab no longer exists — closed,
// retitled beyond recognition, or the terminal restarted and forgot its ids.
// Callers treat it as "open a fresh one".
var ErrNotFound = errors.New("no tab found for that session")

// kind is which terminal orbit is driving, decided per call from the
// environment: cheap (env vars and one stat), and it keeps tests able to
// flip the environment without fighting a cached sync.Once.
type kind int

const (
	none          kind = iota
	ghosttyAPI         // Ghostty 1.3+ on macOS: AppleScript dictionary — real ids
	ghosttyLegacy      // older Ghostty on macOS: System Events keystrokes, titles
	ghosttyAway        // macOS, Ghostty installed but orbit runs elsewhere: windows only
	ghosttyLinux       // ghostty CLI: windows only
	iterm              // iTerm2 on macOS: AppleScript dictionary — session ids
)

const ghosttyApp = "/Applications/Ghostty.app"

// ghosttyBundle finds the app bundle of the Ghostty orbit is running inside.
// GHOSTTY_RESOURCES_DIR points into it (…/Ghostty.app/Contents/Resources/…),
// which follows the bundle wherever it's installed; the /Applications default
// covers the not-inside-Ghostty cases.
func ghosttyBundle() string {
	if dir := os.Getenv("GHOSTTY_RESOURCES_DIR"); dir != "" {
		if i := strings.Index(dir, ".app/"); i >= 0 {
			return dir[:i+len(".app")]
		}
	}
	return ghosttyApp
}

// The sdef ships with Ghostty 1.3.0+, so its presence is the version probe.
// `macos-applescript = false` still turns the dictionary off at runtime; that
// surfaces as a script error and falls back to the legacy route per call.
func ghosttySdef() string {
	return ghosttyBundle() + "/Contents/Resources/Ghostty.sdef"
}

func insideGhostty() bool {
	return os.Getenv("TERM_PROGRAM") == "ghostty" || os.Getenv("GHOSTTY_RESOURCES_DIR") != ""
}

func insideITerm() bool {
	return os.Getenv("TERM_PROGRAM") == "iTerm.app" || os.Getenv("LC_TERMINAL") == "iTerm2"
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func detect() kind {
	if runtime.GOOS == "darwin" {
		switch {
		case insideGhostty() && exists(ghosttySdef()):
			return ghosttyAPI
		case insideGhostty():
			return ghosttyLegacy
		case insideITerm():
			return iterm
		case exists(ghosttyApp):
			return ghosttyAway
		}
		return none
	}
	if _, err := exec.LookPath("ghostty"); err == nil {
		return ghosttyLinux
	}
	return none
}

// Preflight checks the surrounding terminal once at startup and returns a
// warning when it is older than what orbit drives best, or "" when there is
// nothing to say. It never blocks: orbit works in any terminal via inline
// attach, so an old Ghostty is a degraded experience, not an error. The
// version comes from TERM_PROGRAM_VERSION, which both Ghostty and iTerm2
// export to their children — but the sdef on disk is what actually gates the
// Ghostty API route, so that is what's checked; the version only makes the
// message concrete.
func Preflight() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	v := os.Getenv("TERM_PROGRAM_VERSION")
	switch {
	case insideGhostty() && !exists(ghosttySdef()):
		if v != "" {
			v = " " + v
		}
		return "Ghostty" + v + " predates the scripting API — tabs need Accessibility and can't be refocused; 1.3+ fixes both"
	case insideITerm() && !versionAtLeast(v, 3):
		return "iTerm2 " + v + " is older than orbit can script — tabs fall back to inline; 3.0+ fixes it"
	}
	return ""
}

// versionAtLeast reports whether a "major.rest…" version string reaches the
// wanted major. Unparseable strings (dev builds, empty) count as new enough:
// the functional probes above are authoritative, and a false alarm on every
// nightly build would teach people to ignore the warning.
func versionAtLeast(v string, major int) bool {
	head, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(head)
	if err != nil {
		return true
	}
	return n >= major
}

// CanTab reports whether a new tab can be opened here. Tabs only make sense
// inside a terminal we can script — a tab spawned into some other app's
// window would land the session where the user isn't looking.
func CanTab() bool {
	switch detect() {
	case ghosttyAPI, ghosttyLegacy, iterm:
		return true
	}
	return false
}

// CanWindow reports whether a new terminal window can be opened here.
func CanWindow() bool { return detect() != none }

// OpenTab runs argv (a full command line, argv[0] included) in a new tab of
// the current window and returns a tab id when the terminal provides one.
// An empty id with a nil error means the tab opened but can't be addressed
// later — the legacy routes work that way.
func OpenTab(argv []string, cwd string) (string, error) {
	switch detect() {
	case ghosttyAPI:
		id, err := ghosttyOpenTab(argv)
		if err == nil {
			return id, nil
		}
		// The dictionary can be off (`macos-applescript = false`) with the
		// sdef still on disk; the keystroke route works regardless.
		if legacyErr := ghosttyOpenTabLegacy(argv); legacyErr == nil {
			return "", nil
		}
		return "", err
	case ghosttyLegacy:
		return "", ghosttyOpenTabLegacy(argv)
	case iterm:
		return itermOpenTab(argv)
	}
	return "", errors.New("no terminal here can open tabs")
}

// OpenWindow runs argv in a new terminal window and returns a tab id when
// the terminal provides one.
func OpenWindow(argv []string, cwd string) (string, error) {
	switch detect() {
	case ghosttyAPI:
		id, err := ghosttyOpenWindow(argv)
		if err == nil {
			return id, nil
		}
		if openErr := ghosttyOpenWindowApp(argv, cwd); openErr == nil {
			return "", nil
		}
		return "", err
	case ghosttyLegacy, ghosttyAway:
		return "", ghosttyOpenWindowApp(argv, cwd)
	case ghosttyLinux:
		return "", ghosttyOpenWindowLinux(argv, cwd)
	case iterm:
		return itermOpenWindow(argv)
	}
	return "", errors.New("no terminal here can open windows")
}

// Focus switches to the tab already showing a session. id is what OpenTab
// returned for it (possibly empty), title is the tab title the session
// carries now; matching wants both because neither alone is trustworthy —
// Ghostty reuses tab ids after a close, and titles can collide between two
// sessions in the same directory. ErrNotFound means no tab matched.
func Focus(id, title string) error {
	switch detect() {
	case ghosttyAPI, ghosttyAway:
		return ghosttyFocus(id, title)
	case ghosttyLegacy:
		return ghosttyFocusLegacy(title)
	case iterm:
		return itermFocus(id, title)
	}
	return ErrNotFound
}

// osascript runs an AppleScript and returns its trimmed output.
func osascript(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		if ee := new(exec.ExitError); errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", errors.New(strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// found is the protocol between the focus scripts and Go: scripts print
// "found" or "missing" so a miss is distinguishable from a script error.
func found(out string, err error) error {
	if err != nil {
		return err
	}
	if out != "found" {
		return ErrNotFound
	}
	return nil
}

// shellCommand renders argv as a line a shell will split back into exactly
// argv. The scripted routes type into an interactive shell rather than exec,
// so every argument is quoted: the tmux -f path sits under the home
// directory, and a macOS account named "First Last" would otherwise split it
// in two and the tab would open to a failed attach.
func shellCommand(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuote wraps s in single quotes, which the shell takes literally. The
// only character needing care is a single quote itself: close the string,
// emit an escaped quote, reopen.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// asString renders s as an AppleScript string literal.
func asString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// delay reads an override duration from the environment, for the legacy
// keystroke route on machines where a cold shell takes longer than the
// generous default.
func delay(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

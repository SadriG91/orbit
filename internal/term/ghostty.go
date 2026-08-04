package term

import (
	"fmt"
	"os/exec"
	"time"
)

// The 1.3+ dictionary routes. The attach command is typed into the tab's
// shell via `initial input` rather than run via `command`: it behaves exactly
// like the keystroke route always did (detach leaves a prompt, exiting the
// shell closes the tab) and a surface whose command exits would otherwise
// linger as a dead 👻 tab rather than close.
//
// `in front window` is required even though the sdef marks it optional —
// without it Ghostty 1.3.1 creates the tab but fails to return it (-1708),
// which is indistinguishable from failure.
func ghosttyOpenTab(argv []string) (string, error) {
	return osascript(fmt.Sprintf(`
tell application "Ghostty"
	activate
	set cfg to new surface configuration
	set initial input of cfg to %s & linefeed
	set t to new tab in front window with configuration cfg
	return id of t
end tell`, asString(shellCommand(argv))))
}

func ghosttyOpenWindow(argv []string) (string, error) {
	return osascript(fmt.Sprintf(`
tell application "Ghostty"
	activate
	set cfg to new surface configuration
	set initial input of cfg to %s & linefeed
	set win to new window with configuration cfg
	return id of (selected tab of win)
end tell`, asString(shellCommand(argv))))
}

// ghosttyFocus finds the tab a session lives in and switches to it. Match
// preference: id and title agreeing beats title alone beats id alone —
// Ghostty's tab ids are reused after a close, so an id whose title moved on
// probably belongs to some other tab now, and a title can collide between
// two sessions with the same directory and name. The one-pass collection
// keeps it a single AppleScript round trip.
func ghosttyFocus(id, title string) error {
	return found(osascript(fmt.Sprintf(`
tell application "Ghostty"
	set best to missing value
	set byTitle to missing value
	set byId to missing value
	repeat with w in windows
		repeat with t in tabs of w
			set tid to id of t
			set tname to name of t
			if tid is %[1]s and tname is %[2]s then
				set best to {w, t}
			else if tname is %[2]s and byTitle is missing value then
				set byTitle to {w, t}
			else if tid is %[1]s and byId is missing value then
				set byId to {w, t}
			end if
		end repeat
	end repeat
	if best is missing value then set best to byTitle
	if best is missing value then set best to byId
	if best is missing value then return "missing"
	activate
	select tab (item 2 of best)
	activate window (item 1 of best)
	return "found"
end tell`, asString(id), asString(title))))
}

// ghosttyOpenTabLegacy drives Ghostty's own cmd+T through System Events, for
// Ghostty before 1.3 (or with `macos-applescript = false`), which has no
// scriptable way to open a tab. Needs Accessibility permission for the
// terminal, and a delay long enough for the new tab's shell to come up —
// zsh buffers keystrokes typed before its first prompt, so the delay only
// has to clear the window creation itself.
func ghosttyOpenTabLegacy(argv []string) error {
	attach := shellCommand(argv)
	wait := delay("ORBIT_TAB_DELAY", time.Second).Seconds()
	script := fmt.Sprintf(`
tell application "Ghostty" to activate
delay 0.35
tell application "System Events" to tell process "Ghostty"
	keystroke "t" using command down
	delay %.2f
	keystroke %s
	key code 36
end tell`, wait, asString(attach))
	return exec.Command("osascript", "-e", script).Run()
}

// ghosttyFocusLegacy is tab switching without the dictionary: walk the
// accessibility tree matching titles. Ghostty uses native macOS tabs, which
// System Events shows as one window per tab group — the active tab's title
// is the window's, background tabs are the tab bar's radio buttons.
func ghosttyFocusLegacy(title string) error {
	return found(osascript(fmt.Sprintf(`
tell application "Ghostty" to activate
tell application "System Events" to tell process "Ghostty"
	repeat with w in windows
		if title of w is %[1]s then
			perform action "AXRaise" of w
			return "found"
		end if
		repeat with tg in tab groups of w
			repeat with rb in radio buttons of tg
				if title of rb is %[1]s then
					perform action "AXRaise" of w
					click rb
					return "found"
				end if
			end repeat
		end repeat
	end repeat
end tell
return "missing"`, asString(title))))
}

// ghosttyOpenWindowApp spawns a detached Ghostty window via LaunchServices —
// works whether or not orbit itself runs inside Ghostty.
func ghosttyOpenWindowApp(argv []string, cwd string) error {
	args := append([]string{"-na", "Ghostty.app", "--args", "--working-directory=" + cwd, "-e"}, argv...)
	return exec.Command("open", args...).Run()
}

func ghosttyOpenWindowLinux(argv []string, cwd string) error {
	args := append([]string{"--working-directory=" + cwd, "-e"}, argv...)
	return exec.Command("ghostty", args...).Start()
}

package term

import "fmt"

// iTerm2 has had a stable AppleScript dictionary for years; sessions carry
// UUID-backed ids that are not reused, so — unlike Ghostty — a matching id
// alone is trustworthy and the title is only the fallback for sessions
// orbit didn't open itself. The attach command goes through `write text`,
// which types into the session's shell (with a trailing newline by default)
// for the same reasons the Ghostty route uses `initial input`.
func itermOpenTab(argv []string) (string, error) {
	return osascript(fmt.Sprintf(`
tell application "iTerm2"
	activate
	if (count of windows) is 0 then
		create window with default profile
	else
		tell current window to create tab with default profile
	end if
	set s to current session of current window
	tell s to write text %s
	return id of s
end tell`, asString(shellCommand(argv))))
}

func itermOpenWindow(argv []string) (string, error) {
	return osascript(fmt.Sprintf(`
tell application "iTerm2"
	activate
	create window with default profile
	set s to current session of current window
	tell s to write text %s
	return id of s
end tell`, asString(shellCommand(argv))))
}

func itermFocus(id, title string) error {
	return found(osascript(fmt.Sprintf(`
tell application "iTerm2"
	set best to missing value
	set byTitle to missing value
	repeat with w in windows
		repeat with t in tabs of w
			repeat with s in sessions of t
				if id of s is %[1]s then
					set best to {w, s}
				else if name of s is %[2]s and byTitle is missing value then
					set byTitle to {w, s}
				end if
			end repeat
		end repeat
	end repeat
	if best is missing value then set best to byTitle
	if best is missing value then return "missing"
	activate
	select (item 2 of best)
	select (item 1 of best)
	return "found"
end tell`, asString(id), asString(title))))
}

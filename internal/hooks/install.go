package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/sadrig91/orbit/internal/format"
)

// How the hooks reach a session without touching anything the user owns.
//
// orbit types every live session's command itself, in tmux.Spawn — which
// means every session whose state can matter is one orbit launches, and the
// hook config can ride along per invocation. Claude takes `--settings <file>`
// and merges it over the user's own settings without displacing them
// (verified). The file lives in orbit's cache, not user config, and Claude
// keeps no trust-hash over hook definitions, so it may change freely between
// releases.
//
// Codex and copilot are not wired yet. Codex takes the same shape inline via
// -c, but skips untrusted hooks with a startup warning until the user
// approves them once in /hooks — that wants deliberate UX, not a surprise.
// Copilot only reads a global hooks directory, which would fire orbit's hook
// for the user's own copilot runs too; that needs saying out loud and an
// uninstall, so it is a decision rather than a default.

// claudeEvents is what orbit subscribes to, with the matcher that limits
// each where one is needed.
//
// PermissionRequest, not Notification: the notification fires ~6s after the
// prompt has been sitting. No PreToolUse: it precedes the permission gate,
// so it adds a subprocess per tool call and no information. SessionStart is
// limited to startup|resume|clear because it also fires on compaction, in
// the middle of a working turn, where "finished" would be a lie — and it is
// subscribed at all because a resumed session must overwrite whatever the
// previous run's state file claimed before it died.
var claudeEvents = []struct {
	name    string
	matcher string
}{
	{"SessionStart", "startup|resume|clear"},
	{"UserPromptSubmit", ""},
	{"PermissionRequest", ""},
	{"PermissionDenied", ""},
	{"PostToolUse", ""},
	{"PostToolUseFailure", ""},
	{"Stop", ""},
	{"SessionEnd", ""},
}

// hookTimeout bounds the hook, in seconds. The default would be far worse
// than a missed event: these run synchronously inside the agent's loop, so a
// hung hook is a hung agent.
const hookTimeout = 5

// SpawnArgs returns what to append to an agent's launch command so its
// session reports state, or "" when that agent isn't wired for it — the
// caller just gets no events and the transcript inference carries the load,
// exactly as it does for sessions predating all of this.
func SpawnArgs(agent string) string {
	if agent != "claude" {
		return ""
	}
	path, err := ensureClaudeSettings()
	if err != nil {
		return ""
	}
	// Quoted, because nothing downstream will do it: the composed command is
	// typed into an interactive shell by tmux send-keys, so a space in $HOME
	// would otherwise split the path into a bad flag and a stray word that
	// claude reads as a prompt.
	return " --settings " + shellQuote(path)
}

func settingsPath() string { return format.Home(".cache", "orbit", "hooks", "claude.json") }

// ensureClaudeSettings writes the settings file the spawn will point at, if
// it isn't already exactly what it should be. Content-compared rather than
// written every time so a running dashboard doesn't churn the file's mtime
// with every spawn.
// executable is a seam: test binaries live under the temp dir themselves,
// which is exactly what the guard below rejects.
var executable = os.Executable

func ensureClaudeSettings() (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	// A `go run` binary lives under the temp dir and is deleted the moment
	// the run exits — baking its path into a file that real sessions read at
	// startup would leave them invoking a ghost. update.classify draws the
	// same line for the same reason. Dev builds run from a checkout path and
	// pass fine.
	if strings.HasPrefix(exe, os.TempDir()) {
		return "", os.ErrNotExist
	}
	// Deliberately not resolving symlinks (contrast update.Detect, which
	// must): the brew path /opt/homebrew/bin/orbit survives version bumps,
	// while its Caskroom target dies with each one — for a path baked into a
	// config file, the link is the durable name.
	want := ClaudeSettings(exe)
	path := settingsPath()
	if have, err := os.ReadFile(path); err == nil && string(have) == string(want) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, want, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// ClaudeSettings renders the hooks stanza Claude merges over the user's own
// settings. The command is the orbit binary by absolute path — inside a tmux
// login shell there is no promise orbit is on PATH — and quoted, because
// Claude runs it through a shell. Exported so checks that need a real
// terminal can generate the same bytes for a binary of their choosing rather
// than a hand-kept copy that drifts.
func ClaudeSettings(exe string) []byte {
	type handler struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type matcher struct {
		Matcher string    `json:"matcher,omitempty"`
		Hooks   []handler `json:"hooks"`
	}
	events := map[string][]matcher{}
	for _, ev := range claudeEvents {
		events[ev.name] = []matcher{{Matcher: ev.matcher, Hooks: []handler{{
			Type:    "command",
			Command: shellQuote(exe) + " hook claude " + ev.name,
			Timeout: hookTimeout,
		}}}}
	}
	// Marshalling a map of plain structs has no failure mode; the shape test
	// would catch one appearing before any user did.
	b, _ := json.MarshalIndent(map[string]any{"hooks": events}, "", " ")
	return b
}

// shellQuote makes a string safe to place on a shell command line: wrapped
// in single quotes, with any embedded single quote spliced as '\”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

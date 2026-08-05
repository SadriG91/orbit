package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"

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

// claudeEvents is what orbit subscribes to. PermissionRequest, not
// Notification: the notification fires ~6s after the prompt has been
// sitting, and PreToolUse is omitted because it precedes the permission
// gate, so it adds a subprocess per tool call and no information —
// UserPromptSubmit already said "working".
var claudeEvents = []string{
	"UserPromptSubmit",
	"PermissionRequest",
	"PermissionDenied",
	"PostToolUse",
	"Stop",
	"SessionEnd",
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
	return " --settings " + path
}

func settingsPath() string { return format.Home(".cache", "orbit", "hooks", "claude.json") }

// ensureClaudeSettings writes the settings file the spawn will point at, if
// it isn't already exactly what it should be. Content-compared rather than
// written every time so a running dashboard doesn't churn the file's mtime
// with every spawn.
func ensureClaudeSettings() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	want, err := ClaudeSettings(exe)
	if err != nil {
		return "", err
	}
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
// login shell there is no promise orbit is on PATH. Exported so checks that
// need a real terminal can generate the same bytes for a binary of their
// choosing rather than a hand-kept copy that drifts.
func ClaudeSettings(exe string) ([]byte, error) {
	type handler struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type matcher struct {
		Hooks []handler `json:"hooks"`
	}
	events := map[string][]matcher{}
	for _, ev := range claudeEvents {
		events[ev] = []matcher{{Hooks: []handler{{
			Type:    "command",
			Command: exe + " hook claude " + ev,
			Timeout: hookTimeout,
		}}}}
	}
	return json.MarshalIndent(map[string]any{"hooks": events}, "", " ")
}

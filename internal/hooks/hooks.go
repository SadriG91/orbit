// Package hooks turns agent hook events into session state.
//
// The transcripts cannot say whether a session needs you. A permission prompt
// is not written to them at all, and their last word during any tool call is
// the same either way — a call without a result — so inference has to wait
// and see whether anything moves. Measured over 8,327 real tool calls, that
// guess is wrong about one time in nine.
//
// The agents themselves know, and their hook systems say so as it happens:
// PermissionRequest fires the same second the prompt is drawn. orbit injects
// a hook command into the sessions it spawns, the command writes one small
// state file per session, and the scan reads those files instead of guessing.
//
// Everything here runs inside the agent's own loop, which sets the rules: one
// file write and exit, no locks, no network, never a failure the agent can
// see. A slow hook does not degrade orbit — it stalls the agent the user is
// actually working with.
//
// Copilot raises the stakes further: its preToolUse hook is fail-closed. A
// non-zero exit — including a crash — denies the user's tool call outright,
// and exit 2 denies even when stdout says allow. So the hook entry point must
// reach exit 0 on every path, panic included, and write nothing to stdout,
// which several of these events interpret as instructions.
package hooks

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sadrig91/orbit/internal/format"
)

// Status is what a hook event says the session is doing now.
type Status string

const (
	Working  Status = "working"
	NeedsYou Status = "needs_you"
	YourTurn Status = "your_turn"
)

// State is one session's last known status, as reported by the agent itself.
type State struct {
	Status Status    `json:"status"`
	Event  string    `json:"event"`
	At     time.Time `json:"at"`
}

// transitions maps each agent's event names to a status. Absent means the
// event is not subscribed or carries no state worth recording.
//
// Notification is deliberately missing for claude: it fires about six seconds
// after the prompt has been sitting, measured — subscribing to it would
// rebuild the exact lag this package exists to remove. PermissionRequest is
// the same-second signal.
//
// Copilot has no approval event at all, so nothing there can produce
// NeedsYou; its pending approvals stay with the transcript inference, which
// treats a copilot Working entry as soft — see session.Resolve.
var transitions = map[string]map[string]Status{
	"claude": {
		"UserPromptSubmit":  Working,
		"PermissionRequest": NeedsYou,
		"PermissionDenied":  Working, // answered; the model carries on
		"PostToolUse":       Working, // also the edge that clears an approval
		"Stop":              YourTurn,
	},
	"codex": {
		"UserPromptSubmit":  Working,
		"PermissionRequest": NeedsYou,
		"PostToolUse":       Working,
		"Stop":              YourTurn,
	},
	// Both spellings for copilot. The camelCase names are what fired in our
	// live test; the reference also documents PascalCase aliases that emit
	// the Claude-shaped snake_case payload instead — including a
	// PermissionRequest alias whose firing nobody has verified (the matching
	// string in the bundled CLI sits in its ACP layer, a different
	// mechanism). Which set the installed config registers is decided at
	// install time, with a live test; the table accepts either so that
	// decision doesn't reach back here.
	"copilot": {
		"userPromptSubmitted": Working,
		"preToolUse":          Working,
		"postToolUse":         Working,
		"agentStop":           YourTurn,

		"UserPromptSubmit":  Working,
		"PreToolUse":        Working,
		"PostToolUse":       Working,
		"Stop":              YourTurn,
		"PermissionRequest": NeedsYou,
	},
}

// ended lists the events that mean the session is over and its state file
// should go rather than linger as a stale claim.
var ended = map[string]bool{"SessionEnd": true, "sessionEnd": true}

// Dir is where the state files live: one small JSON per live session.
func Dir() string { return format.Home(".cache", "orbit", "state") }

// Run handles `orbit hook <agent> <event>`. It must never fail in a way the
// agent notices: whatever goes wrong, the answer is a clean exit and silence
// on stdout — several of these events interpret stdout as instructions.
func Run(agent, event string, stdin io.Reader) {
	_ = Record(agent, event, stdin)
}

// Record applies one event. Exposed separately from Run so tests can see the
// errors Run deliberately swallows.
func Record(agent, event string, stdin io.Reader) error {
	// The payload identifies the session. Claude and codex spell the field
	// session_id, copilot spells it sessionId; everything else in the payload
	// is noise for this purpose.
	var p struct {
		SessionID  string `json:"session_id"`
		SessionID2 string `json:"sessionId"`
	}
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&p); err != nil {
		return err
	}
	id := p.SessionID
	if id == "" {
		id = p.SessionID2
	}
	id = sanitize(id)
	if id == "" {
		return nil // nothing to key on, nothing to record
	}

	if ended[event] {
		os.Remove(file(agent, id))
		return nil
	}
	status, ok := transitions[agent][event]
	if !ok {
		return nil // unsubscribed or unknown; not an error, just not ours
	}

	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(State{Status: status, Event: event, At: time.Now()})
	if err != nil {
		return err
	}
	// Written beside and renamed over, so the scan can never read half a
	// file. The scan runs every couple of seconds; torn reads would be
	// routine, not rare.
	tmp, err := os.CreateTemp(Dir(), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file(agent, id))
}

// Load returns the recorded state for a session, if any.
func Load(agent, id string) (State, bool) {
	b, err := os.ReadFile(file(agent, sanitize(id)))
	if err != nil {
		return State{}, false
	}
	var st State
	if json.Unmarshal(b, &st) != nil || st.Status == "" {
		return State{}, false
	}
	return st, true
}

// Prune removes state files that have not been touched in a while. SessionEnd
// removes them properly; this catches the sessions that never got one — a
// killed agent, a crashed machine — before the directory fills with claims
// about conversations long over.
func Prune(olderThan time.Duration) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-olderThan)
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(Dir(), e.Name()))
		}
	}
}

func file(agent, id string) string { return filepath.Join(Dir(), agent+"-"+id+".json") }

// sanitize keeps a session id usable as a filename component. Ids are UUIDs
// from the agents' own stores, but they arrive over stdin from a subprocess
// invocation and are about to become a path — so nothing traversal-shaped
// gets through on principle.
func sanitize(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return -1
	}, id)
}

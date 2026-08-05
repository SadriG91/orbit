// Package hooks turns agent hook events into session state.
//
// The transcripts cannot say whether a session needs attention. A permission prompt
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
// and exit 2 denies even when stdout says allow. Dispatch owns that contract,
// next to where it is stated: swallow everything, say nothing on stdout,
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
	// Soft marks a status the emitting agent cannot make definitive. Copilot
	// has no approval event, so its Working only means "a tool started" —
	// which is exactly the ambiguous state — and the reader should stop
	// believing it once it has sat still. The judgement of which events are
	// soft lives here, with the event tables, not with the reader.
	Soft bool `json:"soft,omitempty"`
	// ToolUseID pins a NeedsYou to the tool call that asked. Agents run tool
	// batches in parallel, and without it the PostToolUse of some
	// auto-approved read arriving a beat later would clear a prompt that is
	// still on screen — see Record.
	ToolUseID string `json:"tool_use_id,omitempty"`
}

// rule is one event's contribution to state.
type rule struct {
	status Status
	soft   bool
}

// transitions maps each agent's event names to a rule. Absent means the event
// is not subscribed or carries no state worth recording.
//
// Notification is deliberately missing for claude: it fires about six seconds
// after the prompt has been sitting, measured — subscribing to it would
// rebuild the exact lag this package exists to remove. PermissionRequest is
// the same-second signal.
//
// Copilot's rows are the camelCase names that actually fired in a live run;
// the PascalCase aliases its reference documents are deliberately absent
// until someone fires them — they include an approval event nobody has seen
// work, and an unverified NeedsYou would put a false triangle on a session
// with nothing on screen. Every copilot Working is soft for the same reason:
// with no approval event, "a tool started" is all it can ever mean.
var transitions = map[string]map[string]rule{
	"claude": {
		"SessionStart":       {YourTurn, false}, // matcher-limited to startup|resume|clear; see install.go
		"UserPromptSubmit":   {Working, false},
		"PermissionRequest":  {NeedsYou, false},
		"PermissionDenied":   {Working, false}, // answered; the model carries on
		"PostToolUse":        {Working, false}, // also the edge that clears an approval
		"PostToolUseFailure": {Working, false}, // a failed tool is answered too
		"Stop":               {YourTurn, false},
	},
	"codex": {
		"SessionStart":      {YourTurn, false},
		"UserPromptSubmit":  {Working, false},
		"PermissionRequest": {NeedsYou, false},
		"PostToolUse":       {Working, false},
		"Stop":              {YourTurn, false},
	},
	"copilot": {
		"userPromptSubmitted": {Working, true},
		"preToolUse":          {Working, true},
		"postToolUse":         {Working, true},
		"agentStop":           {YourTurn, false},
	},
}

// ended lists the events that mean the session is over and its state file
// should go rather than linger as a stale claim.
var ended = map[string]bool{"SessionEnd": true, "sessionEnd": true}

// Dir is where the state files live: one small JSON per live session.
func Dir() string { return format.Home(".cache", "orbit", "state") }

// Dispatch handles the `orbit hook <agent> <event>` argv, and owns the
// contract the package doc states: nothing on stdout, no panic escapes, and
// the caller exits 0 whenever this returns true — however mangled the argv,
// because a truncated invocation falling through to the normal CLI would
// exit non-zero, and on copilot that denies the user's tool call.
func Dispatch(args []string, stdin io.Reader) (handled bool) {
	if len(args) < 2 || args[1] != "hook" {
		return false
	}
	defer func() { recover() }() //nolint:errcheck // silence is the contract
	if len(args) >= 4 {
		_ = Record(args[2], args[3], stdin)
	}
	return true
}

// Record applies one event. Dispatch discards the error — silence is the
// contract on the hook path — but tests need to see it.
func Record(agent, event string, stdin io.Reader) error {
	// The agent name arrives as argv from whatever hook config invoked us and
	// becomes part of a path, so it gets the same treatment as the id.
	agent = sanitize(agent)

	// The payload identifies the session. Claude and codex spell the field
	// session_id, copilot spells it sessionId; the tool_use_id, where an
	// event carries one, ties approvals to answers.
	var p struct {
		SessionID  string `json:"session_id"`
		SessionID2 string `json:"sessionId"`
		ToolUseID  string `json:"tool_use_id"`
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
	r, ok := transitions[agent][event]
	if !ok {
		return nil // unsubscribed or unknown; not an error, just not ours
	}

	dir := Dir()
	if !filepath.IsAbs(dir) {
		// No HOME in the hook's environment. Writing a relative .cache tree
		// into whatever cwd the agent gave us would litter repositories with
		// state files the dashboard could never find anyway.
		return nil
	}

	// Agents run tool batches in parallel. When a prompt is parked, the
	// PostToolUse of some other, auto-approved call must not clear it — only
	// the answer to the call that asked does. Comparing tool ids is how the
	// two are told apart; an event without one keeps last-writer behaviour,
	// which is also what absorbs the read-then-write race between two hook
	// processes: the next event corrects it.
	if r.status == Working && (event == "PostToolUse" || event == "PostToolUseFailure") {
		if cur, ok := Load(agent, id); ok &&
			cur.Status == NeedsYou && cur.ToolUseID != "" &&
			p.ToolUseID != "" && p.ToolUseID != cur.ToolUseID {
			return nil
		}
	}

	st := State{Status: r.status, Event: event, At: time.Now(), Soft: r.soft}
	if r.status == NeedsYou {
		st.ToolUseID = p.ToolUseID
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Written beside and renamed over, so the scan can never read half a
	// file. The scan runs every couple of seconds; torn reads would be
	// routine, not rare. CreateTemp already opens 0600, matching the rest of
	// orbit's cache.
	tmp, err := os.CreateTemp(dir, ".state-*")
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
	return os.Rename(tmp.Name(), file(agent, id))
}

// Load returns the recorded state for a session, if any.
func Load(agent, id string) (State, bool) {
	b, err := os.ReadFile(file(agent, id))
	if err != nil {
		return State{}, false
	}
	var st State
	if json.Unmarshal(b, &st) != nil || st.Status == "" {
		return State{}, false
	}
	return st, true
}

// Forget drops a session's recorded state. Called when orbit is about to
// resume a session: whatever the file claims is from a previous run — often
// one that ended in a kill, since kill-session SIGHUPs the agent and its
// SessionEnd hook never fires — and a fresh claude at an idle prompt emits
// nothing until you type, so a stale claim would stand indefinitely.
func Forget(agent, id string) {
	os.Remove(file(agent, id))
}

// Prune removes state files that have not been touched in a while. SessionEnd
// removes them properly and Forget covers resumes; this catches what is left
// — sessions that died and were never touched again — before the directory
// fills with claims about conversations long over.
func Prune(olderThan time.Duration) {
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-olderThan)
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// file sanitizes both parts itself: every caller feeds it values that came
// from outside this process — argv, a hook payload, a transcript filename —
// and a choke point cannot be forgotten the way call-site discipline can.
func file(agent, id string) string {
	return filepath.Join(Dir(), sanitize(agent)+"-"+sanitize(id)+".json")
}

// sanitize keeps a value usable as a filename component. Ids are UUIDs from
// the agents' own stores and the agent name is orbit's own argv, but both
// arrive from outside this process and are about to become a path — so
// nothing traversal-shaped gets through on principle.
func sanitize(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return -1
	}, id)
}

// Package dispatch runs an agent CLI headlessly and records what it does.
//
// It is the other half of the problem internal/hooks solves. Hooks observe a
// session someone else is driving; dispatch is orbit driving one itself — you
// name a task from the dashboard, orbit starts the agent with no terminal
// attached, and the run appears in the list like any other session.
//
// The state it reports is first-hand rather than inferred. All three CLIs emit
// structured JSONL in non-interactive mode, so orbit reads the agent's own
// events: there is no transcript to lag, no stillness to guess at, and no 12
// second threshold. What each stream says was measured by running it, and the
// tables live next to the parsers in claude.go, codex.go and copilot.go.
//
// Two facts shape everything here.
//
// A dispatched run writes to the agent's ordinary store — verified for all
// three — so it stays resumable: `claude --resume <id>` on a dispatched session
// replays it in the interactive TUI and appends to the same transcript, no
// fork. Dispatch is therefore a way to start work, not a separate kind of
// session, and every dispatch ends with one you can take over.
//
// Only Claude can ask for approval in this mode, and only if asked to. With
// `--input-format stream-json --permission-prompt-tool stdio` the CLI writes a
// can_use_tool control request and blocks; without it, a tool needing approval
// is auto-denied and the model quietly works around it. Codex and copilot have
// no such channel at all — copilot's CLI requires --allow-all-tools to run
// non-interactively. So orbit takes the approval as the moment to stop: it
// interrupts the turn and hands the session back, which is the one outcome
// that is honest on all three.
package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sadrig91/orbit/internal/format"
)

// Status is where a dispatched run got to.
type Status string

const (
	Running   Status = "running"   // the agent is working
	NeedsYou  Status = "needs_you" // stopped at an approval; waiting to be taken over
	Done      Status = "done"      // the turn completed
	Failed    Status = "failed"    // the CLI errored, timed out, or was killed
	Cancelled Status = "cancelled" // deliberately stopped from orbit
)

// Record is one dispatch, from the moment orbit decides to start it. The
// dashboard writes it, the runner takes it over, and both read it — there is
// no separate job file, because everything the runner needs to start is also
// what the dashboard needs to display.
type Record struct {
	ID    string `json:"id"`    // orbit's own id for this dispatch
	Agent string `json:"agent"` // claude | codex | copilot
	// SessionID is the agent's own id for the conversation, which is what
	// joins a dispatch to the session in the list. Claude and copilot let
	// orbit choose it up front (--session-id), so it is set before the process
	// starts; codex only reveals its thread_id on the first event, so for
	// codex this is empty until the runner fills it in.
	SessionID string `json:"session_id,omitempty"`
	Cwd       string `json:"cwd"`
	Prompt    string `json:"prompt"`
	Tmux      string `json:"tmux,omitempty"` // the tmux session the runner lives in

	Status     Status   `json:"status"`
	Activity   string   `json:"activity,omitempty"`   // the last thing it was seen doing
	Activities []string `json:"activities,omitempty"` // recent distinct steps, oldest first
	Result     string   `json:"result,omitempty"`     // the agent's final response, when emitted
	Pending    string   `json:"pending,omitempty"`    // the tool call it handed back on
	Err        string   `json:"err,omitempty"`

	Started time.Time `json:"started"`
	Updated time.Time `json:"updated"`
	Ended   time.Time `json:"ended,omitempty"`
}

// Live reports whether the run is still supposed to be going.
func (r *Record) Live() bool { return r.Status == Running }

// Dir is where the records live: one small JSON per dispatch.
func Dir() string { return format.Home(".cache", "orbit", "dispatch") }

// LogPath is where a run's narration is kept. The tmux pane holds it too, but
// only until the pane is gone — and the pane goes as soon as the run ends,
// which is exactly when someone wants to know what happened.
func LogPath(id string) string { return filepath.Join(Dir(), sanitize(id)+".log") }

func file(id string) string { return filepath.Join(Dir(), sanitize(id)+".json") }

// Save writes the record. Atomic, because the dashboard reads this directory
// every couple of seconds while the runner is writing it.
func Save(r *Record) error {
	r.Updated = time.Now()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return format.WriteAtomic(file(r.ID), b, 0o600)
}

// Load reads one record by orbit's dispatch id.
func Load(id string) (*Record, bool) {
	return read(file(id))
}

// MarkCancelled records an intentional stop after orbit has killed the runner.
// The runner cannot reliably write its own terminal state once its tmux session
// is gone, so the dashboard owns this transition.
func MarkCancelled(id string) error {
	r, ok := Load(id)
	if !ok {
		return fmt.Errorf("dispatch %q not found", id)
	}
	r.Status = Cancelled
	r.Activity = ""
	r.Pending = ""
	r.Err = ""
	r.Ended = time.Now()
	return Save(r)
}

func read(path string) (*Record, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var r Record
	if json.Unmarshal(b, &r) != nil || r.ID == "" || r.Agent == "" {
		return nil, false
	}
	return &r, true
}

// Key identifies the session a record belongs to. Agent and id together,
// because ids are only unique within one agent's store.
func Key(agent, sessionID string) string { return agent + "\x00" + sessionID }

// Records returns every readable dispatch, oldest first. Callers that need to
// show launch failures and agents which have not announced a session id yet
// must not use Active: those records are deliberately unjoinable but still
// important UI state.
func Records() []*Record {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var recs []*Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if r, ok := read(filepath.Join(Dir(), e.Name())); ok {
			recs = append(recs, r)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Started.Before(recs[j].Started) })
	return recs
}

// Active returns every record that has a session to attach to, keyed by Key.
// When two records share a session — a session dispatched to more than once —
// the newest wins, since Records is oldest first.
func Active() map[string]*Record {
	recs := Records()
	out := make(map[string]*Record, len(recs))
	for _, r := range recs {
		if r.SessionID != "" {
			out[Key(r.Agent, r.SessionID)] = r
		}
	}
	return out
}

// Forget drops the records for a session. Called when orbit is about to resume
// one interactively: the dispatch is over the moment a person takes it over,
// and a record left saying "needs attention" would keep the triangle on a session
// being actively typed into.
func Forget(agent, sessionID string) {
	if sessionID == "" {
		return
	}
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(Dir(), e.Name())
		if r, ok := read(path); ok && r.Agent == agent && r.SessionID == sessionID {
			os.Remove(path)
			os.Remove(LogPath(r.ID))
		}
	}
}

// Prune removes finished records that have been sitting for a while, and their
// logs. Running ones are kept whatever their age: a long dispatch is still a
// dispatch, and deleting its record would strand the run with nothing on
// screen to say it exists.
func Prune(olderThan time.Duration) {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-olderThan)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(Dir(), e.Name())
		r, ok := read(path)
		if !ok {
			// Unreadable and old enough is junk; unreadable and new may be a
			// record being written right now.
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				os.Remove(path)
			}
			continue
		}
		if r.Live() || r.Updated.After(cutoff) {
			continue
		}
		os.Remove(path)
		os.Remove(LogPath(r.ID))
	}
}

// sanitize keeps a value usable as a filename component. The ids here are
// orbit's own, but they reach the runner as argv from a command line typed
// into a shell, so nothing traversal-shaped gets through on principle — the
// same rule, and the same reasoning, as hooks.sanitize.
func sanitize(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return -1
	}, id)
}

// NewID mints an id for a dispatch — and, for the agents that accept one, the
// session id the agent will use.
//
// It is a version 4 UUID because that is what claude's --session-id and
// copilot's --session-id require; anything else is rejected. Randomness comes
// from crypto/rand via uuid4, so two dashboards started in the same second
// cannot collide.
func NewID() string { return uuid4() }

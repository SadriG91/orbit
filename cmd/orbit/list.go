package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/sadrig91/orbit/internal/dispatch"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"
	"github.com/sadrig91/orbit/internal/tmux"
)

// listSessions prints the index without starting the dashboard: --list for
// reading, --json for piping into something else.
func listSessions(asJSON bool) {
	ix := session.NewIndex()
	sessions := ix.Scan()

	live := map[string]*tmux.Session{}
	for _, t := range tmux.List() {
		if t.SessionID != "" {
			live[t.SessionID] = t
		}
	}
	// The same join the dashboard's scan does, for the same reason: without
	// it a dispatched session reports as "not running" here while the
	// dashboard shows it working.
	dispatches := dispatch.Active()
	now := time.Now()
	for _, s := range sessions {
		s.Tmux = live[s.ID]
		s.Dispatch = dispatches[dispatch.Key(s.Agent.String(), s.ID)]
		s.Resolve(now)
	}
	session.SortSessions(sessions)

	if asJSON {
		emitJSON(sessions)
		return
	}
	for _, s := range sessions {
		fmt.Printf("%-7s %-2s %-9s %8s %-22s %-46s %s\n",
			s.Agent, s.State.Icon(), format.RelTime(s.Modified), format.HumanTokens(s.Tokens),
			format.Truncate(s.ShortCwd(), 22), format.Truncate(format.FirstLine(s.Name()), 46), s.ID)
	}
	fmt.Fprintf(os.Stderr, "\n%d sessions\n", len(sessions))
	for _, e := range ix.Errs() {
		fmt.Fprintln(os.Stderr, "warn:", e)
	}
}

type jsonSession struct {
	Agent    string        `json:"agent"`
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Cwd      string        `json:"cwd"`
	Branch   string        `json:"branch,omitempty"`
	State    string        `json:"state"`
	Messages int           `json:"messages"`
	Tokens   int64         `json:"tokens"`
	Modified time.Time     `json:"modified"`
	Running  bool          `json:"running"`
	Resume   string        `json:"resume"`
	Path     string        `json:"path,omitempty"`
	Dispatch *jsonDispatch `json:"dispatch,omitempty"`
}

// jsonDispatch is the headless run behind a session, when there is one. Its
// own object rather than more top-level fields, so a consumer can tell "orbit
// is driving this" from "someone is sitting in front of it".
type jsonDispatch struct {
	Status   string `json:"status"`
	Prompt   string `json:"prompt"`
	Activity string `json:"activity,omitempty"`
	Pending  string `json:"pending,omitempty"`
	Error    string `json:"error,omitempty"`
}

func emitJSON(sessions []*session.Session) {
	rows := make([]jsonSession, 0, len(sessions))
	for _, s := range sessions {
		state := s.State.Label()
		if state == "" {
			state = "idle"
		}
		var d *jsonDispatch
		if r := s.Dispatch; r != nil {
			d = &jsonDispatch{Status: string(r.Status), Prompt: r.Prompt,
				Activity: r.Activity, Pending: r.Pending, Error: r.Err}
		}
		rows = append(rows, jsonSession{
			Agent: s.Agent.String(), ID: s.ID, Title: s.Name(), Cwd: s.Cwd,
			Branch: s.Branch, State: state, Messages: s.Msgs, Tokens: s.Tokens,
			Modified: s.Modified.UTC(), Running: s.Tmux != nil,
			Resume: s.Agent.ResumeCmd(s.ID), Path: s.Path, Dispatch: d,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(rows)
}

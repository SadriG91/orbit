package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

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
	now := time.Now()
	for _, s := range sessions {
		s.Tmux = live[s.ID]
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
	Agent    string    `json:"agent"`
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Cwd      string    `json:"cwd"`
	Branch   string    `json:"branch,omitempty"`
	State    string    `json:"state"`
	Messages int       `json:"messages"`
	Tokens   int64     `json:"tokens"`
	Modified time.Time `json:"modified"`
	Running  bool      `json:"running"`
	Resume   string    `json:"resume"`
	Path     string    `json:"path,omitempty"`
}

func emitJSON(sessions []*session.Session) {
	rows := make([]jsonSession, 0, len(sessions))
	for _, s := range sessions {
		state := s.State.Label()
		if state == "" {
			state = "idle"
		}
		rows = append(rows, jsonSession{
			Agent: s.Agent.String(), ID: s.ID, Title: s.Name(), Cwd: s.Cwd,
			Branch: s.Branch, State: state, Messages: s.Msgs, Tokens: s.Tokens,
			Modified: s.Modified.UTC(), Running: s.Tmux != nil,
			Resume: s.Agent.ResumeCmd(s.ID), Path: s.Path,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(rows)
}

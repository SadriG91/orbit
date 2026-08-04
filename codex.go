package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Codex: ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl
// The first record is a session_meta carrying cwd, id and git info; state comes
// from the trailing event_msg stream.

func (ix *Index) scanCodex() []*Session {
	root := home(".codex", "sessions")
	var paths []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	return ix.scanPaths(paths, Codex, parseCodex)
}

type codexRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		ID        string `json:"id"` // older rollouts only carry `id`
		Cwd       string `json:"cwd"`
		Message   string `json:"message"`
		Git       *struct {
			Branch string `json:"branch"`
		} `json:"git"`
	} `json:"payload"`
}

func parseCodex(path string, mod time.Time) *Session {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	s := &Session{Path: path, Modified: mod}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var r codexRecord
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		switch r.Type {
		case "session_meta":
			s.ID = cmp.Or(r.Payload.SessionID, r.Payload.ID)
			s.Cwd = r.Payload.Cwd
			if r.Payload.Git != nil {
				s.Branch = r.Payload.Git.Branch
			}
		case "event_msg":
			switch r.Payload.Type {
			case "user_message":
				s.Msgs++
				s.hint = HintBusy
				if m := strings.TrimSpace(r.Payload.Message); m != "" {
					s.Last = m
					if s.Title == "" {
						s.Title = m // codex has no generated title; first prompt stands in
					}
				}
			case "agent_message":
				s.Msgs++
				s.hint = HintBusy
			case "task_started":
				s.hint = HintBusy
			case "task_complete", "turn_aborted":
				s.hint = HintDone
			default:
				if strings.HasSuffix(r.Payload.Type, "approval_request") {
					s.hint = HintApproval
				}
			}
		}
	}
	if s.ID == "" {
		// Last resort: rollout-<timestamp>-<uuid>.jsonl
		base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if parts := strings.Split(base, "-"); len(parts) >= 5 {
			s.ID = strings.Join(parts[len(parts)-5:], "-") // a uuid is 5 dash-separated groups
		}
	}
	if s.ID == "" || s.Cwd == "" {
		return nil
	}
	// Title from the first prompt, last prompt separately — keep them distinct.
	s.Title = truncate(firstLine(s.Title), 60)
	return s
}

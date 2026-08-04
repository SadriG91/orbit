package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude Code: ~/.claude/projects/<encoded-cwd>/<session-uuid>.jsonl
// The encoded directory name is lossy (/ and . both become -), so cwd is read
// out of the records instead of decoded from the path.

func (ix *Index) scanClaude() []*Session {
	root := home(".claude", "projects")
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var paths []string
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			// Only top-level transcripts; the uuid/ subdirs hold subagent traffic.
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			paths = append(paths, filepath.Join(root, d.Name(), f.Name()))
		}
	}
	return ix.scanPaths(paths, Claude, parseClaude)
}

type claudeRecord struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	AITitle     string `json:"aiTitle"`
	LastPrompt  string `json:"lastPrompt"`
	Cwd         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func parseClaude(path string, mod time.Time) *Session {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	s := &Session{
		ID:       strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Path:     path,
		Modified: mod,
	}
	var lastStamp string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 64*1024*1024) // tool results get long
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var r claudeRecord
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if r.Timestamp != "" {
			lastStamp = r.Timestamp
		}
		if r.Cwd != "" {
			s.Cwd = r.Cwd
		}
		if r.GitBranch != "" {
			s.Branch = r.GitBranch
		}
		switch r.Type {
		case "ai-title":
			s.Title = r.AITitle
		case "last-prompt":
			if r.LastPrompt != "" {
				s.Last = r.LastPrompt
			}
		case "user", "assistant":
			if r.IsSidechain || r.IsMeta || r.Message == nil {
				continue
			}
			blocks := blockTypes(r.Message.Content)
			if r.Type == "assistant" {
				s.Msgs++
				if has(blocks, "tool_use") {
					s.hint = HintMaybeApproval
				} else {
					s.hint = HintDone
				}
			} else if has(blocks, "tool_result") {
				s.hint = HintBusy
			} else {
				s.Msgs++
				s.hint = HintBusy
			}
		}
	}
	if s.Cwd == "" {
		return nil // can't resume somewhere we can't locate
	}
	s.Modified = eventTime(lastStamp, mod)
	return s
}

// A content field is either a bare string or an array of typed blocks.
func blockTypes(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		return []string{"text"}
	}
	var arr []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, a := range arr {
		out = append(out, a.Type)
	}
	return out
}

func has(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

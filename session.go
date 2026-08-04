package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type Agent int

const (
	Claude Agent = iota
	Codex
	Copilot
)

var AllAgents = []Agent{Claude, Codex, Copilot}

func (a Agent) String() string {
	switch a {
	case Codex:
		return "codex"
	case Copilot:
		return "copilot"
	}
	return "claude"
}

// Short tag shown in the list, and used to prefix tmux session names.
func (a Agent) Tag() string {
	switch a {
	case Codex:
		return "cx"
	case Copilot:
		return "cp"
	}
	return "cl"
}

func (a Agent) ResumeCmd(id string) string {
	switch a {
	case Codex:
		return "codex resume " + id
	case Copilot:
		return "copilot --resume=" + id
	}
	return "claude --resume " + id
}

func (a Agent) NewCmd() string { return a.String() }

func (a Agent) Installed() bool {
	_, err := exec.LookPath(a.String())
	return err == nil
}

// Hint is what a transcript says about where the session stopped, before
// tmux tells us whether the process is actually alive.
type Hint int

const (
	HintBusy Hint = iota
	HintDone
	HintApproval
	HintMaybeApproval // an unanswered tool call — approval prompt if it's been sitting
)

type State int

const (
	Dormant   State = iota // no tmux session
	ShellOnly              // tmux alive, the agent has exited
	Working
	NeedsApproval
	YourTurn
)

func (s State) Icon() string {
	switch s {
	case Working:
		return "●"
	case NeedsApproval:
		return "▲"
	case YourTurn:
		return "◆"
	case ShellOnly:
		return "○"
	}
	return "·"
}

func (s State) Label() string {
	switch s {
	case Working:
		return "working"
	case NeedsApproval:
		return "needs you"
	case YourTurn:
		return "your turn"
	case ShellOnly:
		return "shell"
	}
	return ""
}

type Session struct {
	Agent    Agent
	ID       string
	Path     string // transcript file, empty for copilot (sqlite-backed)
	Cwd      string
	Branch   string
	Title    string
	Last     string // last thing you typed
	Msgs     int
	Modified time.Time

	hint  Hint
	Tmux  *Tmux
	State State
}

// ShortCwd is home-relative and at most two trailing components.
func (s *Session) ShortCwd() string {
	p := s.Cwd
	if p == "" || p == "/" {
		return "/"
	}
	if home, err := os.UserHomeDir(); err == nil {
		if p == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(p, home+"/"); ok {
			p = rest
		} else {
			p = strings.TrimPrefix(p, "/")
		}
	}
	parts := strings.Split(p, "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}

func (s *Session) Name() string {
	if s.Title != "" {
		return firstLine(s.Title)
	}
	if s.Last != "" {
		return firstLine(s.Last)
	}
	return "(untitled)"
}

// TabTitle is what Ghostty puts on the tab.
func (s *Session) TabTitle() string {
	return s.ShortCwd() + " · " + truncate(s.Name(), 48)
}

func (s *Session) TmuxName() string {
	base := strings.NewReplacer(".", "_", ":", "_", " ", "_", "/", "-").Replace(s.ShortCwd())
	id := s.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return s.Agent.Tag() + "-" + base + "-" + id
}

func (s *Session) Live() bool {
	return s.State == Working || s.State == NeedsApproval || s.State == YourTurn
}

func (s *Session) resolve(now time.Time) {
	if s.Tmux == nil {
		s.State = Dormant
		return
	}
	if !s.Tmux.AgentRunning {
		s.State = ShellOnly
		return
	}
	switch s.hint {
	case HintDone:
		s.State = YourTurn
	case HintApproval:
		s.State = NeedsApproval
	case HintMaybeApproval:
		// An unanswered tool call. If it's been sitting there, the tool isn't
		// slow — the agent is parked on a permission prompt.
		if now.Sub(s.Modified) > 12*time.Second {
			s.State = NeedsApproval
		} else {
			s.State = Working
		}
	default:
		s.State = Working
	}
}

// Index caches parsed sessions. File-backed transcripts are re-read only when
// mtime or size changes, so ticking across ~200 files costs a stat() each.
type Index struct {
	files   map[string]cached
	cpCache []*Session
	cpStamp string
	Errs    []string
}

type cached struct {
	mod  time.Time
	size int64
	s    *Session
}

func NewIndex() *Index { return &Index{files: map[string]cached{}} }

func (ix *Index) Scan() []*Session {
	ix.Errs = nil
	var out []*Session
	out = append(out, ix.scanClaude()...)
	out = append(out, ix.scanCodex()...)
	out = append(out, ix.scanCopilot()...)
	return out
}

// scanPaths reads transcripts through the cache, parsing misses in parallel.
// A cold start is a few hundred large files; warm ticks are all stat() hits.
func (ix *Index) scanPaths(paths []string, ag Agent, parse func(string, time.Time) *Session) []*Session {
	type job struct {
		path string
		info os.FileInfo
	}
	var todo []job
	var out []*Session

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if c, ok := ix.files[p]; ok && c.mod.Equal(info.ModTime()) && c.size == info.Size() {
			if c.s != nil {
				out = append(out, c.s)
			}
			continue
		}
		todo = append(todo, job{p, info})
	}
	if len(todo) == 0 {
		return out
	}

	results := make([]*Session, len(todo))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i, j := range todo {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if s := parse(j.path, j.info.ModTime()); s != nil {
				s.Agent = ag
				results[i] = s
			}
		}(i, j)
	}
	wg.Wait()

	for i, j := range todo {
		// Cache misses too, so junk files aren't re-parsed every tick.
		ix.files[j.path] = cached{j.info.ModTime(), j.info.Size(), results[i]}
		if results[i] != nil {
			out = append(out, results[i])
		}
	}
	return out
}

func home(rest ...string) string {
	h, _ := os.UserHomeDir()
	return filepath.Join(append([]string{h}, rest...)...)
}

func sortSessions(ss []*Session) {
	rank := func(s *Session) int {
		switch s.State {
		case NeedsApproval:
			return 0
		case YourTurn:
			return 1
		case Working:
			return 2
		case ShellOnly:
			return 3
		}
		return 4
	}
	sort.SliceStable(ss, func(i, j int) bool {
		if a, b := rank(ss[i]), rank(ss[j]); a != b {
			return a < b
		}
		return ss[i].Modified.After(ss[j].Modified)
	})
}

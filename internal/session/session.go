package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/tmux"
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
	Tokens   int64
	Modified time.Time

	hint  Hint
	Tmux  *tmux.Session
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
		return format.FirstLine(s.Title)
	}
	if s.Last != "" {
		return format.FirstLine(s.Last)
	}
	return "(untitled)"
}

// TabTitle is what Ghostty puts on the tab.
func (s *Session) TabTitle() string {
	return s.ShortCwd() + " · " + format.Truncate(s.Name(), 48)
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

func (s *Session) Resolve(now time.Time) {
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

type SortMode int

const (
	SortAge SortMode = iota
	SortTokens
	SortProject
	SortAgent
)

var AllSorts = []SortMode{SortAge, SortTokens, SortProject, SortAgent}

func (s SortMode) String() string {
	switch s {
	case SortTokens:
		return "tokens"
	case SortProject:
		return "project"
	case SortAgent:
		return "agent"
	}
	return "age"
}

// Index caches parsed sessions. File-backed transcripts are re-read only when
// mtime or size changes, so ticking across ~200 files costs a stat() each.
// Index caches parsed transcripts. It guards its own state rather than relying
// on callers to serialise: single-flight scanning upstream is an efficiency
// measure and a recovery path may legitimately overlap two scans, so the
// invariant belongs to the type. A concurrent map write here is fatal, not
// merely wrong.
type Index struct {
	mu      sync.Mutex
	files   map[string]cached
	cpCache []*Session
	cpStamp string

	errsMu sync.Mutex
	errs   []string
}

// Errs returns any non-fatal problems from the last scan.
func (ix *Index) Errs() []string {
	ix.errsMu.Lock()
	defer ix.errsMu.Unlock()
	return append([]string(nil), ix.errs...)
}

func (ix *Index) addErr(msg string) {
	ix.errsMu.Lock()
	defer ix.errsMu.Unlock()
	ix.errs = append(ix.errs, msg)
}

type cached struct {
	mod  time.Time
	size int64
	s    *Session
}

func NewIndex() *Index { return &Index{files: map[string]cached{}} }

// Scan reads every store and returns a fresh snapshot of what it found.
//
// Session holds no mutable references — Tmux is replaced wholesale on every
// scan, never mutated in place — so the shallow copy snapshot makes is a
// complete one.
func (ix *Index) Scan() []*Session {
	ix.errsMu.Lock()
	ix.errs = nil
	ix.errsMu.Unlock()

	var out []*Session
	out = append(out, ix.scanClaude()...)
	out = append(out, ix.scanCodex()...)
	out = append(out, ix.scanCopilot()...)
	return snapshot(out)
}

// ScratchDir is where orbit runs the agent CLIs it drives itself — at present
// only summarisation.
//
// Those runs are ordinary sessions as far as the agent is concerned: `claude
// -p` writes a transcript keyed by its working directory. Run in the directory
// of the session being summarised, it leaves a phantom conversation in that
// project — auto-titled from whatever was in the excerpt, carrying a token
// count of its own, and eligible to be summarised in turn. Worse, it is by
// definition the most recently modified conversation in that directory, which
// is exactly what the unlinked-tmux match in the UI's scan looks for, so a
// resumed session could be joined to orbit's own bookkeeping instead of the
// work it belongs to.
//
// Giving those runs a directory of their own keeps them out of real projects,
// and snapshot drops them so they never reach the dashboard.
func ScratchDir() string { return format.Home(".cache", "orbit", "scratch") }

// snapshot copies the cache's sessions for the caller, dropping orbit's own.
//
// The copies are deliberate. The cache reuses parsed *Session values across
// scans, and the caller goes on to write to what it gets (Tmux, State) and to
// render it — while the next scan is already running.
func snapshot(out []*Session) []*Session {
	scratch := ScratchDir()
	snap := make([]*Session, 0, len(out))
	for _, s := range out {
		if s.Cwd == scratch {
			continue // orbit talking to itself; see ScratchDir
		}
		c := *s
		snap = append(snap, &c)
	}
	return snap
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
		ix.mu.Lock()
		c, ok := ix.files[p]
		ix.mu.Unlock()
		if ok && c.mod.Equal(info.ModTime()) && c.size == info.Size() {
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

	ix.mu.Lock()
	for i, j := range todo {
		// Cache misses too, so junk files aren't re-parsed every tick.
		ix.files[j.path] = cached{j.info.ModTime(), j.info.Size(), results[i]}
	}
	ix.mu.Unlock()
	for i := range todo {
		if results[i] != nil {
			out = append(out, results[i])
		}
	}
	return out
}

// EventTime prefers the timestamp the agent itself recorded over the file's
// mtime. They diverge badly: agents rewrite old transcripts in batches (title
// backfills and the like), which bumps mtime on sessions untouched for weeks
// and makes a three-week-old conversation claim it ran an hour ago.
func EventTime(stamp string, fallback time.Time) time.Time {
	if stamp == "" {
		return fallback
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, stamp); err == nil {
			return t
		}
	}
	return fallback
}

func SortSessionsBy(ss []*Session, mode SortMode) {
	SortSessions(ss) // state first, recency within it
	if mode == SortAge {
		return
	}
	sort.SliceStable(ss, func(i, j int) bool {
		a, b := ss[i], ss[j]
		// Anything live stays pinned to the top whatever the sort.
		if a.Live() != b.Live() {
			return a.Live()
		}
		switch mode {
		case SortTokens:
			if a.Tokens != b.Tokens {
				return a.Tokens > b.Tokens
			}
		case SortProject:
			if a.Cwd != b.Cwd {
				return a.Cwd < b.Cwd
			}
		case SortAgent:
			if a.Agent != b.Agent {
				return a.Agent < b.Agent
			}
		}
		return a.Modified.After(b.Modified)
	})
}

func SortSessions(ss []*Session) {
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

// RecordText pulls the human-readable text out of a transcript record, for
// both Claude's message blocks and Codex's event payloads.
func RecordText(line []byte) string {
	var r struct {
		Message *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Payload *struct {
			Message string `json:"message"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &r) != nil {
		return ""
	}
	if r.Payload != nil && r.Payload.Message != "" {
		return r.Payload.Message
	}
	if r.Message == nil || len(r.Message.Content) == 0 {
		return ""
	}
	if r.Message.Content[0] == '"' {
		var s string
		json.Unmarshal(r.Message.Content, &s)
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(r.Message.Content, &blocks) != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Text != "" {
			sb.WriteString(b.Text)
			sb.WriteByte(' ')
		}
	}
	return sb.String()
}

// ParseSortMode resolves a configured name, falling back to age.
func ParseSortMode(name string) SortMode {
	for _, s := range AllSorts {
		if strings.EqualFold(s.String(), name) {
			return s
		}
	}
	return SortAge
}

// AwaitingTool reports whether the transcript ends on a tool call nobody has
// answered — the ambiguous state, where the agent is either running something
// slow or parked on a permission prompt.
func (s *Session) AwaitingTool() bool { return s.hint == HintMaybeApproval }

// PromoteToApproval says the pane has been seen asking a question, so the wait
// in Resolve can be skipped.
func (s *Session) PromoteToApproval() {
	if s.State == Working {
		s.State = NeedsApproval
	}
}

// LooksLikeApprovalPrompt reports whether a pane is showing a question waiting
// on an answer.
//
// This exists because no agent records the fact. A permission prompt is not in
// the transcript at all — Claude writes the tool call and then nothing until
// you answer, so the only evidence orbit had was an unanswered tool call that
// stopped advancing, which takes seconds of stillness to be sure about. The
// screen knows immediately.
//
// Deliberately conservative, and deliberately only ever a shortcut: the
// stillness rule in Resolve still stands behind it, so a prompt this fails to
// recognise costs a few seconds rather than being missed. That is what makes
// pattern-matching another program's output acceptable here — the cost of
// being wrong is latency, not correctness. Never invert that by making this
// the only path.
func LooksLikeApprovalPrompt(pane string) bool {
	lines := tailLines(pane, 24)
	for i, l := range lines {
		if !strings.HasSuffix(strings.TrimSpace(l), "?") {
			continue
		}
		// A question is only a prompt if something below it is waiting to be
		// chosen. Prose ending in a question mark is not.
		for _, below := range lines[i+1:] {
			if isChoice(below) {
				return true
			}
		}
	}
	return false
}

// isChoice matches the answer lines agents offer: "1. Yes", "❯ 2. No",
// "> y) allow", and the bare y/n pair.
func isChoice(line string) bool {
	t := strings.TrimSpace(line)
	t = strings.TrimLeft(t, "❯>▸●*- \t")
	if t == "" {
		return false
	}
	if len(t) > 2 && (t[0] >= '1' && t[0] <= '9') && (t[1] == '.' || t[1] == ')') {
		return true
	}
	lower := strings.ToLower(t)
	for _, p := range []string{"y) ", "n) ", "y/n", "[y/n]", "(y/n)", "yes, and", "don't ask again"} {
		if strings.HasPrefix(lower, p) || strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// tailLines returns the last n non-empty lines, which is where a prompt sits
// however much output preceded it.
func tailLines(s string, n int) []string {
	var out []string
	all := strings.Split(s, "\n")
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(all[i]) != "" {
			out = append([]string{all[i]}, out...)
		}
	}
	return out
}

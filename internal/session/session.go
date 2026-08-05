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

	"github.com/sadrig91/orbit/internal/dispatch"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/hooks"
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
		return "needs attention"
	case YourTurn:
		return "finished"
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

	// Dispatch is the record of an orbit-driven headless run of this session,
	// when there is one. Joined on by the UI's scan, which reads the whole
	// record directory once rather than a file per session — see
	// dispatch.Active.
	Dispatch *dispatch.Record
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
	// A dispatch outranks everything below, including the tmux checks, and is
	// the only source here that is not inference at all: orbit started the
	// process and read the CLI's own event stream, so "working" means an event
	// said so and "needs attention" means the agent blocked asking for permission.
	//
	// It has to come before the tmux checks rather than after because a
	// finished dispatch has no tmux left — the runner ends with its pane — and
	// the whole point is that the session is then sitting there waiting to be
	// taken over.
	if d := s.Dispatch; d != nil && s.dispatchTrusted(d) {
		switch d.Status {
		case dispatch.Running:
			s.State = Working
			return
		case dispatch.NeedsYou:
			s.State = NeedsApproval
			return
		case dispatch.Failed:
			// Also "needs attention": a dispatch that stopped is exactly the thing
			// worth walking over to, and ▲ is how orbit says so — including
			// the desktop notification. The detail pane carries the reason.
			s.State = NeedsApproval
			return
		case dispatch.Done:
			s.State = YourTurn
			return
		}
	}

	if s.Tmux == nil {
		s.State = Dormant
		return
	}
	if !s.Tmux.AgentRunning {
		s.State = ShellOnly
		return
	}

	// The agent's own word beats inference. Sessions orbit spawns carry a
	// hook that reports state as it changes — PermissionRequest fires the
	// same second the prompt is drawn — where the transcript cannot say at
	// all: its last word during any tool call is a call without a result,
	// whether the tool is running or a prompt has been sitting for a minute.
	// Guessing from stillness gets that wrong about one time in nine.
	if st, ok := hooks.Load(s.Agent.String(), s.ID); ok && s.hookTrusted(st, now) {
		switch st.Status {
		case hooks.NeedsYou:
			s.State = NeedsApproval
			return
		case hooks.YourTurn:
			s.State = YourTurn
			return
		case hooks.Working:
			s.State = Working
			return
		}
	}

	switch s.hint {
	case HintDone:
		s.State = YourTurn
	case HintApproval:
		s.State = NeedsApproval
	case HintMaybeApproval:
		// An unanswered tool call. If it's been sitting there, the tool isn't
		// slow — the agent is parked on a permission prompt.
		if now.Sub(s.Modified) > stillnessWindow {
			s.State = NeedsApproval
		} else {
			s.State = Working
		}
	default:
		s.State = Working
	}
}

// stillnessWindow is how long an unanswered tool call may sit before the
// inference calls it a permission prompt — and how long a soft hook Working
// is believed before handing back to that same inference. One constant
// because they are the same judgement: how much stillness means "parked".
const stillnessWindow = 12 * time.Second

// dispatchTrusted decides whether a dispatch record still speaks for this
// session. Two questions, one per half of the lifecycle.
//
// A run claiming to be *running* is only believed while its runner is alive,
// and the runner lives in a tmux session — so no tmux means no runner. That
// covers the case the hooks work already learned the hard way: killing a
// session SIGHUPs whatever was inside it, so the process never gets to write
// its own ending, and a record left saying "working" would spin a dot forever.
//
// A *finished* run is believed until the conversation moves past it. Resuming
// a dispatched session interactively is the intended next step, and the moment
// someone types into it the record is describing a previous chapter. Orbit's
// own resume path calls dispatch.Forget as well; this covers the resumes it
// does not perform. The margin is the same 30 seconds hookTrusted allows, for
// the same reason — the transcript's clock and orbit's are not the same clock.
func (s *Session) dispatchTrusted(d *dispatch.Record) bool {
	if d.Live() {
		return s.Tmux != nil
	}
	return !s.Modified.After(d.Ended.Add(30 * time.Second))
}

// hookTrusted decides whether a recorded hook state still speaks for this
// session.
//
// The transcript outrunning the state file means the hooks are not reporting
// for this run — a session relaunched by hand inside its pane, without
// orbit's injection, keeps writing its transcript while the old file sits
// there claiming last run's truth. The margin covers the write-behind the
// transcripts are documented to have (Claude calls transcript_path
// asynchronous and possibly lagging); a transient distrust during a long
// generation is harmless, because the inference it falls back to handles
// fresh activity correctly and the next event restores trust.
//
// The transcript can also flatly contradict the file, and a definite
// transcript beats a hook claim: HintDone means a finished turn was written,
// so anything but YourTurn is a stale file (a lost Stop event would
// otherwise pin "working" forever — the transcript is the self-correction
// the old inference had, kept). HintApproval is codex's explicit approval
// marker, which no Working claim should override.
//
// Soft states expire into the stillness inference; the judgement of which
// events are soft lives in the hooks package, with the event tables.
func (s *Session) hookTrusted(st hooks.State, now time.Time) bool {
	if s.Modified.After(st.At.Add(30 * time.Second)) {
		return false
	}
	if s.hint == HintDone && st.Status != hooks.YourTurn {
		return false
	}
	if s.hint == HintApproval && st.Status == hooks.Working {
		return false
	}
	if st.Soft && st.Status == hooks.Working && now.Sub(st.At) > stillnessWindow {
		return false
	}
	return true
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

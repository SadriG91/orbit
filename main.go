package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

//go:embed tmux.conf
var tmuxConf string

// Set at build time: -ldflags "-X main.version=v1.2.3"
var version = "dev"

const confMarker = "# orbit —"

func main() {
	window := flag.Bool("window", false, "attach sessions in a new Ghostty window instead of a tab")
	inline := flag.Bool("inline", false, "attach in this terminal instead of spawning a tab")
	noNotify := flag.Bool("no-notify", false, "don't send desktop notifications when a session wants you")
	list := flag.Bool("list", false, "print the session index as plain text and exit")
	asJSON := flag.Bool("json", false, "print the session index as JSON and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	probe := flag.Bool("probe-logos", false, "render the agent logos to check terminal support")
	flag.Parse()

	if *showVersion {
		fmt.Println("orbit", version)
		return
	}

	if *list || *asJSON {
		listSessions(*asJSON)
		return
	}
	if *probe {
		if err := ProbeLogos(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "orbit:", err)
			os.Exit(1)
		}
		return
	}

	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Fprintln(os.Stderr, "orbit: tmux not found — brew install tmux")
		os.Exit(1)
	}
	if err := installConf(); err != nil {
		fmt.Fprintln(os.Stderr, "orbit:", err)
		os.Exit(1)
	}

	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "orbit: config:", err, "(using defaults)")
	}
	if *noNotify {
		cfg.Notify = false
	}

	mode := cfg.attachMode()
	switch {
	case *inline:
		mode = attachInline
	case *window:
		mode = attachWindow
	}

	m := newModel(cfg, mode)
	m.notify = NewNotifier(cfg.Notify)
	defer m.notify.Close()

	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "orbit:", err)
		os.Exit(1)
	}
}

func listSessions(asJSON bool) {
	ix := NewIndex()
	sessions := ix.Scan()
	live := map[string]*Tmux{}
	for _, t := range TmuxList() {
		if t.SessionID != "" {
			live[t.SessionID] = t
		}
	}
	now := time.Now()
	for _, s := range sessions {
		s.Tmux = live[s.ID]
		s.resolve(now)
	}
	sortSessions(sessions)

	if asJSON {
		type out struct {
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
		rows := make([]out, 0, len(sessions))
		for _, s := range sessions {
			state := s.State.Label()
			if state == "" {
				state = "idle"
			}
			rows = append(rows, out{
				s.Agent.String(), s.ID, s.Name(), s.Cwd, s.Branch, state,
				s.Msgs, s.Tokens, s.Modified.UTC(), s.Tmux != nil,
				s.Agent.ResumeCmd(s.ID), s.Path,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rows)
		return
	}

	for _, s := range sessions {
		fmt.Printf("%-7s %-2s %-9s %8s %-22s %-46s %s\n",
			s.Agent, s.State.Icon(), relTime(s.Modified), humanTokens(s.Tokens),
			truncate(s.ShortCwd(), 22), truncate(firstLine(s.Name()), 46), s.ID)
	}
	fmt.Fprintf(os.Stderr, "\n%d sessions\n", len(sessions))
	for _, e := range ix.Errs {
		fmt.Fprintln(os.Stderr, "warn:", e)
	}
}

// installConf keeps ~/.config/orbit/tmux.conf in sync with the embedded copy,
// but only overwrites a file orbit itself wrote — edits you make by hand stick.
func installConf() error {
	path := confPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	case string(existing) == tmuxConf:
		return nil
	case !strings.HasPrefix(string(existing), confMarker):
		return nil // hand-edited or foreign; leave it alone
	}
	return os.WriteFile(path, []byte(tmuxConf), 0o644)
}

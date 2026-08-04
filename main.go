package main

import (
	_ "embed"
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
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("orbit", version)
		return
	}

	if *list {
		listSessions()
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

	mode := attachSmart
	switch {
	case *inline:
		mode = attachInline
	case *window:
		mode = attachWindow
	}

	m := newModel(mode)
	m.notify = NewNotifier(!*noNotify)
	defer m.notify.Close()

	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "orbit:", err)
		os.Exit(1)
	}
}

func listSessions() {
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
	for _, s := range sessions {
		fmt.Printf("%-7s %-2s %-9s %-22s %-46s %s\n",
			s.Agent, s.State.Icon(), relTime(s.Modified),
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

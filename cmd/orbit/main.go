// Command orbit is a dashboard for the coding-agent sessions on this machine.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/sadrig91/orbit/internal/config"
	"github.com/sadrig91/orbit/internal/tmux"
	"github.com/sadrig91/orbit/internal/ui"
)

// Set at build time: -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	var (
		window      = flag.Bool("window", false, "attach sessions in a new Ghostty window instead of a tab")
		inline      = flag.Bool("inline", false, "attach in this terminal instead of spawning a tab")
		noNotify    = flag.Bool("no-notify", false, "don't send desktop notifications when a session wants you")
		list        = flag.Bool("list", false, "print the session index as plain text and exit")
		asJSON      = flag.Bool("json", false, "print the session index as JSON and exit")
		probe       = flag.Bool("probe-logos", false, "render the agent logos to check terminal support")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	switch {
	case *showVersion:
		fmt.Println("orbit", version)
		return
	case *list || *asJSON:
		listSessions(*asJSON)
		return
	case *probe:
		if err := ui.ProbeLogos(os.Stdout); err != nil {
			die(err)
		}
		return
	}

	if err := requireTmux(); err != nil {
		die(err)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "orbit: config:", err, "(using defaults)")
	}
	if *noNotify {
		cfg.Notify = false
	}

	attach := ""
	switch {
	case *inline:
		attach = "inline"
	case *window:
		attach = "window"
	}

	m := ui.New(cfg, attach)
	defer m.Close()

	if _, err := tea.NewProgram(m).Run(); err != nil {
		die(err)
	}
}

// requireTmux fails early and with an actionable message: every session orbit
// starts lives on a tmux server, so there is nothing useful to do without it.
func requireTmux() error {
	if !tmux.Available() {
		return fmt.Errorf("tmux not found — brew install tmux")
	}
	return tmux.InstallConf()
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "orbit:", err)
	os.Exit(1)
}

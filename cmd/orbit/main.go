// Command orbit is a dashboard for the coding-agent sessions on this machine.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/sadrig91/orbit/internal/config"
	"github.com/sadrig91/orbit/internal/debug"
	"github.com/sadrig91/orbit/internal/hooks"
	"github.com/sadrig91/orbit/internal/term"
	"github.com/sadrig91/orbit/internal/tmux"
	"github.com/sadrig91/orbit/internal/ui"
)

// Set at build time: -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	// `orbit hook <agent> <event>` — the hidden subcommand the agents' hook
	// systems call, dispatched before flags, config, tmux checks, anything.
	// Dispatch owns the contract (exit 0 whatever happened, silence on
	// stdout); it lives in the hooks package, next to where the stakes are
	// documented, so main only has to honour the exit code.
	if hooks.Dispatch(os.Args, os.Stdin) {
		os.Exit(0)
	}

	var (
		window      = flag.Bool("window", false, "attach sessions in a new Ghostty window instead of a tab")
		inline      = flag.Bool("inline", false, "attach in this terminal instead of spawning a tab")
		noNotify    = flag.Bool("no-notify", false, "don't send desktop notifications when a session wants you")
		noUpdate    = flag.Bool("no-update", false, "don't check for or install a newer orbit on start")
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

	// Armed before anything can wedge. A dashboard that stops responding is
	// otherwise undiagnosable: SIGQUIT would put the traceback in the alt
	// screen and kill the process with it.
	dumpPath := debug.ListenForDumps()

	// Before loading: settings added since the file was written are appended
	// to it, so a new feature is visible where people look for it. Values
	// already there are never touched, and a failure here is not worth
	// mentioning — the config still loads, defaults still apply.
	addedKeys, retiredKeys, _ := config.Sync()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "orbit: config:", err, "(using defaults)")
	}
	if *noNotify {
		cfg.Notify = false
	}
	if *noUpdate {
		cfg.Update.Auto = false
	}

	attach := ""
	switch {
	case *inline:
		attach = "inline"
	case *window:
		attach = "window"
	}

	m := ui.New(cfg, attach, version)
	defer m.Close()
	if len(addedKeys) > 0 {
		m.Warn("new settings added to your config: " + strings.Join(addedKeys, ", "))
	}
	if len(retiredKeys) > 0 {
		m.Warn("orbit now manages these and updates them with each release, so they " +
			"were removed from your config: " + strings.Join(retiredKeys, ", "))
	}
	m.Warn(term.Preflight())
	m.SetDumpPath(dumpPath)

	if _, err := tea.NewProgram(m).Run(); err != nil {
		die(err)
	}

	// An update installed while orbit was running: the process still in
	// memory is the old build, so hand the terminal to the new one. This is
	// deliberately after Run returns, when Bubble Tea has restored the
	// terminal — exec never comes back, so anything left undone stays undone.
	if exe := m.Relaunch(); exe != "" {
		m.Close()
		if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
			die(fmt.Errorf("restart after update: %w", err))
		}
	}
}

// requireTmux fails early and with an actionable message: every session orbit
// starts lives on a tmux server, so there is nothing useful to do without it.
func requireTmux() error {
	if !tmux.Available() {
		return fmt.Errorf("tmux not found — brew install tmux")
	}
	if err := tmux.CheckVersion(); err != nil {
		return err
	}
	return tmux.InstallConf()
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "orbit:", err)
	os.Exit(1)
}

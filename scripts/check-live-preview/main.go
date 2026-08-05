// Command check-live-preview drives the streaming preview in a real terminal.
//
//	go run ./scripts/check-live-preview
//
// The Go tests cover the protocol and the emulator, but not this: a control
// client only exists once the dashboard is running, has scanned, and has a
// cursor sitting on a live session. That needs a pty and a real orbit process.
//
// What it proves, in order:
//  1. orbit opens a control client once it settles on a live session
//  2. moving the cursor moves that one client rather than opening another
//  3. quitting takes the client with it
//  4. none of the above resizes anybody's session
//
// It creates two throwaway tmux sessions on orbit's server, borrowing the ids
// of two real transcripts so the dashboard shows them as live, and kills them
// on the way out. Nothing else is touched.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"

	"github.com/sadrig91/orbit/internal/tmux"
)

const (
	sessionA = "orbit-livecheck-a"
	sessionB = "orbit-livecheck-b"
)

var failed bool

func ok(format string, a ...any)  { fmt.Printf("\033[32m✓\033[0m "+format+"\n", a...) }
func bad(format string, a ...any) { failed = true; fmt.Printf("\033[31m✗\033[0m "+format+"\n", a...) }
func say(format string, a ...any) { fmt.Printf("\033[36m%s\033[0m\n", fmt.Sprintf(format, a...)) }

func tmuxCmd(args ...string) (string, error) {
	out, err := exec.Command("tmux", tmux.Args(args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func main() {
	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Println("tmux is required")
		os.Exit(1)
	}
	if err := tmux.InstallConf(); err != nil {
		fmt.Println("install conf:", err)
		os.Exit(1)
	}

	say("building orbit")
	if out, err := exec.Command("go", "build", "-o", "orbit", "./cmd/orbit").CombinedOutput(); err != nil {
		fmt.Printf("build failed: %v\n%s", err, out)
		os.Exit(1)
	}

	ids := transcriptIDs(2)
	if len(ids) < 2 {
		fmt.Println("need at least two claude transcripts to borrow ids from")
		os.Exit(1)
	}

	// Anything already attached is somebody else's and has to be discounted,
	// or a leak from an earlier run would be blamed on this one.
	baseline := countControlClients()
	if baseline > 0 {
		say("note: %d control client(s) already attached before we started", baseline)
	}

	// Two live sessions, so there is somewhere for the cursor to move to.
	say("creating two throwaway tmux sessions")
	for i, name := range []string{sessionA, sessionB} {
		spawn(name, ids[i])
		defer tmux.Kill(name)
	}
	sizesBefore := sessionSizes()

	say("launching orbit under a pty")
	cmd := exec.Command("./orbit", "--no-update", "--no-notify")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		fmt.Println("pty.Start:", err)
		os.Exit(1)
	}
	pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 200})
	go func() { // drain, or orbit blocks once the pty buffer fills
		buf := make([]byte, 4096)
		for {
			if _, err := ptmx.Read(buf); err != nil {
				return
			}
		}
	}()
	defer func() {
		ptmx.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// A cold scan over every transcript can take a while, so wait rather than
	// guess. The control client is the signal that the preview went live.
	first, err := waitForControlClient(90 * time.Second)
	if err != nil {
		bad("no control client appeared: %v", err)
		report()
		return
	}
	ok("orbit opened a control client, watching %s", first)

	// Moving the cursor must move the same client, not add a second one.
	fmt.Fprint(ptmx, "j")
	second, err := waitForControlSessionChange(first, 20*time.Second)
	if err != nil {
		bad("the client did not follow the cursor: %v", err)
	} else {
		ok("cursor moved and the client followed: %s -> %s", first, second)
	}
	if n := countControlClients(); n > baseline+1 {
		bad("%d control clients attached (%d before we started) — one connection should serve the whole dashboard", n, baseline)
	} else {
		ok("still exactly one control client of ours")
	}

	say("quitting")
	fmt.Fprint(ptmx, "q")
	if err := waitForNoControlClient(baseline, 20*time.Second); err != nil {
		bad("a control client outlived orbit: %v", err)
	} else {
		ok("quitting took the control client with it")
	}

	// The whole preview design rests on this: watching must be free.
	for name, before := range sizesBefore {
		if after := sessionSize(name); after != "" && after != before {
			bad("%s was resized: %s -> %s", name, before, after)
		}
	}
	ok("no session was resized")

	report()
}

func report() {
	if failed {
		fmt.Println("\n\033[31mFAILED\033[0m")
		os.Exit(1)
	}
	fmt.Println("\n\033[32mall checks passed\033[0m")
}

// transcriptIDs borrows ids from real sessions so the dashboard joins the
// throwaway tmux sessions to something and shows them as live.
func transcriptIDs(n int) []string {
	out, err := exec.Command("./orbit", "--list").Output()
	if err != nil {
		return nil
	}
	var ids []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() && len(ids) < n {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || f[0] != "claude" {
			continue
		}
		ids = append(ids, f[len(f)-1])
	}
	return ids
}

func spawn(name, id string) {
	cwd, _ := os.Getwd()
	// A steady trickle of output, so there is something for the stream to carry.
	tmuxCmd("new-session", "-d", "-s", name, "-x", "120", "-y", "40",
		`sh -c 'while :; do date +%T; sleep 1; done'`)
	tmuxCmd("set-option", "-t", name, "@orbit_session", id)
	tmuxCmd("set-option", "-t", name, "@orbit_agent", "claude")
	tmuxCmd("set-option", "-t", name, "@orbit_title", name)
	_ = cwd
}

// controlSession returns the session orbit's control client is watching, or ""
// if there isn't one.
func controlSession() string {
	out, err := tmuxCmd("list-clients", "-F", "#{client_control_mode} #{client_session}")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if mode, sess, ok := strings.Cut(strings.TrimSpace(line), " "); ok && mode == "1" {
			return sess
		}
	}
	return ""
}

func countControlClients() int {
	out, err := tmuxCmd("list-clients", "-F", "#{client_control_mode}")
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "1" {
			n++
		}
	}
	return n
}

func poll(d time.Duration, want func() bool) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if want() {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("gave up after %v", d)
}

func waitForControlClient(d time.Duration) (string, error) {
	var got string
	err := poll(d, func() bool { got = controlSession(); return got != "" })
	return got, err
}

func waitForControlSessionChange(from string, d time.Duration) (string, error) {
	var got string
	err := poll(d, func() bool {
		got = controlSession()
		return got != "" && got != from
	})
	return got, err
}

func waitForNoControlClient(baseline int, d time.Duration) error {
	return poll(d, func() bool { return countControlClients() <= baseline })
}

func sessionSizes() map[string]string {
	out, err := tmuxCmd("list-sessions", "-F", "#{session_name} #{window_width}x#{window_height}")
	if err != nil {
		return nil
	}
	res := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, size, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			res[name] = size
		}
	}
	return res
}

func sessionSize(name string) string { return sessionSizes()[name] }

package tmux

import (
	"os/exec"
	"strings"
	"testing"
)

// tmux 3.4 (Debian, Ubuntu 24.04) escapes control characters in command output,
// so the \x1f separator comes back as the literal four characters \037. Parsing
// only the raw form made every line collapse into a single field, which meant
// List returned nothing, every session resolved to Dormant, and the dashboard
// showed a wall of `·` — with no error, because a failed parse is
// indistinguishable from "no tmux server running yet".
func TestListParsesBothSeparatorForms(t *testing.T) {
	fields := []string{
		"cl-work-api-gateway-aaaaaaaa", // session_name
		"aaaaaaaa-bbbb-cccc",           // @orbit_session
		"claude",                       // @orbit_agent
		"1",                            // session_attached
		"1785865291",                   // session_activity
		"node",                         // pane_current_command
		"work/api-gateway · Refactor",  // @orbit_title
		"/home/u/work/api-gateway",     // session_path
		"1785865000",                   // session_created
	}

	for _, tc := range []struct {
		name string
		sep  string
	}{
		{"raw separator", fieldSep},
		{"escaped by tmux 3.4", fieldSepEscaped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseList(strings.Join(fields, tc.sep) + "\n")
			if len(got) != 1 {
				t.Fatalf("parsed %d sessions, want 1 — the separator was not understood", len(got))
			}
			s := got[0]
			if s.Name != fields[0] {
				t.Errorf("Name = %q, want %q", s.Name, fields[0])
			}
			if s.SessionID != fields[1] {
				t.Errorf("SessionID = %q, want %q", s.SessionID, fields[1])
			}
			if s.Agent != "claude" {
				t.Errorf("Agent = %q, want claude", s.Agent)
			}
			if !s.Attached {
				t.Error("Attached = false, want true")
			}
			if !s.AgentRunning {
				t.Error("AgentRunning = false, but pane_current_command is node")
			}
			if s.Title != fields[6] {
				t.Errorf("Title = %q, want %q", s.Title, fields[6])
			}
			if s.Cwd != fields[7] {
				t.Errorf("Cwd = %q, want %q", s.Cwd, fields[7])
			}
			if s.Activity.Unix() != 1785865291 {
				t.Errorf("Activity = %v", s.Activity)
			}
			if s.Created.Unix() != 1785865000 {
				t.Errorf("Created = %v", s.Created)
			}
		})
	}
}

// A pane sitting at a shell is what ShellOnly is built on, so the shell table
// has to survive the login-shell spelling tmux reports after an agent exits.
func TestShellPaneMeansAgentNotRunning(t *testing.T) {
	for _, cmd := range []string{"zsh", "-zsh", "bash", "fish", ""} {
		line := strings.Join([]string{"n", "id", "claude", "0", "0", cmd, "t", "/tmp", "0"}, fieldSep)
		s, ok := parseListLine(line)
		if !ok {
			t.Fatalf("%q did not parse", cmd)
		}
		if s.AgentRunning {
			t.Errorf("pane_current_command %q should read as a shell, not a running agent", cmd)
		}
	}
}

func TestListSkipsMalformedLines(t *testing.T) {
	good := strings.Join([]string{"n", "id", "claude", "0", "0", "node", "t", "/tmp", "0"}, fieldSep)
	got := parseList("garbage\n" + good + "\n\n")
	if len(got) != 1 {
		t.Fatalf("parsed %d sessions, want only the well-formed one", len(got))
	}
}

// OpenTab is the one call site that goes through a shell rather than exec, so
// its arguments have to survive word splitting. A macOS home directory with a
// space in it — "/Users/First Last" — would otherwise break the -f path in two
// and the tab would open to a failed attach.
func TestAttachShellCommandQuotesArguments(t *testing.T) {
	t.Setenv("HOME", "/Users/First Last")

	got := attachShellCommand("cl-work-api-gateway-aaaaaaaa")
	want := "tmux '-u' '-L' 'orbit' '-f' '/Users/First Last/.config/orbit/tmux.conf' 'attach' '-t' 'cl-work-api-gateway-aaaaaaaa'"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	// Round-trip through a real shell: the argv the command produces must match
	// the argv exec would have passed.
	out, err := exec.Command("sh", "-c", strings.Replace(got, "tmux ", "printf '%s\\n' ", 1)).Output()
	if err != nil {
		t.Fatalf("shell rejected the command: %v", err)
	}
	argv := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	wantArgv := Args("attach", "-t", "cl-work-api-gateway-aaaaaaaa")
	if len(argv) != len(wantArgv) {
		t.Fatalf("shell split into %d args, want %d: %q", len(argv), len(wantArgv), argv)
	}
	for i := range argv {
		if argv[i] != wantArgv[i] {
			t.Errorf("arg %d = %q, want %q", i, argv[i], wantArgv[i])
		}
	}
}

func TestShellQuoteHandlesQuotes(t *testing.T) {
	for _, s := range []string{"plain", "with space", "it's", `a"b`, `back\slash`, "$HOME", "`cmd`"} {
		out, err := exec.Command("sh", "-c", "printf '%s' "+shellQuote(s)).Output()
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("%q round-tripped as %q", s, out)
		}
	}
}

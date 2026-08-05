package tmux

import (
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
		"tab-876faca00",                // @orbit_tab
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
			if s.TabID != "tab-876faca00" {
				t.Errorf("TabID = %q, want tab-876faca00", s.TabID)
			}
		})
	}
}

// A pane sitting at a shell is what ShellOnly is built on, so the shell table
// has to survive the login-shell spelling tmux reports after an agent exits.
func TestShellPaneMeansAgentNotRunning(t *testing.T) {
	for _, cmd := range []string{"zsh", "-zsh", "bash", "fish", ""} {
		line := strings.Join([]string{"n", "id", "claude", "0", "0", cmd, "t", "/tmp", "0", ""}, fieldSep)
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
	good := strings.Join([]string{"n", "id", "claude", "0", "0", "node", "t", "/tmp", "0", ""}, fieldSep)
	got := parseList("garbage\n" + good + "\n\n")
	if len(got) != 1 {
		t.Fatalf("parsed %d sessions, want only the well-formed one", len(got))
	}
}

// AttachArgv is what terminals run to show a session; it must carry the same
// socket and config flags as every other invocation, or the tab would attach
// to the wrong server.
func TestAttachArgvMatchesArgs(t *testing.T) {
	got := AttachArgv("cl-work-api-gateway-aaaaaaaa")
	want := append([]string{"tmux"}, Args("attach", "-t", "cl-work-api-gateway-aaaaaaaa")...)
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// orbit's live preview attaches a control client to whatever is selected. If
// that counted as "attached", Enter would try to focus a terminal tab that was
// never opened, and every session you merely looked at would be marked as open.
func TestParseClientsIgnoresControlClients(t *testing.T) {
	lines := []string{
		strings.Join([]string{"cl-work-api", "0"}, fieldSep), // a real terminal
		strings.Join([]string{"cx-widgets", "1"}, fieldSep),  // orbit's preview
		strings.Join([]string{"cp-docs", "1"}, fieldSep),     // ditto
		strings.Join([]string{"cp-docs", "0"}, fieldSep),     // …and a real one too
	}
	got := parseClients(strings.Join(lines, "\n"))

	if !got["cl-work-api"] {
		t.Error("a real client did not register as attached")
	}
	if got["cx-widgets"] {
		t.Error("a control client counted as attached — this is the bug")
	}
	// Both kinds on one session still counts: the terminal really is open.
	if !got["cp-docs"] {
		t.Error("a session with both a control and a real client should count")
	}
}

func TestParseClientsHandlesNoClients(t *testing.T) {
	if got := parseClients(""); len(got) != 0 {
		t.Errorf("empty output = %v, want no entries", got)
	}
	// tmux 3.4 escapes the separator here as well.
	got := parseClients("cl-work-api" + fieldSepEscaped + "0")
	if !got["cl-work-api"] {
		t.Error("the escaped separator form was not understood")
	}
}

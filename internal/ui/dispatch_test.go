package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/sadrig91/orbit/internal/dispatch"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"
)

func press(m *Model, key string) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	return cmd
}

func typeInto(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func modelWithSession(t *testing.T, ag session.Agent) *Model {
	t.Helper()
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 120, 40
	m.all = []*session.Session{{
		Agent: ag, ID: "s1", Cwd: format.Home("work", "proj"),
		Title: "an existing session", Modified: time.Now(),
	}}
	m.rebuild()
	return m
}

// The prompt is shared with the quick filter and the full-text search, and
// each leaves it configured differently. `d` has to set it up and Esc has to
// put it back — otherwise the next `/` inherits a 4000-character budget and
// the words "what should it do?".
func TestDispatchPromptSetsUpAndTearsDown(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	press(m, "d")

	if !m.dispatching || !m.filtering {
		t.Fatalf("d did not open the dispatch prompt: dispatching=%v filtering=%v", m.dispatching, m.filtering)
	}
	if m.dispatchTo != session.Codex {
		t.Errorf("target agent = %v, want the selected session's", m.dispatchTo)
	}
	if !strings.Contains(m.filter.Prompt, "codex") {
		t.Errorf("prompt %q does not say which agent it will dispatch", m.filter.Prompt)
	}
	if m.filter.CharLimit <= 80 {
		t.Errorf("CharLimit = %d, too short for a task description", m.filter.CharLimit)
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.dispatching || m.filtering {
		t.Error("esc left the dispatch prompt open")
	}
	if m.filter.Prompt != "/" || m.filter.CharLimit != 80 {
		t.Errorf("esc left the input as %q/%d, want the quick filter back", m.filter.Prompt, m.filter.CharLimit)
	}
}

// Typing a task must not filter the list underneath it. The quick filter is
// live; the other two prompts wait for Enter.
func TestDispatchTypingDoesNotFilterTheList(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	press(m, "d")
	typeInto(m, "zzz nothing matches this")
	if len(m.view) != 1 {
		t.Errorf("the list shrank to %d rows while a task was being typed", len(m.view))
	}
}

// The target is captured when `d` is pressed, not read back when Enter is: a
// scan landing mid-typing re-sorts the list and moves the cursor, and the task
// belongs to the session you were looking at when you started writing it.
func TestDispatchTargetSurvivesTheListMovingUnderneath(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	press(m, "d")
	want := m.dispatchInto

	m.all = append([]*session.Session{{
		Agent: session.Codex, ID: "s2", Cwd: format.Home("elsewhere"),
		Title: "arrived mid-typing", Modified: time.Now(),
	}}, m.all...)
	m.rebuild()

	if m.dispatchTo != session.Claude || m.dispatchInto != want {
		t.Errorf("target moved to %v/%s, want claude/%s", m.dispatchTo, m.dispatchInto, want)
	}
}

func TestDispatchIgnoresAnEmptyTask(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	if cmd := m.dispatch(session.Claude, "/tmp", "   "); cmd != nil {
		t.Error("an empty task started a dispatch")
	}
}

// Copilot's CLI requires --allow-all-tools to run non-interactively at all, so
// dispatching one means it may run any tool unattended. That is consent, not a
// default: `d` explains itself instead of doing it.
func TestDispatchCopilotNeedsConsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A stub on PATH, so the test exercises the consent check rather than
	// skipping on machines where copilot is not installed.
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "copilot"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := modelWithSession(t, session.Copilot)
	msg := m.dispatch(session.Copilot, "/tmp", "do a thing")()
	status, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("got %T, want a status message explaining the refusal", msg)
	}
	if !strings.Contains(string(status), "copilot_allow_all_tools") {
		t.Errorf("status %q does not say how to allow it", status)
	}

	m.cfg.Dispatch.CopilotAllowAllTools = true
	if cmd := m.dispatch(session.Copilot, "/tmp", "do a thing"); cmd == nil {
		t.Error("consent given and it still refused")
	}
}

// A dispatch never attaches. The whole point is that it runs without a
// terminal — opening a tab for it would be the thing dispatch exists to avoid.
func TestDispatchedMessageDoesNotAttach(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	_, cmd := m.Update(dispatchedMsg{agent: "claude", cwd: "/w"})
	if cmd == nil {
		t.Fatal("no follow-up command at all")
	}
	if !strings.Contains(m.status, "claude") {
		t.Errorf("status = %q, want it to say what was dispatched", m.status)
	}

	m2 := modelWithSession(t, session.Claude)
	m2.Update(dispatchedMsg{agent: "claude", cwd: "/w", err: "tmux said no"})
	if !strings.Contains(m2.status, "tmux said no") {
		t.Errorf("status = %q, want the failure surfaced", m2.status)
	}
}

func TestDispatchNamesAndTitles(t *testing.T) {
	t.Setenv("HOME", "/home/jane")
	rec := &dispatch.Record{Agent: "codex", Cwd: "/home/jane/work/orbit", Prompt: "look at\nfeature X"}

	if got := dispatchName(rec); got != "cx-work-orbit-d" {
		t.Errorf("dispatchName = %q, want cx-work-orbit-d", got)
	}
	title := dispatchTitle(rec)
	if strings.ContainsAny(title, "\n\r") {
		t.Errorf("dispatchTitle = %q, must be one line — tmux titles cannot wrap", title)
	}
	if !strings.Contains(title, "look at") {
		t.Errorf("dispatchTitle = %q, want the task in it", title)
	}
}

func TestDispatchLine(t *testing.T) {
	tests := []struct {
		name       string
		rec        dispatch.Record
		wantLabel  string
		wantDetail string
	}{
		{"working", dispatch.Record{Status: dispatch.Running, Activity: "Bash: go test"}, "dispatched · working", "Bash: go test"},
		{"just started", dispatch.Record{Status: dispatch.Running}, "dispatched · working", "starting…"},
		{"handed back", dispatch.Record{Status: dispatch.NeedsYou, Pending: "Bash: rm -rf x"}, "dispatched · stopped for you", "Bash: rm -rf x"},
		{"failed", dispatch.Record{Status: dispatch.Failed, Err: "timed out"}, "dispatched · failed", "timed out"},
		{"done", dispatch.Record{Status: dispatch.Done}, "dispatched · done", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, detail := dispatchLine(&tt.rec)
			if label != tt.wantLabel || detail != tt.wantDetail {
				t.Errorf("got %q/%q, want %q/%q", label, detail, tt.wantLabel, tt.wantDetail)
			}
		})
	}
}

// The detail pane is where you find out what a dispatch is waiting for, and
// how to get it moving again.
func TestDetailPaneShowsAHandoff(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	m.all[0].Dispatch = &dispatch.Record{
		ID: "d1", Agent: "claude", SessionID: "s1", Status: dispatch.NeedsYou,
		Prompt: "delete the build directory", Pending: "Bash: rm -rf build",
	}
	m.all[0].State = session.NeedsApproval
	m.rebuild()

	out := stripANSI(strings.Join(m.detail(70, 30), "\n"))
	for _, want := range []string{"stopped for you", "rm -rf build", "delete the build directory", "takes it over"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail pane missing %q:\n%s", want, out)
		}
	}
}

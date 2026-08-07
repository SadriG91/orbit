package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sadrig91/orbit/internal/dispatch"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"
)

func press(m *Model, key string) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	return cmd
}

func reviewDispatch(m *Model) {
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
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

// Dispatch has its own multiline task editor, leaving the quick filter's
// value and limits independent.
func TestDispatchPromptSetsUpAndTearsDown(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	press(m, "d")

	if !m.dispatching || m.filtering {
		t.Fatalf("d did not open the dispatch prompt: dispatching=%v filtering=%v", m.dispatching, m.filtering)
	}
	if m.dispatchTo != session.Codex {
		t.Errorf("target agent = %v, want the selected session's", m.dispatchTo)
	}
	if got := m.dispatchDir.Value(); got != m.dispatchInto {
		t.Errorf("directory field = %q, want selected directory %q", got, m.dispatchInto)
	}
	if !strings.Contains(stripANSI(m.render()), "@codex (default)") {
		t.Error("composer does not show the default agent")
	}
	if m.dispatchTask.CharLimit <= 80 {
		t.Errorf("CharLimit = %d, too short for a task description", m.dispatchTask.CharLimit)
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.dispatching || m.filtering {
		t.Error("esc left the dispatch prompt open")
	}
	if m.filter.Prompt != "/" || m.filter.CharLimit != 80 {
		t.Errorf("esc left the input as %q/%d, want the quick filter back", m.filter.Prompt, m.filter.CharLimit)
	}
	if m.dispatchDir.Focused() {
		t.Error("esc left the directory focused")
	}
}

func TestDispatchOpensWithoutASelectedSession(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 80, 24
	press(m, "d")
	if !m.dispatching {
		t.Fatal("d did nothing on an empty dashboard")
	}
	want, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := m.dispatchDir.Value(); got != want {
		t.Errorf("directory = %q, want Orbit working directory %q", got, want)
	}
}

func TestParseDispatchPrompt(t *testing.T) {
	tests := []struct {
		in         string
		fallback   session.Agent
		wantAgent  session.Agent
		wantPrompt string
		wantErr    string
	}{
		{"check feature X", session.Claude, session.Claude, "check feature X", ""},
		{"  @codex can you check feature X?  ", session.Claude, session.Codex, "can you check feature X?", ""},
		{"@CLAUDE fix it", session.Copilot, session.Claude, "fix it", ""},
		{"@copilot\tinspect this", session.Codex, session.Copilot, "inspect this", ""},
		{"@gemini check it", session.Claude, session.Claude, "", "unknown agent"},
		{"@codex", session.Claude, session.Codex, "", "add a task"},
		{"   ", session.Codex, session.Codex, "", "describe the task"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			ag, prompt, err := parseDispatchPrompt(tt.in, tt.fallback)
			if ag != tt.wantAgent || prompt != tt.wantPrompt {
				t.Errorf("got %s/%q, want %s/%q", ag, prompt, tt.wantAgent, tt.wantPrompt)
			}
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestResolveDispatchDir(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", base)

	for _, input := range []string{"project", "~/project", project} {
		got, err := resolveDispatchDir(input, base)
		if err != nil || got != project {
			t.Errorf("resolveDispatchDir(%q) = %q, %v; want %q", input, got, err, project)
		}
	}
	if _, err := resolveDispatchDir("missing", base); err == nil {
		t.Error("nonexistent directory was accepted")
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDispatchDir(file, base); err == nil {
		t.Error("regular file was accepted as a directory")
	}
}

func TestDispatchComposerFocusAndValidation(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	press(m, "d")
	m.dispatchTask.SetValue("@codex check feature X")

	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.dispatchDirFocused || !m.dispatchDir.Focused() || m.dispatchTask.Focused() {
		t.Fatal("Tab did not move focus from task to directory")
	}
	m.dispatchDir.SetValue("")
	typeInto(m, filepath.Join(t.TempDir(), "missing"))
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // directory Enter returns to task
	if m.dispatchDirFocused {
		t.Fatal("Enter in directory did not return focus to task")
	}
	reviewDispatch(m) // Ctrl+Enter validates and opens review
	if !m.dispatching || !m.dispatchDirFocused {
		t.Fatal("invalid directory closed the composer instead of focusing the field")
	}
	if !strings.Contains(m.status, "directory") {
		t.Errorf("validation status = %q, want a directory error", m.status)
	}
}

func TestDispatchComposerRejectsUnknownMention(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	press(m, "d")
	m.dispatchTask.SetValue("@gemini check feature X")
	reviewDispatch(m)
	if !m.dispatching || m.dispatchDirFocused {
		t.Fatal("unknown agent did not leave the task field open")
	}
	if !strings.Contains(m.status, "@claude, @codex, or @copilot") {
		t.Errorf("status = %q, want supported agents", m.status)
	}
}

func TestDispatchComposerOwnsAn80x24Frame(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	m.w, m.h = 80, 24
	press(m, "d")
	out := m.render()
	plain := stripANSI(out)
	for _, want := range []string{"SESSIONS", "DISPATCH TASK", "AGENT", "TASK · FOCUSED", "DIRECTORY", "@claude (default)", "task / directory"} {
		if !strings.Contains(plain, want) {
			t.Errorf("composer missing %q:\n%s", want, plain)
		}
	}
	if got := lipgloss.Height(out); got != 24 {
		t.Errorf("composer is %d rows, want 24", got)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(stripANSI(line)); got > 80 {
			t.Errorf("line %d is %d columns, want at most 80", i, got)
		}
	}
}

func TestDispatchDirectoryPickerUsesKnownProjects(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	other := filepath.Join(t.TempDir(), "another-project")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	m.all = append(m.all, &session.Session{
		Agent: session.Codex, ID: "s2", Cwd: other, Title: "another", Modified: time.Now().Add(-time.Minute),
	})
	m.rebuild()
	press(m, "d")

	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.dispatchDirFocused || m.dispatchDirCursor != 0 {
		t.Fatalf("directory picker did not select its default: focused=%v cursor=%d", m.dispatchDirFocused, m.dispatchDirCursor)
	}
	frame := stripANSI(m.render())
	if !strings.Contains(frame, "recent project directories") {
		t.Errorf("inline directory picker is not visible:\n%s", frame)
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.dispatchDir.Value(); got != other {
		t.Errorf("Down selected %q, want %q", got, other)
	}
	if len(m.view) != 2 {
		t.Error("directory picker changed the mounted Sessions list")
	}
}

func TestDispatchDirectoryPickerScrollsToItsCursor(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	for i := 0; i < 8; i++ {
		m.all = append(m.all, &session.Session{
			Agent: session.Codex, ID: fmt.Sprintf("extra-%d", i),
			Cwd: fmt.Sprintf("/work/project-%d", i), Title: "extra", Modified: time.Now(),
		})
	}
	m.rebuild()
	press(m, "d")
	m.dispatchDirFocused = true
	choices := m.dispatchDirectories()
	m.dispatchDirCursor = 7
	m.dispatchDir.SetValue(choices[7])

	frame := stripANSI(strings.Join(m.dispatchLines(50, 20), "\n"))
	if !strings.Contains(frame, "▸ "+choices[7]) {
		t.Errorf("picker cursor scrolled off-screen:\n%s", frame)
	}
}

func TestDispatchUsesMentionAsItsOnlyAgentSelector(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	press(m, "d")
	if m.composerAgentFocused {
		t.Fatal("dispatch composer retained a separate agent focus stop")
	}
	m.dispatchTask.SetValue("@codex inspect the change")
	plain := stripANSI(m.render())
	if !strings.Contains(plain, "@codex (from task)") || strings.Contains(plain, "[claude]") {
		t.Errorf("mention did not become the sole visible selector:\n%s", plain)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.dispatchDirFocused {
		t.Fatal("Tab did not move directly from task to directory")
	}
}

func TestDispatchReviewAndFailedLaunchPreserveDraft(t *testing.T) {
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "codex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	m := modelWithSession(t, session.Claude)
	press(m, "d")
	m.dispatchTask.SetValue("@codex inspect the change")
	m.dispatchDir.SetValue(dir)
	reviewDispatch(m)
	if m.dispatchReview == nil || m.dispatchReview.agent != session.Codex {
		t.Fatalf("Enter did not open review with the resolved mention: %#v", m.dispatchReview)
	}
	plain := stripANSI(m.render())
	for _, want := range []string{"REVIEW DISPATCH", "READY TO DISPATCH", "codex", "DIRECTORY", "inspect the change"} {
		if !strings.Contains(plain, want) {
			t.Errorf("review missing %q:\n%s", want, plain)
		}
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.dispatchReview != nil || m.dispatchTask.Value() != "@codex inspect the change" || !m.dispatchTask.Focused() {
		t.Fatal("Esc did not return the intact draft to editing")
	}

	draft := &dispatchDraft{agent: session.Codex, cwd: dir, task: "inspect the change"}
	m.dispatchLaunching = draft
	m.dispatching = false
	m.filtering = false
	m.Update(dispatchedMsg{agent: "codex", cwd: dir, err: "tmux failed", draft: draft})
	if !m.dispatching || m.dispatchLaunching != nil || m.dispatchTask.Value() != draft.task || m.dispatchDir.Value() != dir {
		t.Fatalf("failed launch lost its draft: dispatching=%v launching=%v task=%q dir=%q", m.dispatching, m.dispatchLaunching != nil, m.dispatchTask.Value(), m.dispatchDir.Value())
	}
}

func TestRetryDispatchRestoresAgentTaskAndDirectory(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	dir := t.TempDir()
	m.all[0].Dispatch = &dispatch.Record{
		ID: "d1", Agent: "codex", Cwd: dir, Prompt: "finish the tests", Status: dispatch.Failed,
	}
	m.rebuild()
	m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})

	if !m.dispatching || m.dispatchTo != session.Codex {
		t.Fatalf("retry did not reopen the codex composer: dispatching=%v agent=%s", m.dispatching, m.dispatchTo)
	}
	if got := m.dispatchTask.Value(); got != "@codex finish the tests" {
		t.Errorf("retry task = %q", got)
	}
	if got := m.dispatchDir.Value(); got != dir {
		t.Errorf("retry directory = %q, want %q", got, dir)
	}
}

func TestDispatchLogActionIsAvailableOnlyWhenLogExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m := modelWithSession(t, session.Codex)
	m.all[0].Dispatch = &dispatch.Record{ID: "d1", Agent: "codex", Status: dispatch.Done}
	m.rebuild()
	if cmd := m.openDispatchLog(); cmd != nil || !strings.Contains(m.status, "not available") {
		t.Fatalf("missing log action = cmd:%v status:%q", cmd != nil, m.status)
	}

	if err := os.MkdirAll(dispatch.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dispatch.LogPath("d1"), []byte("run output"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "less"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if cmd := m.openDispatchLog(); cmd == nil {
		t.Fatal("existing dispatch log did not produce a pager command")
	}
}

func TestNewSessionComposerWorksWithoutSessions(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 80, 24
	press(m, "n")
	if !m.newing || !m.composerAgentFocused {
		t.Fatalf("n did not open the agent-first composer: newing=%v agentFocused=%v", m.newing, m.composerAgentFocused)
	}
	plain := stripANSI(m.render())
	for _, want := range []string{"NEW SESSION", "AGENT · FOCUSED", "[", "DIRECTORY", "start"} {
		if !strings.Contains(plain, want) {
			t.Errorf("new composer missing %q:\n%s", want, plain)
		}
	}
	if got := lipgloss.Height(m.render()); got != 24 {
		t.Errorf("new composer is %d rows, want 24", got)
	}
}

func TestNewSessionComposerSelectsAgentAndValidatesDirectory(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	press(m, "n")
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.dispatchTo != session.Codex {
		t.Errorf("Right selected %s, want codex", m.dispatchTo)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.dispatchDirFocused {
		t.Fatal("Tab did not focus the new-session directory")
	}
	m.dispatchDir.SetValue(filepath.Join(t.TempDir(), "missing"))
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.newing || !m.dispatchDirFocused || !strings.Contains(m.status, "directory") {
		t.Fatalf("invalid directory did not stay open and focused: newing=%v focused=%v status=%q", m.newing, m.dispatchDirFocused, m.status)
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

func TestDispatchTaskAcceptsMultipleLines(t *testing.T) {
	m := modelWithSession(t, session.Claude)
	press(m, "d")
	typeInto(m, "inspect the parser")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	typeInto(m, "then run the tests")

	if got := m.dispatchTask.Value(); got != "inspect the parser\nthen run the tests" {
		t.Errorf("multiline task = %q", got)
	}
	if m.dispatchReview != nil {
		t.Error("plain Enter submitted instead of inserting a newline")
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
		{"cancelled", dispatch.Record{Status: dispatch.Cancelled}, "dispatched · cancelled", "stopped from orbit"},
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

func TestDetailPaneShowsDispatchProgressAndResult(t *testing.T) {
	m := modelWithSession(t, session.Codex)
	now := time.Now()
	m.all[0].Dispatch = &dispatch.Record{
		ID: "d1", Agent: "codex", SessionID: m.all[0].ID, Status: dispatch.Done,
		Prompt: "finish polish", Activities: []string{"Read README", "Go test ./..."},
		Result: "All checks pass.", Started: now.Add(-time.Minute), Ended: now, Updated: now,
	}
	m.all[0].State = session.YourTurn
	m.rebuild()

	out := stripANSI(strings.Join(m.detail(70, 35), "\n"))
	for _, want := range []string{"duration", "timeout", "RECENT ACTIVITY", "Read README", "Go test ./...", "RESULT", "All checks pass."} {
		if !strings.Contains(out, want) {
			t.Errorf("detail pane missing %q:\n%s", want, out)
		}
	}
}

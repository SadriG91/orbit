package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Payloads as each agent actually sends them — captured from live runs, not
// composed. Claude and codex spell the id session_id, copilot sessionId.
const (
	claudePayload  = `{"session_id":"4da26e73-aaaa-bbbb-cccc-000000000001","transcript_path":"/x","cwd":"/y","hook_event_name":"PermissionRequest","tool_name":"Bash"}`
	codexPayload   = `{"session_id":"019fd218-25ee-7890-91bc-a61b87219695","turn_id":"t1","cwd":"/y","hook_event_name":"Stop"}`
	copilotPayload = `{"timestamp":1785936000,"cwd":"/y","sessionId":"ef5477fa-c9ad-4138-8993-878e7ad55337","toolName":"bash"}`
)

func TestRecordMapsEventsToStates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		agent, event, payload, id string
		want                      Status
	}{
		{"claude", "PermissionRequest", claudePayload, "4da26e73-aaaa-bbbb-cccc-000000000001", NeedsYou},
		{"claude", "Stop", claudePayload, "4da26e73-aaaa-bbbb-cccc-000000000001", YourTurn},
		{"claude", "UserPromptSubmit", claudePayload, "4da26e73-aaaa-bbbb-cccc-000000000001", Working},
		{"claude", "PermissionDenied", claudePayload, "4da26e73-aaaa-bbbb-cccc-000000000001", Working},
		{"codex", "Stop", codexPayload, "019fd218-25ee-7890-91bc-a61b87219695", YourTurn},
		{"copilot", "agentStop", copilotPayload, "ef5477fa-c9ad-4138-8993-878e7ad55337", YourTurn},
		{"copilot", "preToolUse", copilotPayload, "ef5477fa-c9ad-4138-8993-878e7ad55337", Working},
	}
	for _, tt := range tests {
		if err := Record(tt.agent, tt.event, strings.NewReader(tt.payload)); err != nil {
			t.Fatalf("%s/%s: %v", tt.agent, tt.event, err)
		}
		st, ok := Load(tt.agent, tt.id)
		if !ok {
			t.Fatalf("%s/%s: no state recorded", tt.agent, tt.event)
		}
		if st.Status != tt.want {
			t.Errorf("%s/%s: status = %q, want %q", tt.agent, tt.event, st.Status, tt.want)
		}
		if st.At.IsZero() {
			t.Errorf("%s/%s: no timestamp", tt.agent, tt.event)
		}
	}
}

// A permission prompt reads as needing you until the agent says otherwise —
// and PostToolUse right after approval is the edge that says otherwise.
func TestApprovalClearsOnPostToolUse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "4da26e73-aaaa-bbbb-cccc-000000000001"

	Record("claude", "PermissionRequest", strings.NewReader(claudePayload))
	if st, _ := Load("claude", id); st.Status != NeedsYou {
		t.Fatalf("after PermissionRequest: %q", st.Status)
	}
	Record("claude", "PostToolUse", strings.NewReader(claudePayload))
	if st, _ := Load("claude", id); st.Status != Working {
		t.Errorf("after PostToolUse: %q, want working", st.Status)
	}
}

// SessionEnd removes the file. A state file for a finished session is not
// stale data, it is a false claim waiting for a session id to be reused
// against it.
func TestSessionEndRemovesTheState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "4da26e73-aaaa-bbbb-cccc-000000000001"

	Record("claude", "Stop", strings.NewReader(claudePayload))
	if _, ok := Load("claude", id); !ok {
		t.Fatal("no state to remove")
	}
	Record("claude", "SessionEnd", strings.NewReader(claudePayload))
	if _, ok := Load("claude", id); ok {
		t.Error("SessionEnd left the state behind")
	}
}

// The hook runs inside the agent's loop; on copilot a failure denies the
// user's tool call. So garbage in must produce silence out — an error return
// for tests, but never a panic, and never a file.
func TestGarbageNeverPanicsOrWrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for _, in := range []string{"", "not json", `{"no_id": true}`, `[1,2,3]`, `{"session_id": 42}`} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("input %q panicked: %v", in, r)
				}
			}()
			_ = Record("claude", "Stop", strings.NewReader(in))
		}()
	}
	if entries, err := os.ReadDir(Dir()); err == nil && len(entries) > 0 {
		t.Errorf("garbage produced %d state files", len(entries))
	}

	// Unknown agents and events are not ours to interpret.
	_ = Record("mystery-agent", "Stop", strings.NewReader(claudePayload))
	_ = Record("claude", "SomeFutureEvent", strings.NewReader(claudePayload))
	if entries, err := os.ReadDir(Dir()); err == nil && len(entries) > 0 {
		t.Errorf("unknown agent/event wrote state")
	}
}

// The id arrives over stdin and becomes a filename; nothing traversal-shaped
// may survive that trip.
func TestSessionIDIsSanitised(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	payload := `{"session_id":"../../../../tmp/evil"}`
	if err := Record("claude", "Stop", strings.NewReader(payload)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	entries, _ := os.ReadDir(Dir())
	for _, e := range entries {
		if strings.Contains(e.Name(), "..") || strings.Contains(e.Name(), "/") {
			t.Errorf("unsafe state filename %q", e.Name())
		}
	}
	// And it stayed inside the state dir.
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "..", "tmp", "evil")); err == nil {
		t.Error("a traversal id escaped the state directory")
	}
}

func TestStateFilesAreOwnerOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Record("claude", "Stop", strings.NewReader(claudePayload))

	di, err := os.Stat(Dir())
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode = %v, want 0700", di.Mode().Perm())
	}
	entries, _ := os.ReadDir(Dir())
	for _, e := range entries {
		info, _ := e.Info()
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", e.Name(), info.Mode().Perm())
		}
	}
}

func TestPruneSweepsOnlyOldFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Record("claude", "Stop", strings.NewReader(claudePayload))
	stale := filepath.Join(Dir(), "claude-old.json")
	os.WriteFile(stale, []byte(`{"status":"working"}`), 0o600)
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(stale, old, old)

	Prune(7 * 24 * time.Hour)

	if _, err := os.Stat(stale); err == nil {
		t.Error("a week-old state file survived the prune")
	}
	if _, ok := Load("claude", "4da26e73-aaaa-bbbb-cccc-000000000001"); !ok {
		t.Error("a fresh state file was swept")
	}
}

// The settings file is the whole contract with claude: PermissionRequest and
// not Notification (six seconds late, measured), a short explicit timeout
// (the default is far worse than a missed event — these run inside the
// agent's loop), and the orbit binary by absolute path, since a tmux login
// shell promises nothing about PATH.
func TestClaudeSettingsShape(t *testing.T) {
	b := ClaudeSettings("/opt/homebrew/bin/orbit")
	var cfg struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("the settings do not parse: %v", err)
	}
	for _, want := range []string{"SessionStart", "PermissionRequest", "PostToolUse", "PostToolUseFailure", "Stop", "UserPromptSubmit", "SessionEnd"} {
		if _, ok := cfg.Hooks[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	if _, ok := cfg.Hooks["Notification"]; ok {
		t.Error("subscribed to Notification, which lags the prompt by ~6s")
	}
	for ev, matchers := range cfg.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				if h.Command != "'/opt/homebrew/bin/orbit' hook claude "+ev {
					t.Errorf("%s command = %q", ev, h.Command)
				}
				if h.Timeout == 0 || h.Timeout > 10 {
					t.Errorf("%s timeout = %d, want short and explicit", ev, h.Timeout)
				}
			}
		}
	}
}

// SpawnArgs is what rides on the typed command. Only claude is wired; the
// others must get nothing rather than a flag their CLI will choke on.
func TestSpawnArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	executable = func() (string, error) { return "/opt/homebrew/bin/orbit", nil }
	t.Cleanup(func() { executable = os.Executable })

	got := SpawnArgs("claude")
	if !strings.HasPrefix(got, " --settings '") || !strings.HasSuffix(got, "'") {
		t.Fatalf("claude args = %q, want a quoted path", got)
	}
	path := strings.Trim(strings.TrimPrefix(got, " --settings "), "'")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the settings file was not written: %v", err)
	}
	// Idempotent: a second spawn must not churn the file.
	before, _ := os.Stat(path)
	if again := SpawnArgs("claude"); again != got {
		t.Errorf("second call = %q, want %q", again, got)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an unchanged settings file was rewritten")
	}

	for _, agent := range []string{"codex", "copilot", "unknown"} {
		if got := SpawnArgs(agent); got != "" {
			t.Errorf("SpawnArgs(%q) = %q, want empty until wired", agent, got)
		}
	}
}

// Dispatch owns the exit-0 contract, so it must claim any argv naming the
// subcommand — however mangled — and never let a panic escape. A truncated
// invocation falling through to the normal CLI would exit non-zero, and on
// copilot that denies the user's tool call.
func TestDispatchClaimsTheHookWordWhateverFollows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for _, args := range [][]string{
		{"orbit", "hook"},
		{"orbit", "hook", "claude"},
		{"orbit", "hook", "claude", "Stop"},
		{"orbit", "hook", "claude", "Stop", "extra", "junk"},
		{"orbit", "hook", "../../etc", "SessionEnd"},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%v panicked through Dispatch: %v", args, r)
				}
			}()
			if !Dispatch(args, strings.NewReader("garbage")) {
				t.Errorf("%v was not claimed", args)
			}
		}()
	}
	for _, args := range [][]string{{"orbit"}, {"orbit", "--list"}, {"orbit", "hooky", "x", "y"}} {
		if Dispatch(args, strings.NewReader("")) {
			t.Errorf("%v was wrongly claimed as a hook invocation", args)
		}
	}
}

// Agents run tool batches in parallel: an auto-approved read finishing a beat
// after a prompt appears must not clear it. Only the answer to the call that
// asked — the PostToolUse carrying the same tool id — does.
func TestForeignPostToolUseDoesNotClearAParkedPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const id = "4da26e73-aaaa-bbbb-cccc-000000000001"
	ask := `{"session_id":"` + id + `","tool_use_id":"toolu_bash"}`
	other := `{"session_id":"` + id + `","tool_use_id":"toolu_read"}`
	same := ask

	Record("claude", "PermissionRequest", strings.NewReader(ask))
	Record("claude", "PostToolUse", strings.NewReader(other))
	if st, _ := Load("claude", id); st.Status != NeedsYou {
		t.Fatalf("a foreign PostToolUse cleared the prompt: %q", st.Status)
	}
	// The real answer clears it.
	Record("claude", "PostToolUse", strings.NewReader(same))
	if st, _ := Load("claude", id); st.Status != Working {
		t.Errorf("the matching PostToolUse did not clear it: %q", st.Status)
	}
	// And an event with no id keeps last-writer behaviour rather than pinning.
	Record("claude", "PermissionRequest", strings.NewReader(`{"session_id":"`+id+`"}`))
	Record("claude", "PostToolUse", strings.NewReader(`{"session_id":"`+id+`"}`))
	if st, _ := Load("claude", id); st.Status != Working {
		t.Errorf("id-less events stopped clearing: %q", st.Status)
	}
}

// Forget is the resume-time hygiene: whatever the file claims is from a
// previous run, and a fresh agent at an idle prompt emits nothing to
// correct it.
func TestForgetDropsTheState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const id = "4da26e73-aaaa-bbbb-cccc-000000000001"
	Record("claude", "PermissionRequest", strings.NewReader(claudePayload))
	Forget("claude", id)
	if _, ok := Load("claude", id); ok {
		t.Error("Forget left the state behind")
	}
	Forget("claude", "never-existed") // must be a no-op, not a panic
}

// Copilot's Working is soft — with no approval event it only means "a tool
// started" — and the softness judgement must travel in the State, where the
// event tables live, not be re-derived by readers from agent names.
func TestCopilotWorkingIsMarkedSoft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const id = "ef5477fa-c9ad-4138-8993-878e7ad55337"

	Record("copilot", "preToolUse", strings.NewReader(copilotPayload))
	st, _ := Load("copilot", id)
	if !st.Soft {
		t.Error("copilot Working was not marked soft")
	}
	Record("copilot", "agentStop", strings.NewReader(copilotPayload))
	if st, _ = Load("copilot", id); st.Soft {
		t.Error("copilot YourTurn is definitive and must not be soft")
	}
	Record("claude", "PostToolUse", strings.NewReader(claudePayload))
	if st, _ = Load("claude", "4da26e73-aaaa-bbbb-cccc-000000000001"); st.Soft {
		t.Error("claude Working marked soft — it has a real approval event")
	}
}

// Both paths that reach a shell are quoted: the settings path typed into the
// spawn line, and the binary path Claude runs the hook command through.
func TestPathsWithSpacesAreQuoted(t *testing.T) {
	b := ClaudeSettings("/Users/jane doe/go/bin/orbit")
	if !strings.Contains(string(b), `'/Users/jane doe/go/bin/orbit' hook claude Stop`) {
		t.Errorf("the hook command is not quoted:\n%s", b)
	}

	home := filepath.Join(t.TempDir(), "jane doe")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	executable = func() (string, error) { return "/opt/homebrew/bin/orbit", nil }
	t.Cleanup(func() { executable = os.Executable })
	got := SpawnArgs("claude")
	if got == "" {
		t.Fatal("no spawn args for a HOME with a space")
	}
	want := " --settings '" + filepath.Join(home, ".cache", "orbit", "hooks", "claude.json") + "'"
	if got != want {
		t.Errorf("SpawnArgs = %q, want %q", got, want)
	}
}

// The SessionStart subscription is what overwrites a dead run's claim, but it
// must not fire on compaction, which happens mid-turn — hence the matcher.
func TestSessionStartIsMatcherLimited(t *testing.T) {
	b := ClaudeSettings("/opt/homebrew/bin/orbit")
	var cfg struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Hooks["SessionStart"][0].Matcher; got != "startup|resume|clear" {
		t.Errorf("SessionStart matcher = %q — compact would set your_turn mid-work", got)
	}
	if got := cfg.Hooks["Stop"][0].Matcher; got != "" {
		t.Errorf("Stop grew a matcher: %q", got)
	}
}

// A `go run` binary is deleted the moment the run exits; baking its path into
// a file real sessions read at startup would leave them invoking a ghost.
func TestSpawnArgsRefusesATempBinary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	executable = func() (string, error) { return filepath.Join(os.TempDir(), "go-build123", "orbit"), nil }
	t.Cleanup(func() { executable = os.Executable })
	if got := SpawnArgs("claude"); got != "" {
		t.Errorf("SpawnArgs = %q for a temp-dir binary, want empty", got)
	}
}

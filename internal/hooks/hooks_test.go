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
			Run("claude", "Stop", strings.NewReader(in))
		}()
	}
	if entries, err := os.ReadDir(Dir()); err == nil && len(entries) > 0 {
		t.Errorf("garbage produced %d state files", len(entries))
	}

	// Unknown agents and events are not ours to interpret.
	Run("mystery-agent", "Stop", strings.NewReader(claudePayload))
	Run("claude", "SomeFutureEvent", strings.NewReader(claudePayload))
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
	b, err := ClaudeSettings("/opt/homebrew/bin/orbit")
	if err != nil {
		t.Fatal(err)
	}
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
	for _, want := range []string{"PermissionRequest", "PostToolUse", "Stop", "UserPromptSubmit", "SessionEnd"} {
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
				if h.Command != "/opt/homebrew/bin/orbit hook claude "+ev {
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

	got := SpawnArgs("claude")
	if !strings.HasPrefix(got, " --settings ") {
		t.Fatalf("claude args = %q", got)
	}
	path := strings.TrimPrefix(got, " --settings ")
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

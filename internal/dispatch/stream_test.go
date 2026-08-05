package dispatch

import "testing"

// The lines below are real ones, copied from runs of each CLI rather than
// written from its documentation. That matters more here than usual: the
// previous attempt at inferring session state was reverted precisely because
// its codex and copilot patterns were invented rather than sampled, and every
// one of the surprises these parsers exist to handle — copilot's sessionId
// appearing only on the result event, codex reporting a sandbox refusal as an
// ordinary non-zero exit, claude's approval arriving as a control request —
// would have been missed by a test written from the docs.

func TestParseClaude(t *testing.T) {
	tests := []struct {
		name string
		line string
		want step
	}{
		{
			name: "init carries the session id",
			line: `{"type":"system","subtype":"init","cwd":"/tmp/x","session_id":"19abc310-e17a-4fc3-b102-f70545467078","model":"claude-opus-5","permissionMode":"auto"}`,
			want: step{sessionID: "19abc310-e17a-4fc3-b102-f70545467078", note: "session 19abc310-e17a-4fc3-b102-f70545467078"},
		},
		{
			name: "a tool call is the activity",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"curl -s https://example.com","description":"Check example.com"}}]},"session_id":"s"}`,
			want: step{activity: "Bash: curl -s https://example.com"},
		},
		{
			name: "text with no tool call is narration",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"I'll run that command."}]},"session_id":"s"}`,
			want: step{note: "I'll run that command."},
		},
		{
			name: "the approval that dispatch exists to catch",
			line: `{"type":"control_request","request_id":"14579fff-aa2d-4b07-b410-1d41a796b068","request":{"subtype":"can_use_tool","tool_name":"Bash","display_name":"Bash","input":{"command":"curl -s -o /dev/null -w '%{http_code}' https://example.com","description":"Check HTTP status code for example.com"},"decision_reason":"This command requires approval","decision_reason_type":"other","tool_use_id":"toolu_vrtx_01XkCo2C2EEBDgTgAfbXbMrV"}}`,
			want: step{ask: &ask{
				requestID: "14579fff-aa2d-4b07-b410-1d41a796b068",
				tool:      "Bash",
				detail:    "Bash: curl -s -o /dev/null -w '%{http_code}' https://example.com — This command requires approval",
			}},
		},
		{
			name: "an unrecognised control request still has to be answered",
			line: `{"type":"control_request","request_id":"abc","request":{"subtype":"something_new"}}`,
			want: step{ask: &ask{requestID: "abc"}},
		},
		{
			name: "success ends the turn",
			line: `{"type":"result","subtype":"success","is_error":false,"result":"Output: 200","session_id":"s"}`,
			want: step{done: true, note: "Output: 200"},
		},
		{
			name: "an error result is a failure, not an ending",
			line: `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"","session_id":"s"}`,
			want: step{err: "error_during_execution"},
		},
		{
			name: "a tool result is not itself worth reporting",
			line: `{"type":"user","message":{"content":[{"tool_use_id":"toolu_01","type":"tool_result","content":"200","is_error":false}]},"session_id":"s"}`,
		},
		{name: "junk is ignored", line: `not json at all`},
	}
	runParserTests(t, parseClaude, tests)
}

func TestParseCodex(t *testing.T) {
	tests := []struct {
		name string
		line string
		want step
	}{
		{
			name: "thread.started is the only place the id appears",
			line: `{"type":"thread.started","thread_id":"019fd26c-d00c-7413-a16b-956a44842bd0"}`,
			want: step{sessionID: "019fd26c-d00c-7413-a16b-956a44842bd0", note: "thread 019fd26c-d00c-7413-a16b-956a44842bd0"},
		},
		{
			name: "a running command is the activity",
			line: `{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc \"curl -s https://example.com\"","status":"in_progress"}}`,
			want: step{activity: `shell: /bin/zsh -lc "curl -s https://example.com"`},
		},
		{
			// This is how a sandbox refusal surfaces: exit 6 and nothing else.
			// Codex has no approval event in exec mode, so a non-zero exit is
			// narrated but never treated as the run failing.
			name: "a failed command is narrated, not fatal",
			line: `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"curl https://example.com","exit_code":6,"status":"failed"}}`,
			want: step{
				activity: "shell: curl https://example.com",
				note:     "exit 6: curl https://example.com",
			},
		},
		{
			name: "turn.completed ends the run",
			line: `{"type":"turn.completed","usage":{"input_tokens":30766,"output_tokens":125}}`,
			want: step{done: true},
		},
		{
			name: "an object-shaped error yields its message",
			line: `{"type":"turn.failed","error":{"message":"model overloaded"}}`,
			want: step{err: "model overloaded"},
		},
		{
			name: "a string-shaped error does too",
			line: `{"type":"turn.failed","error":"stream disconnected"}`,
			want: step{err: "stream disconnected"},
		},
		{
			name: "an agent message is narration",
			line: `{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"The command exited with status 6."}}`,
			want: step{note: "The command exited with status 6."},
		},
	}
	runParserTests(t, parseCodex, tests)
}

func TestParseCopilot(t *testing.T) {
	tests := []struct {
		name string
		line string
		want step
	}{
		{
			name: "a tool call delta names the tool",
			line: `{"type":"assistant.tool_call_delta","data":{"toolCallId":"call_n7hm","toolName":"bash","toolType":"function","inputDelta":"{\""},"ephemeral":true}`,
			want: step{activity: "bash"},
		},
		{
			name: "the final message is narration",
			line: `{"type":"assistant.message","data":{"messageId":"ed6b","model":"gpt-5.6-sol","content":"MANGO-9.","phase":"final_answer"}}`,
			want: step{note: "MANGO-9."},
		},
		{
			name: "result carries the session id and ends the run",
			line: `{"type":"result","timestamp":"2026-08-05T15:02:29.292Z","sessionId":"11111111-2222-4333-8444-555555555555","exitCode":0}`,
			want: step{sessionID: "11111111-2222-4333-8444-555555555555", done: true},
		},
		{
			name: "a non-zero exit is a failure",
			line: `{"type":"result","sessionId":"s","exitCode":2}`,
			want: step{sessionID: "s", err: "copilot exited 2"},
		},
		{
			// What a copilot that had to be signalled emitted on the way out.
			name: "abort reports why",
			line: `{"type":"abort","data":{"reason":"user_initiated"}}`,
			want: step{err: "aborted: user_initiated"},
		},
		{
			name: "startup chatter is ignored",
			line: `{"type":"session.mcp_server_status_changed","data":{"serverName":"github-mcp-server","status":"connected"},"ephemeral":true}`,
		},
	}
	runParserTests(t, newCopilotParser(), tests)
}

// TestCopilotCoalescesToolDeltas covers the reason copilot needs a fresh
// parser per run. One `echo` produced fifteen tool_call_delta events in a
// measured run, all with the same toolCallId; acting on each would rewrite the
// record fifteen times to say "bash".
func TestCopilotCoalescesToolDeltas(t *testing.T) {
	p := newCopilotParser()
	const first = `{"type":"assistant.tool_call_delta","data":{"toolCallId":"call_a","toolName":"bash","inputDelta":"{\""}}`
	const same = `{"type":"assistant.tool_call_delta","data":{"toolCallId":"call_a","toolName":"bash","inputDelta":"command"}}`
	const next = `{"type":"assistant.tool_call_delta","data":{"toolCallId":"call_b","toolName":"str_replace_editor","inputDelta":"{\""}}`

	if st, ok := p([]byte(first)); !ok || st.activity != "bash" {
		t.Fatalf("first delta: got %+v ok=%v, want activity bash", st, ok)
	}
	for i := 0; i < 14; i++ {
		if _, ok := p([]byte(same)); ok {
			t.Fatalf("delta %d of the same call was reported again", i+2)
		}
	}
	if st, ok := p([]byte(next)); !ok || st.activity != "str_replace_editor" {
		t.Fatalf("new call: got %+v ok=%v, want activity str_replace_editor", st, ok)
	}

	// A second run must not inherit the first run's last tool call, or its
	// opening delta would be swallowed.
	if st, ok := newCopilotParser()([]byte(next)); !ok || st.activity != "str_replace_editor" {
		t.Fatalf("fresh parser: got %+v ok=%v, want the call reported", st, ok)
	}
}

func TestToolLabel(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input map[string]any
		want  string
	}{
		{"command wins", "Bash", map[string]any{"command": "go test ./...", "description": "run tests"}, "Bash: go test ./..."},
		{"path when there is no command", "Read", map[string]any{"file_path": "/a/b.go"}, "Read: /a/b.go"},
		{"description is the last resort", "Task", map[string]any{"description": "look at feature X"}, "Task: look at feature X"},
		{"nothing recognisable degrades to the name", "Mystery", map[string]any{"widgets": 3}, "Mystery"},
		{"no input at all", "Mystery", nil, "Mystery"},
		{"blank values do not count", "Bash", map[string]any{"command": "   ", "file_path": "/a"}, "Bash: /a"},
		{
			"a heredoc is flattened to one line",
			"Bash", map[string]any{"command": "cat <<'EOF'\nline one\nline two\nEOF"},
			"Bash: cat <<'EOF' line one line two EOF",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolLabel(tt.tool, tt.input); got != tt.want {
				t.Errorf("toolLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOneLineTruncates(t *testing.T) {
	long := ""
	for i := 0; i < 50; i++ {
		long += "abcdefghij"
	}
	got := oneLine(long)
	if len([]rune(got)) != 161 { // 160 plus the ellipsis
		t.Errorf("oneLine kept %d runes, want 161", len([]rune(got)))
	}
}

func runParserTests(t *testing.T, p parser, tests []struct {
	name string
	line string
	want step
}) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := p([]byte(tt.line))
			wantOK := tt.want != step{} || tt.want.ask != nil
			if ok != wantOK {
				t.Fatalf("ok = %v, want %v (step %+v)", ok, wantOK, got)
			}
			if !ok {
				return
			}
			if got.sessionID != tt.want.sessionID || got.activity != tt.want.activity ||
				got.note != tt.want.note || got.done != tt.want.done || got.err != tt.want.err {
				t.Errorf("step = %+v, want %+v", got, tt.want)
			}
			switch {
			case (got.ask == nil) != (tt.want.ask == nil):
				t.Fatalf("ask = %v, want %v", got.ask, tt.want.ask)
			case got.ask == nil:
			case *got.ask != *tt.want.ask:
				t.Errorf("ask = %+v, want %+v", *got.ask, *tt.want.ask)
			}
		})
	}
}

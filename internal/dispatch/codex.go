package dispatch

import (
	"encoding/json"

	"github.com/sadrig91/orbit/internal/format"
)

// Codex's dispatch stream, from `codex exec --json`:
//
//	thread.started                  thread_id — the only place it appears
//	turn.started
//	item.started    command_execution / file_change / mcp_tool_call
//	item.completed  command_execution (exit_code, status), agent_message (text)
//	turn.completed  usage
//	turn.failed     error
//
// Codex has no approval channel here, and this was checked rather than
// assumed: `-c approval_policy=on-request` changes nothing in exec mode. A
// command the sandbox refused came back as a failed command_execution with
// exit 6 and the turn completed regardless — the run degrades instead of
// asking. The request types that would ask (exec_approval_request,
// apply_patch_approval_request) exist only in `codex mcp-server` and
// `codex app-server`, which are JSON-RPC and marked experimental.
//
// So a dispatched codex can be handed back when it finishes, but never
// mid-run. Adopting the app-server would change that and is the obvious next
// move if this turns out to matter.
//
// Sandbox and approval policy are deliberately not set on the command line:
// the user's ~/.codex/config.toml decides, exactly as it does when they run
// codex themselves. --skip-git-repo-check is passed because orbit dispatches
// into whatever directory a session lives in, and refusing to run outside a
// repository would make dispatch useless in half of them.

func codexArgv(r *Record) []string {
	return []string{
		"codex", "exec",
		"--json",
		"--skip-git-repo-check",
		"-C", r.Cwd,
		r.Prompt,
	}
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Error    any    `json:"error"`
	Item     *struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Command  string `json:"command"`
		Status   string `json:"status"`
		Path     string `json:"path"`
		Tool     string `json:"tool"`
		Server   string `json:"server"`
		ExitCode *int   `json:"exit_code"`
	} `json:"item"`
}

func parseCodex(line []byte) (step, bool) {
	var e codexEvent
	if json.Unmarshal(line, &e) != nil {
		return step{}, false
	}
	switch e.Type {
	case "thread.started":
		return step{sessionID: e.ThreadID, note: "thread " + e.ThreadID}, true

	case "item.started", "item.completed":
		if e.Item == nil {
			return step{}, false
		}
		switch e.Item.Type {
		case "command_execution":
			s := step{activity: "shell: " + oneLine(e.Item.Command)}
			// A non-zero exit is narrated but is not a failure of the run:
			// codex expects commands to fail and carries on. This is also
			// where a sandbox denial surfaces, since nothing else reports one.
			if e.Type == "item.completed" && e.Item.ExitCode != nil && *e.Item.ExitCode != 0 {
				s.note = "exit " + format.Itoa(*e.Item.ExitCode) + ": " + oneLine(e.Item.Command)
			}
			return s, true
		case "file_change":
			return step{activity: "edit: " + oneLine(e.Item.Path)}, true
		case "mcp_tool_call":
			return step{activity: e.Item.Server + ": " + e.Item.Tool}, true
		case "agent_message":
			if e.Type == "item.completed" && e.Item.Text != "" {
				return step{note: oneLine(e.Item.Text)}, true
			}
		}

	case "turn.completed":
		return step{done: true}, true

	case "turn.failed", "error":
		return step{err: oneLine(errText(e.Error))}, true
	}
	return step{}, false
}

// errText pulls something printable out of an error field whose shape is not
// documented — codex has used both a bare string and an object with a message.
func errText(v any) string {
	switch e := v.(type) {
	case string:
		return e
	case map[string]any:
		for _, k := range []string{"message", "error", "detail"} {
			if s, ok := e[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return "the turn failed"
}

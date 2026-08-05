package dispatch

import (
	"encoding/json"
)

// Claude's dispatch stream, verified by running it rather than from the docs:
//
//	system/init          session_id, and the permission mode in force
//	assistant            message.content blocks; tool_use is the activity
//	user                 tool_result blocks, including denials
//	control_request      can_use_tool — an approval, see below
//	result               the turn ended; subtype success or an error
//
// The approval is the reason this parser exists at all. Two runs of the same
// prompt, one with `--permission-prompt-tool stdio` and one without:
//
//	without   the tool is auto-denied. A tool_result arrives with is_error
//	          true and "This command requires approval", the model works
//	          around it, and the final result carries permission_denials.
//	          Nothing blocks and nothing asks — the run just quietly returns
//	          a worse answer.
//	with      a control_request{can_use_tool} lands 0.01s after the tool_use
//	          event, carrying tool_name, input, tool_use_id, decision_reason
//	          and permission_suggestions — and the CLI blocks until answered.
//
// So orbit always passes the flag: an approval orbit can see is a session it
// can hand back, and an approval it cannot see is a silently degraded run.
//
// No --permission-mode is passed by default. Whatever the user's own settings
// do when they run claude is what a dispatch does, which makes the handoff
// mean exactly "your claude would have prompted here" rather than a policy
// orbit invented. Their configured mode is echoed in system/init if you need
// to know which one was in force, and dispatch.claude_permission_mode
// overrides it for people who want an unattended run held to a stricter one.

func claudeArgv(r *Record, permissionMode string) []string {
	argv := []string{
		"claude", "-p",
		// stream-json in both directions is what opens the control channel;
		// --print alone has no way to receive an answer.
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose", // required by the CLI for stream-json output
		"--permission-prompt-tool", "stdio",
		"--session-id", r.SessionID,
	}
	if permissionMode != "" {
		argv = append(argv, "--permission-mode", permissionMode)
	}
	return argv
}

// claudeInput is the single user message that starts the turn, written to
// stdin. stdin then stays open, because that is where control responses go.
func claudeInput(prompt string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type":               "user",
		"message":            map[string]any{"role": "user", "content": prompt},
		"parent_tool_use_id": nil,
	})
	return append(b, '\n')
}

// claudeHandoff is the answer to a can_use_tool that stops the run.
//
// interrupt matters, and was measured. A plain deny lets the model carry on
// and talk its way around the refusal — it wrote four paragraphs explaining
// what it could not do. deny with interrupt ends the turn in the same tenth of
// a second, and `claude --resume` then opens on "Ran 1 shell command ⎿
// Interrupted · What should Claude do instead?" with a live cursor, which is
// the handoff working.
//
// The run exits 1 with subtype error_during_execution and terminal_reason
// aborted_streaming afterwards. That is the expected outcome of a handoff, not
// a failure, and the runner treats it as such.
func claudeHandoff(requestID string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response": map[string]any{
				"behavior":  "deny",
				"message":   "orbit stopped here so you can take this session over",
				"interrupt": true,
			},
		},
	})
	return append(b, '\n')
}

// claudeAck answers a control request orbit does not handle. Every control
// request blocks the CLI until something replies, so an unrecognised one has
// to be answered rather than ignored — silence would hang the agent.
func claudeAck(requestID string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   map[string]any{},
		},
	})
	return append(b, '\n')
}

type claudeEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	Request   *struct {
		Subtype  string         `json:"subtype"`
		ToolName string         `json:"tool_name"`
		Input    map[string]any `json:"input"`
		Reason   string         `json:"decision_reason"`
	} `json:"request"`
	Message *struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

func parseClaude(line []byte) (step, bool) {
	var e claudeEvent
	if json.Unmarshal(line, &e) != nil {
		return step{}, false
	}
	switch e.Type {
	case "control_request":
		if e.Request == nil {
			return step{}, false
		}
		if e.Request.Subtype != "can_use_tool" {
			// Answered, not acted on: see claudeAck.
			return step{ask: &ask{requestID: e.RequestID}}, true
		}
		detail := toolLabel(e.Request.ToolName, e.Request.Input)
		if e.Request.Reason != "" {
			detail += " — " + e.Request.Reason
		}
		return step{ask: &ask{
			requestID: e.RequestID,
			tool:      e.Request.ToolName,
			detail:    detail,
		}}, true

	case "system":
		if e.Subtype == "init" {
			return step{sessionID: e.SessionID, note: "session " + e.SessionID}, true
		}

	case "assistant":
		if e.Message == nil {
			return step{}, false
		}
		for _, b := range e.Message.Content {
			if b.Type == "tool_use" {
				return step{activity: toolLabel(b.Name, b.Input)}, true
			}
		}
		for _, b := range e.Message.Content {
			if b.Type == "text" && b.Text != "" {
				return step{note: oneLine(b.Text)}, true
			}
		}

	case "result":
		if e.IsError {
			// The message is the useful part; subtype alone says nothing a
			// person can act on.
			msg := e.Result
			if msg == "" {
				msg = e.Subtype
			}
			return step{err: oneLine(msg)}, true
		}
		return step{done: true, note: oneLine(e.Result)}, true
	}
	return step{}, false
}

package dispatch

import (
	"encoding/json"

	"github.com/sadrig91/orbit/internal/format"
)

// Copilot's dispatch stream, from `copilot -p … --output-format json`. Every
// name below was seen in a real run; nothing here is taken from the docs.
//
//	session.mcp_server_status_changed, mcp.tools.list_changed,
//	session.skills_loaded, session.tools_updated
//	user.message
//	assistant.turn_start            data.turnId
//	model.call_start
//	assistant.tool_call_delta       data.toolCallId, toolName, inputDelta
//	assistant.message_start, assistant.message_delta
//	assistant.message               data.content, data.phase
//	assistant.turn_end
//	session.usage_checkpoint
//	assistant.idle                  data.aborted, when it was cut short
//	result                          sessionId, exitCode, usage
//	abort                           data.reason, on a signal
//	session.info                    data.infoType, e.g. cancellation
//
// This is the richest of the three streams — worth saying, because orbit's
// README claims copilot's live states are coarser. That is true of its session
// database and the reverse of true of its CLI.
//
// Two things this cost to learn. sessionId appears **only** on the result
// event, not on every event as the docs suggest, which would leave a run
// unjoinable to a session until the moment it ended. Passing --session-id
// makes that moot: copilot honours it, so orbit knows the id before the
// process starts and the result event only confirms it.
//
// And copilot can hang. A `-p` run doing a shell tool call sat for four
// minutes and had to be signalled, emitting abort{user_initiated} and
// assistant.idle{aborted:true} on the way out. The runner's timeout is not
// theoretical tidiness; it is the only thing that ends that run.
//
// There is no approval channel, and no prospect of one: copilot's own help
// says --allow-all-tools is *required* for non-interactive mode. A dispatched
// copilot therefore runs whatever it decides to run, which is why orbit will
// not dispatch one until the setting saying so has been turned on by hand.

func copilotArgv(r *Record, allowAllTools bool) []string {
	argv := []string{
		"copilot",
		"-p", r.Prompt,
		"--output-format", "json",
		"--session-id", r.SessionID,
		"--no-color",
	}
	if allowAllTools {
		argv = append(argv, "--allow-all-tools")
	}
	return argv
}

type copilotEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	ExitCode  *int   `json:"exitCode"`
	Data      *struct {
		ToolCallID string `json:"toolCallId"`
		ToolName   string `json:"toolName"`
		Content    string `json:"content"`
		Phase      string `json:"phase"`
		Reason     string `json:"reason"`
		Message    string `json:"message"`
		InfoType   string `json:"infoType"`
		Aborted    bool   `json:"aborted"`
	} `json:"data"`
}

// newCopilotParser returns a parser that has seen no tool calls yet.
//
// Copilot has no "a tool call started" event: it streams the call's arguments
// as assistant.tool_call_delta, one event per fragment — fifteen of them for a
// single `echo` in a measured run, each carrying the same toolCallId and
// toolName. Reporting every one would rewrite the record fifteen times to say
// the same thing, so the parser reports the first delta of each call and
// swallows the rest. The arguments themselves are not reassembled: they arrive
// as raw JSON fragments ("{\"", "command", "\":\"", "echo", " OR", "BIT"),
// and half a shell command is worse in the dashboard than none.
func newCopilotParser() parser {
	lastCall := ""
	return func(line []byte) (step, bool) {
		var e copilotEvent
		if json.Unmarshal(line, &e) != nil {
			return step{}, false
		}
		switch e.Type {
		case "assistant.tool_call_delta":
			if e.Data == nil || e.Data.ToolCallID == lastCall {
				return step{}, false
			}
			lastCall = e.Data.ToolCallID
			name := e.Data.ToolName
			if name == "" {
				name = "tool"
			}
			return step{activity: name}, true

		case "assistant.message":
			if e.Data != nil && e.Data.Content != "" {
				return step{note: oneLine(e.Data.Content)}, true
			}

		case "abort":
			reason := "aborted"
			if e.Data != nil && e.Data.Reason != "" {
				reason = "aborted: " + e.Data.Reason
			}
			return step{err: reason}, true

		case "result":
			// The turn is over either way; the exit code is what separates a
			// finished run from a broken one.
			if e.ExitCode != nil && *e.ExitCode != 0 {
				return step{sessionID: e.SessionID, err: "copilot exited " + format.Itoa(*e.ExitCode)}, true
			}
			return step{sessionID: e.SessionID, done: true}, true
		}
		return step{}, false
	}
}

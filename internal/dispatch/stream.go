package dispatch

import "strings"

// step is what one line of an agent's event stream means to orbit. Every
// parser reduces its CLI's schema to this, so the runner has one shape to act
// on and each stream's quirks stay in the file that knows about them.
type step struct {
	sessionID string // the line revealed the agent's own id for the conversation
	activity  string // a human-readable "doing this now"
	note      string // narration for the pane and the log, when it isn't activity
	ask       *ask   // blocked on an approval — claude only, see claude.go
	done      bool   // the turn ended normally
	err       string // the run failed, and this is why
}

// ask is an approval the agent is parked on. requestID is what the answer has
// to quote; tool and detail are for the person deciding.
type ask struct {
	requestID string
	tool      string
	detail    string
}

// parser turns one line of a CLI's stream into a step. Reporting false means
// the line carried nothing worth acting on, which is most of them.
type parser func(line []byte) (step, bool)

// parserFor returns a fresh parser for one run. Fresh, because copilot's
// streams its tool calls a character at a time and the parser has to remember
// which call it is already reporting — see copilot.go.
func parserFor(agent string) parser {
	switch agent {
	case "codex":
		return parseCodex
	case "copilot":
		return newCopilotParser()
	}
	return parseClaude
}

// toolLabel renders a tool call as one short line: the name, plus whichever
// argument says what it is actually about. Tools disagree on what that
// argument is called, so the order is the order of usefulness — a command
// beats a path beats a description — and an unrecognised tool degrades to its
// name alone rather than to a dump of its input.
func toolLabel(name string, input map[string]any) string {
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "prompt", "description"} {
		if v, ok := input[k].(string); ok && strings.TrimSpace(v) != "" {
			return name + ": " + oneLine(v)
		}
	}
	return name
}

// oneLine flattens a value for a single-line label. Commands are routinely
// multi-line heredocs and the label has one line to work with.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

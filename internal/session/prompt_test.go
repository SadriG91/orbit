package session

import "testing"

// Verbatim from a real Claude Code pane, which is the only reason to trust
// that the matcher recognises the thing it was written for.
const claudePrompt = `  Tool use

    context7 - query-docs(libraryId: "/anthropics/anthropic-sdk-typescript", query: "configuring maxRetries")
    Retrieves and queries up-to-date documentation and code examples from Context7.

  Do you want to proceed?
  ❯ 1. Yes
    2. Yes, and don't ask again for context7 - query-docs commands in /Users/utu417
    3. No

  Esc to cancel · Tab to amend`

func TestLooksLikeApprovalPrompt(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want bool
	}{
		{"claude permission prompt", claudePrompt, true},
		{"bare y/n", "Run `rm -rf build`?\n  [y/n]", true},
		{"lettered choices", "Allow this command?\n  y) yes\n  n) no", true},
		{"arrow marker", "Proceed with the edit?\n▸ 1) Yes\n  2) No", true},

		// A question with nothing to choose is prose, not a prompt. This is the
		// case that matters: agents ask rhetorical questions constantly.
		{"rhetorical question in prose", "So what does that mean?\nIt means the retry count is wrong.", false},
		{"question then more prose", "Should we cache it?\nProbably, but not yet — the index is cheap.", false},
		{"plain output", "running tests...\nok  github.com/x/y  1.2s", false},
		{"empty", "", false},

		// The choices have to come after the question, not before it.
		{"choices above an unrelated question", "1. first\n2. second\nWhat do you think?", false},
	}
	for _, tt := range tests {
		if got := LooksLikeApprovalPrompt(tt.pane); got != tt.want {
			t.Errorf("%s: LooksLikeApprovalPrompt = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// Only a session that is already ambiguous may be promoted. Everything else
// keeps the state the transcript gave it.
func TestPromoteToApprovalOnlyTouchesWorking(t *testing.T) {
	for _, st := range []State{Dormant, ShellOnly, YourTurn, NeedsApproval} {
		s := &Session{State: st}
		s.PromoteToApproval()
		if s.State != st {
			t.Errorf("state %v was promoted to %v", st, s.State)
		}
	}
	s := &Session{State: Working}
	s.PromoteToApproval()
	if s.State != NeedsApproval {
		t.Errorf("Working was not promoted, got %v", s.State)
	}
}

// The pane is only consulted for a session whose transcript ends on an
// unanswered tool call — the shortcut must not invent a state from screen
// text alone.
func TestAwaitingToolTracksTheHint(t *testing.T) {
	for hint, want := range map[Hint]bool{
		HintBusy: false, HintDone: false, HintApproval: false, HintMaybeApproval: true,
	} {
		s := &Session{hint: hint}
		if got := s.AwaitingTool(); got != want {
			t.Errorf("hint %v: AwaitingTool = %v, want %v", hint, got, want)
		}
	}
}

// A prompt buried under a wall of output still has to be found, since that is
// exactly what a long tool run leaves on screen.
func TestPromptFoundUnderHeavyOutput(t *testing.T) {
	noise := ""
	for i := 0; i < 500; i++ {
		noise += "line of build output that scrolled past\n"
	}
	if !LooksLikeApprovalPrompt(noise + claudePrompt) {
		t.Error("the prompt was missed under heavy preceding output")
	}
	// …but a prompt that has itself scrolled away must not linger.
	if LooksLikeApprovalPrompt(claudePrompt + "\n" + noise) {
		t.Error("a prompt scrolled far off the bottom still matched")
	}
}

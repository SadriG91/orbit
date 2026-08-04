package config

import (
	"strings"
	"testing"

	"github.com/sadrig91/orbit/internal/session"
)

// The embedded default config is the single source of truth for defaults, so
// a typo in it would silently degrade every install.
func TestDefaultConfigIsUsable(t *testing.T) {
	cfg, err := LoadDefaults()
	if err != nil {
		t.Fatalf("shipped toml does not parse: %v", err)
	}
	if cfg.RecentDays <= 0 {
		t.Error("recent_days must be positive or nothing shows")
	}
	if cfg.SpawnDelayDur() <= 0 || cfg.TabDelayDur() <= 0 {
		t.Error("delays must parse as durations")
	}
	for _, a := range []string{"claude", "codex", "copilot"} {
		argv := cfg.Summary.For(a)
		if len(argv) == 0 {
			t.Errorf("%s: no summary command configured", a)
			continue
		}
		// The command must be the agent's own CLI, or summaries bill the wrong
		// provider — and must not be a shell string, which wouldn't exec.
		if !strings.Contains(argv[0], a) {
			t.Errorf("%s: summary command is %q", a, argv[0])
		}
	}
	if session.ParseSortMode(cfg.Sort) != session.SortAge {
		t.Errorf("default sort should be age, got %v", session.ParseSortMode(cfg.Sort))
	}
}

// Cheap models have the smallest context windows, so the excerpt budget is the
// setting that keeps a huge session from overflowing one.
func TestSummaryInputBudgetIsPerProvider(t *testing.T) {
	cfg, _ := LoadDefaults()
	for _, a := range []string{"claude", "codex", "copilot"} {
		if got := cfg.Summary.InputBudget(a); got < 1000 || got > 200_000 {
			t.Errorf("%s: budget %d is not a sane default", a, got)
		}
	}
	cfg.Summary.Codex.MaxInputChars = 4000
	if got := cfg.Summary.InputBudget("codex"); got != 4000 {
		t.Errorf("per-provider override ignored: got %d", got)
	}
	if cfg.Summary.InputBudget("claude") == 4000 {
		t.Error("a codex override must not affect claude")
	}
	var empty Summary
	if got := empty.InputBudget("claude"); got <= 0 {
		t.Error("an unset budget must still resolve to something usable")
	}
}

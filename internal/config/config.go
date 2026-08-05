package config

import (
	_ "embed"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/sadrig91/orbit/internal/format"
)

//go:embed config.toml
var defaultConfigTOML string

type Config struct {
	Icons      string  `toml:"icons"`
	Attach     string  `toml:"attach"`
	Notify     bool    `toml:"notify"`
	RecentDays int     `toml:"recent_days"`
	Sort       string  `toml:"sort"`
	Group      bool    `toml:"group"`
	SpawnDelay string  `toml:"spawn_delay"`
	TabDelay   string  `toml:"tab_delay"`
	Summary    Summary `toml:"summary"`
	Update     Update  `toml:"update"`
}

type Update struct {
	Auto bool `toml:"auto"`
}

// AutoUpdate lets the environment turn updating off for a single run, and
// gives packagers a way to disable it for a build they manage themselves.
func (c Config) AutoUpdate() bool {
	if os.Getenv("ORBIT_NO_UPDATE") != "" {
		return false
	}
	return c.Update.Auto
}

type Summary struct {
	Enabled       bool     `toml:"enabled"`
	Auto          bool     `toml:"auto"`
	MaxChars      int      `toml:"max_chars"`
	MaxInputChars int      `toml:"max_input_chars"`
	AutoMinNew    int      `toml:"auto_min_new_messages"`
	Claude        Provider `toml:"claude"`
	Codex         Provider `toml:"codex"`
	Copilot       Provider `toml:"copilot"`
}

type Provider struct {
	Command       []string `toml:"command"`
	MaxInputChars int      `toml:"max_input_chars"`
	AutoMinNew    int      `toml:"auto_min_new_messages"`
}

// Providers are addressed by name rather than by an Agent type: config sits at
// the bottom of the dependency graph and shouldn't know what an agent is.
func (s Summary) provider(agent string) Provider {
	switch agent {
	case "codex":
		return s.Codex
	case "copilot":
		return s.Copilot
	}
	return s.Claude
}

func (s Summary) For(agent string) []string { return s.provider(agent).Command }

// InputBudget is how much transcript may be sent. Summarising deliberately runs
// on cheap models, whose context windows are the smallest, so this is the
// setting that keeps a huge session from blowing the window — per provider,
// because one of them may be pointed at a larger model.
func (s Summary) InputBudget(agent string) int {
	if n := s.provider(agent).MaxInputChars; n > 0 {
		return n
	}
	if s.MaxInputChars > 0 {
		return s.MaxInputChars
	}
	return 12000
}

func Path() string { return format.Home(".config", "orbit", "config.toml") }

// LoadDefaults parses the embedded default config — the single source of
// truth for defaults, so the shipped file and the fallbacks can't drift apart.
func LoadDefaults() (Config, error) {
	var cfg Config
	_, err := toml.Decode(defaultConfigTOML, &cfg)
	return cfg, err
}

// Load reads the config file, writing the annotated default first if it
// doesn't exist yet. A malformed file is reported rather than silently ignored,
// but never fatal — orbit falls back to defaults and carries on.
func Load() (Config, error) {
	cfg, err := LoadDefaults()
	if err != nil {
		return cfg, err
	}

	path := Path()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// UserTemplate, not the embedded default: settings orbit owns are
		// withheld so they keep tracking releases. See managed.go.
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			os.WriteFile(path, []byte(UserTemplate()), 0o644)
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return cfg, err // caller surfaces this; defaults are already in place
	}
	return cfg, nil
}

// Icons returns the configured icon mode, letting the environment override the
// file so a single run can be changed without editing it. Resolving the string
// into a mode is the UI's job.
func (c Config) IconMode() string {
	if v := os.Getenv("ORBIT_ICONS"); v != "" {
		return v
	}
	return c.Icons
}

func (c Config) SpawnDelayDur() time.Duration { return parseDur(c.SpawnDelay, 900*time.Millisecond) }
func (c Config) TabDelayDur() time.Duration   { return parseDur(c.TabDelay, time.Second) }

func parseDur(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return def
}

package main

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
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

func (s Summary) provider(a Agent) Provider {
	switch a {
	case Codex:
		return s.Codex
	case Copilot:
		return s.Copilot
	}
	return s.Claude
}

func (s Summary) For(a Agent) []string { return s.provider(a).Command }

// InputBudget is how much transcript may be sent. Summarising deliberately runs
// on cheap models, whose context windows are the smallest, so this is the
// setting that keeps a huge session from blowing the window — per provider,
// because one of them may be pointed at a larger model.
func (s Summary) InputBudget(a Agent) int {
	if n := s.provider(a).MaxInputChars; n > 0 {
		return n
	}
	if s.MaxInputChars > 0 {
		return s.MaxInputChars
	}
	return 12000
}

func configPath() string { return home(".config", "orbit", "config.toml") }

// LoadConfigDefaults parses the embedded default config — the single source of
// truth for defaults, so the shipped file and the fallbacks can't drift apart.
func LoadConfigDefaults() (Config, error) {
	var cfg Config
	_, err := toml.Decode(defaultConfigTOML, &cfg)
	return cfg, err
}

// LoadConfig reads the config file, writing the annotated default first if it
// doesn't exist yet. A malformed file is reported rather than silently ignored,
// but never fatal — orbit falls back to defaults and carries on.
func LoadConfig() (Config, error) {
	cfg, err := LoadConfigDefaults()
	if err != nil {
		return cfg, err
	}

	path := configPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			os.WriteFile(path, []byte(defaultConfigTOML), 0o644)
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

// Env vars override the file so a single run can be changed without editing it.
func (c Config) iconMode() IconMode {
	mode := c.Icons
	if v := os.Getenv("ORBIT_ICONS"); v != "" {
		mode = v
	}
	if (mode == "logo" || mode == "auto") && LogosSupported() {
		return IconLogo
	}
	return IconText
}

func (c Config) attachMode() attachMode {
	switch c.Attach {
	case "tab":
		return attachTab
	case "window":
		return attachWindow
	case "inline":
		return attachInline
	}
	return attachSmart
}

func (c Config) sortMode() SortMode {
	for _, s := range AllSorts {
		if strings.EqualFold(s.String(), c.Sort) {
			return s
		}
	}
	return SortAge
}

func (c Config) spawnDelay() time.Duration { return parseDur(c.SpawnDelay, 900*time.Millisecond) }
func (c Config) tabDelay() time.Duration   { return parseDur(c.TabDelay, time.Second) }

func parseDur(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return def
}

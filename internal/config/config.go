// Package config loads and validates the broadcaster YAML configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type OneBot struct {
	BaseURL        string `yaml:"base_url"`
	AccessToken    string `yaml:"access_token"`
	GroupID        int64  `yaml:"group_id"`
	AdminQQ        int64  `yaml:"admin_qq"`
	SendIntervalMS int    `yaml:"send_interval_ms"`
}

type ESPN struct {
	League          string `yaml:"league"`
	PollIntervalSec int    `yaml:"poll_interval_sec"`
}

type LLM struct {
	BaseURL    string `yaml:"base_url"`
	Model      string `yaml:"model"`
	APIKey     string `yaml:"api_key"`
	TimeoutSec int    `yaml:"timeout_sec"`
}

type Schedule struct {
	Timezone    string `yaml:"timezone"`
	PreviewCron string `yaml:"preview_cron"`
	RecapCron   string `yaml:"recap_cron"`
}

// Events are per-type switches for live broadcasting.
type Events struct {
	Kickoff         bool `yaml:"kickoff"`
	Goal            bool `yaml:"goal"`
	YellowCard      bool `yaml:"yellow_card"`
	RedCard         bool `yaml:"red_card"`
	Halftime        bool `yaml:"halftime"`
	SecondHalfStart bool `yaml:"second_half_start"`
	Substitution    bool `yaml:"substitution"`
	PeriodChange    bool `yaml:"period_change"`
	ShootoutRound   bool `yaml:"shootout_round"`
	Fulltime        bool `yaml:"fulltime"`
}

type Log struct {
	Dir        string `yaml:"dir"`
	Level      string `yaml:"level"`
	MaxAgeDays int    `yaml:"max_age_days"`
}

type Config struct {
	OneBot   OneBot   `yaml:"onebot"`
	ESPN     ESPN     `yaml:"espn"`
	LLM      LLM      `yaml:"llm"`
	Schedule Schedule `yaml:"schedule"`
	Events   Events   `yaml:"events"`
	DataDir  string   `yaml:"data_dir"`
	Log      Log      `yaml:"log"`

	// Location is resolved from Schedule.Timezone during Load.
	Location *time.Location `yaml:"-"`
}

// defaults returns a Config pre-filled with default values; yaml.Unmarshal
// on top of it only overrides keys present in the file.
func defaults() *Config {
	return &Config{
		OneBot: OneBot{
			BaseURL:        "http://127.0.0.1:3000",
			SendIntervalMS: 1500,
		},
		ESPN: ESPN{
			League:          "fifa.world",
			PollIntervalSec: 20,
		},
		LLM: LLM{
			BaseURL:    "https://api.deepseek.com",
			Model:      "deepseek-v4-pro",
			TimeoutSec: 300,
		},
		Schedule: Schedule{
			Timezone:    "Asia/Shanghai",
			PreviewCron: "0 23 * * *",
			RecapCron:   "0 15 * * *",
		},
		Events: Events{
			Kickoff: true, Goal: true, YellowCard: true, RedCard: true,
			Halftime: true, SecondHalfStart: true, Substitution: true,
			PeriodChange: true, ShootoutRound: true, Fulltime: true,
		},
		DataDir: "./data",
		Log: Log{
			Dir:        "/var/log/worldcup",
			Level:      "info",
			MaxAgeDays: 30,
		},
	}
}

// Load reads the YAML file at path, applies defaults and environment
// overrides (LLM_API_KEY, ONEBOT_ACCESS_TOKEN), and validates the result.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := defaults()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if v := os.Getenv("LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("ONEBOT_ACCESS_TOKEN"); v != "" {
		cfg.OneBot.AccessToken = v
	}

	loc, err := time.LoadLocation(cfg.Schedule.Timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", cfg.Schedule.Timezone, err)
	}
	cfg.Location = loc

	if cfg.ESPN.PollIntervalSec < 5 {
		return nil, fmt.Errorf("espn.poll_interval_sec must be >= 5, got %d", cfg.ESPN.PollIntervalSec)
	}
	if cfg.OneBot.SendIntervalMS < 0 {
		return nil, fmt.Errorf("onebot.send_interval_ms must be >= 0")
	}
	return cfg, nil
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	p := writeTemp(t, "onebot:\n  group_id: 12345\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OneBot.GroupID != 12345 {
		t.Errorf("group_id = %d, want 12345", cfg.OneBot.GroupID)
	}
	if cfg.OneBot.BaseURL != "http://127.0.0.1:3000" {
		t.Errorf("default base_url = %q", cfg.OneBot.BaseURL)
	}
	if cfg.ESPN.PollIntervalSec != 20 {
		t.Errorf("default poll interval = %d", cfg.ESPN.PollIntervalSec)
	}
	if cfg.LLM.Model != "deepseek-v4-pro" {
		t.Errorf("default model = %q", cfg.LLM.Model)
	}
	if !cfg.Events.Goal || !cfg.Events.ShootoutRound {
		t.Error("event switches should default to true")
	}
	if cfg.Location == nil || cfg.Location.String() != "Asia/Shanghai" {
		t.Errorf("location = %v, want Asia/Shanghai", cfg.Location)
	}
	if cfg.Schedule.PreviewCron != "0 23 * * *" || cfg.Schedule.RecapCron != "0 15 * * *" {
		t.Errorf("cron defaults wrong: %+v", cfg.Schedule)
	}
}

func TestLoadOverridesAndEventOff(t *testing.T) {
	p := writeTemp(t, `
espn:
  poll_interval_sec: 30
events:
  substitution: false
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ESPN.PollIntervalSec != 30 {
		t.Errorf("poll interval = %d, want 30", cfg.ESPN.PollIntervalSec)
	}
	if cfg.Events.Substitution {
		t.Error("substitution should be off")
	}
	if !cfg.Events.Goal {
		t.Error("goal should remain default true when omitted")
	}
}

func TestLoadEnvOverride(t *testing.T) {
	t.Setenv("LLM_API_KEY", "sk-env-key")
	p := writeTemp(t, "llm:\n  api_key: file-key\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLM.APIKey != "sk-env-key" {
		t.Errorf("api key = %q, want env override", cfg.LLM.APIKey)
	}
}

func TestLoadInvalidTimezone(t *testing.T) {
	p := writeTemp(t, "schedule:\n  timezone: Mars/Olympus\n")
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for invalid timezone")
	}
}

func TestLoadTooFastPolling(t *testing.T) {
	p := writeTemp(t, "espn:\n  poll_interval_sec: 1\n")
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for poll interval < 5s")
	}
}

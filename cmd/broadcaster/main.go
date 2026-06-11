// Command broadcaster runs the 2026 World Cup QQ-group bot: nightly
// previews, afternoon recaps, and real-time match event broadcasting.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/natefinch/lumberjack.v2"

	"worldcup-broadcaster/internal/alert"
	"worldcup-broadcaster/internal/config"
	"worldcup-broadcaster/internal/digest"
	"worldcup-broadcaster/internal/espn"
	"worldcup-broadcaster/internal/llm"
	"worldcup-broadcaster/internal/onebot"
	"worldcup-broadcaster/internal/store"
	"worldcup-broadcaster/internal/watcher"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	runPreview := flag.Bool("run-preview-now", false, "send tomorrow's preview immediately and exit")
	runRecap := flag.Bool("run-recap-now", false, "send today's recap immediately and exit")
	dateOverride := flag.String("date", "", "China date (2006-01-02) override for manual runs")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	logger := setupLogger(cfg)
	logger.Info("worldcup broadcaster starting",
		"league", cfg.ESPN.League, "group", cfg.OneBot.GroupID,
		"poll_interval_sec", cfg.ESPN.PollIntervalSec)
	if cfg.OneBot.GroupID == 0 {
		logger.Warn("onebot.group_id is 0: messages will be logged, not sent")
	}
	if cfg.LLM.APIKey == "" {
		logger.Warn("llm api key missing: previews/recaps will degrade to data-only")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st := store.New(cfg.DataDir)
	espnClient := espn.NewClient(cfg.ESPN.League)
	bot := onebot.New(cfg.OneBot.BaseURL, cfg.OneBot.AccessToken, cfg.OneBot.GroupID,
		time.Duration(cfg.OneBot.SendIntervalMS)*time.Millisecond, logger)
	alerter := alert.New(bot, cfg.OneBot.AdminQQ, logger)
	bot.OnSendError = func(err error) { alerter.Alert("onebot", "QQ消息发送失败: "+err.Error()) }
	bot.Start(ctx)

	var llmClient digest.LLM
	if cfg.LLM.APIKey != "" {
		llmClient = llm.New(cfg.LLM.BaseURL, cfg.LLM.APIKey, cfg.LLM.Model,
			time.Duration(cfg.LLM.TimeoutSec)*time.Second)
	}
	dig := digest.New(espnClient, llmClient, st, bot, alerter.Alert, logger)
	w := watcher.New(espnClient, bot, st, alerter.Alert, logger,
		watcher.DefaultOptions(cfg.Events, time.Duration(cfg.ESPN.PollIntervalSec)*time.Second))

	loc := cfg.Location
	today := func() string { return time.Now().In(loc).Format("2006-01-02") }
	tomorrow := func() string { return time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02") }

	// One-shot manual modes (used for dry runs and catch-up sends).
	if *runPreview || *runRecap {
		if *runPreview {
			date := tomorrow()
			if *dateOverride != "" {
				date = *dateOverride
			}
			if err := dig.Preview(ctx, date); err != nil {
				logger.Error("manual preview failed", "error", err)
			}
		}
		if *runRecap {
			date := today()
			if *dateOverride != "" {
				date = *dateOverride
			}
			if err := dig.Recap(ctx, date); err != nil {
				logger.Error("manual recap failed", "error", err)
			}
		}
		bot.WaitIdle(ctx)
		return
	}

	sched := newMatchScheduler(w, espnClient, alerter, logger)

	c := cron.New(cron.WithLocation(loc), cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger)))
	mustAdd(c, cfg.Schedule.PreviewCron, func() {
		if err := dig.Preview(ctx, tomorrow()); err != nil {
			logger.Error("scheduled preview failed", "error", err)
		}
	})
	mustAdd(c, cfg.Schedule.RecapCron, func() {
		if err := dig.Recap(ctx, today()); err != nil {
			logger.Error("scheduled recap failed", "error", err)
		}
	})
	mustAdd(c, "@every 10m", func() { sched.armWatchers(ctx, today(), tomorrow()) })
	c.Start()
	go sched.armWatchers(ctx, today(), tomorrow()) // arm immediately on boot

	logger.Info("scheduler running",
		"preview_cron", cfg.Schedule.PreviewCron, "recap_cron", cfg.Schedule.RecapCron, "tz", loc.String())
	<-ctx.Done()
	logger.Info("shutting down")
	<-c.Stop().Done()
}

func mustAdd(c *cron.Cron, spec string, fn func()) {
	if _, err := c.AddFunc(spec, fn); err != nil {
		fmt.Fprintf(os.Stderr, "invalid cron spec %q: %v\n", spec, err)
		os.Exit(1)
	}
}

func setupLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
		level = slog.LevelInfo
	}
	var w io.Writer = os.Stderr
	if cfg.Log.Dir != "" {
		if err := os.MkdirAll(cfg.Log.Dir, 0o755); err == nil {
			w = io.MultiWriter(os.Stderr, &lumberjack.Logger{
				Filename:   filepath.Join(cfg.Log.Dir, "broadcaster.log"),
				MaxSize:    50, // MB per file
				MaxAge:     cfg.Log.MaxAgeDays,
				MaxBackups: 60,
				Compress:   true,
			})
		} else {
			fmt.Fprintln(os.Stderr, "cannot create log dir, logging to stderr only:", err)
		}
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// matchScheduler arms one watcher goroutine per upcoming/live match,
// idempotently across its 10-minute scans.
type matchScheduler struct {
	watcher *watcher.Watcher
	espn    *espn.Client
	alerter *alert.Alerter
	logger  *slog.Logger

	mu     sync.Mutex
	active map[string]bool
}

func newMatchScheduler(w *watcher.Watcher, c *espn.Client, a *alert.Alerter, logger *slog.Logger) *matchScheduler {
	return &matchScheduler{watcher: w, espn: c, alerter: a, logger: logger, active: make(map[string]bool)}
}

func (s *matchScheduler) armWatchers(ctx context.Context, dates ...string) {
	for _, date := range dates {
		events, err := s.espn.MatchesOnChinaDate(ctx, date)
		if err != nil {
			s.logger.Error("scheduler fetch failed", "date", date, "error", err)
			s.alerter.Alert("espn", fmt.Sprintf("赛程获取失败（%s）: %v", date, err))
			continue
		}
		for i := range events {
			s.maybeArm(ctx, events[i])
		}
	}
}

func (s *matchScheduler) maybeArm(ctx context.Context, ev espn.Event) {
	// Finished matches are never armed: a bot (re)started after the final
	// whistle must not spray the whole event history into the group.
	if ev.Status.Type.State == "post" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[ev.ID] {
		return
	}
	s.active[ev.ID] = true
	s.logger.Info("arming match watcher", "match", ev.ID, "name", ev.ShortName, "kickoff", ev.Date)
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.active, ev.ID)
			s.mu.Unlock()
		}()
		if err := s.watcher.Watch(ctx, ev); err != nil && ctx.Err() == nil {
			s.logger.Error("watcher exited with error", "match", ev.ID, "error", err)
		}
	}()
}

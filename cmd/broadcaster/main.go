// Command broadcaster runs the 2026 World Cup QQ-group bot: nightly
// previews, afternoon recaps, and real-time match event broadcasting.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
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
	"worldcup-broadcaster/internal/qa"
	"worldcup-broadcaster/internal/store"
	"worldcup-broadcaster/internal/watcher"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	runPreview := flag.Bool("run-preview-now", false, "send tomorrow's preview immediately and exit")
	runRecap := flag.Bool("run-recap-now", false, "send today's recap immediately and exit")
	dateOverride := flag.String("date", "", "China date (2006-01-02) override for manual runs")
	simFixture := flag.String("simulate-fixture", "", "replay a summary fixture's events to the group and exit (live-flow drill)")
	testAlert := flag.Bool("test-alert", false, "send a test alert to admin_qq and exit")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	logger := setupLogger(cfg)
	logger.Info("worldcup broadcaster starting",
		"league", cfg.ESPN.League, "groups", cfg.OneBot.GroupIDs,
		"poll_interval_sec", cfg.ESPN.PollIntervalSec)
	if len(cfg.OneBot.GroupIDs) == 0 {
		logger.Warn("no group configured: messages will be logged, not sent")
	}
	if cfg.LLM.APIKey == "" {
		logger.Warn("llm api key missing: previews/recaps will degrade to data-only")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st := store.New(cfg.DataDir)
	espnClient := espn.NewClient(cfg.ESPN.League)
	bot := onebot.New(cfg.OneBot.BaseURL, cfg.OneBot.AccessToken, cfg.OneBot.GroupIDs,
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

	// One-shot alert-path test: verifies admin private-message delivery.
	if *testAlert {
		alerter.Alert("test", "这是一条测试告警：如果你在QQ私聊里看到它，说明告警链路畅通 ✅")
		return
	}

	// One-shot live-broadcast drill: replay all events of a recorded match
	// through the real formatting/queue/NapCat chain.
	if *simFixture != "" {
		if err := simulateFixture(ctx, *simFixture, cfg, bot, logger); err != nil {
			logger.Error("simulation failed", "error", err)
			os.Exit(1)
		}
		bot.WaitIdle(ctx)
		return
	}

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

	if cfg.OneBot.ListenAddr != "" {
		var qaLLM qa.LLM
		if llmClient != nil {
			qaLLM = llmClient
		}
		qaHandler := qa.NewHandler(qa.Options{
			GroupIDs:       cfg.OneBot.GroupIDs,
			GroupNames:     cfg.QA.GroupNames,
			AdminQQ:        cfg.OneBot.AdminQQ,
			HistorySize:    cfg.QA.HistorySize,
			EngageProb:     cfg.QA.EngageProbability,
			EngageCooldown: time.Duration(cfg.QA.EngageCooldownMin) * time.Minute,
			FollowupWindow: time.Duration(cfg.QA.FollowupWindowSec) * time.Second,
			SeedPersonas:   cfg.QA.SeedPersonas,
		}, bot, qaLLM, dig, espnClient, st, logger)
		qaHandler.SetMemberLister(bot)
		qaHandler.Start(ctx)
		go func() {
			if err := qa.StartServer(ctx, cfg.OneBot.ListenAddr, qaHandler, logger); err != nil {
				logger.Error("qa server failed", "error", err)
				alerter.Alert("qa", "群命令监听服务启动失败: "+err.Error())
			}
		}()
	}

	if cfg.OneBot.KeepaliveIntervalMin > 0 {
		go qqKeepalive(ctx, cfg, bot, alerter, logger)
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

// qqKeepalive probes NapCat's online status and self-heals: on failure it
// alerts the admin (best-effort) and runs the restart command, at most once
// per 20 minutes.
func qqKeepalive(ctx context.Context, cfg *config.Config, bot *onebot.Client, alerter *alert.Alerter, logger *slog.Logger) {
	interval := time.Duration(cfg.OneBot.KeepaliveIntervalMin) * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastRestart time.Time
	consecutive := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		online, err := bot.GetStatus()
		if err == nil && online {
			if consecutive > 0 {
				logger.Info("qq back online", "after_failures", consecutive)
			}
			consecutive = 0
			continue
		}
		consecutive++
		logger.Error("qq offline or napcat unreachable", "error", err, "online", online, "consecutive", consecutive)
		if consecutive < 2 {
			continue // a single blip (e.g. napcat restarting) is not an outage
		}
		if napcatAlive(cfg.OneBot.WebUIURL) {
			// NapCat itself is up but QQ is not logged in. A container
			// restart cannot fix an invalidated session (quick login already
			// failed at boot) and would kill any QR code mid-scan, so leave
			// it running and page the admin instead.
			logger.Warn("napcat alive but qq offline; awaiting manual login, skipping restart", "consecutive", consecutive)
			alerter.Alert("qq-login", fmt.Sprintf("QQ不在线但NapCat存活（连续%d次探测失败）：疑似登录态失效，需要人工扫码重新登录，已暂停自动重启以保护二维码", consecutive))
			continue
		}
		alerter.Alert("qq-online", fmt.Sprintf("QQ疑似掉线（连续%d次探测失败，err=%v），尝试自动重启 NapCat", consecutive, err))
		if cfg.OneBot.RestartCmd != "" && time.Since(lastRestart) > 20*time.Minute {
			lastRestart = time.Now()
			logger.Warn("executing restart command", "cmd", cfg.OneBot.RestartCmd)
			out, cmdErr := exec.CommandContext(ctx, "sh", "-c", cfg.OneBot.RestartCmd).CombinedOutput()
			if cmdErr != nil {
				logger.Error("restart command failed", "error", cmdErr, "output", string(out))
			} else {
				logger.Info("restart command executed", "output", string(out))
			}
		}
	}
}

// napcatAlive reports whether NapCat's WebUI answers HTTP at all — proof the
// process is running even when the QQ account is logged out.
func napcatAlive(webuiURL string) bool {
	if webuiURL == "" {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(webuiURL)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// simulateFixture replays every event of a recorded match summary through
// the real render + queue + OneBot chain, for verifying the live pipeline
// end-to-end against the actual QQ group.
func simulateFixture(ctx context.Context, path string, cfg *config.Config, bot *onebot.Client, logger *slog.Logger) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var sum espn.Summary
	if err := json.Unmarshal(raw, &sum); err != nil {
		return fmt.Errorf("parse fixture: %w", err)
	}
	events := watcher.Diff(&sum, func(string) bool { return false })
	if len(cfg.OneBot.GroupIDs) == 0 {
		return fmt.Errorf("no group configured")
	}
	// Drills only ever hit the first configured group (the test group).
	gid := cfg.OneBot.GroupIDs[0]
	logger.Info("simulation starting", "fixture", path, "events", len(events), "group", gid)
	bot.EnqueueGroupTo(gid, "🧪 [演习] 实时播报链路测试开始：重放2022世界杯决赛全场事件流")
	sent := 0
	for _, e := range events {
		if watcher.Enabled(e.Type, cfg.Events) {
			bot.EnqueueGroupTo(gid, watcher.Render(e))
			sent++
		}
	}
	bot.EnqueueGroupTo(gid, fmt.Sprintf("🧪 [演习] 重放完毕，共 %d 条事件消息。实战今晚见！", sent))
	logger.Info("simulation enqueued", "messages", sent)
	return nil
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

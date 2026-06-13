// Package chatops turns DingTalk group commands into Claude Code runs against
// this repository. An admin allowlist gates every mutating command, and all
// work happens in an isolated git worktree on a dedicated branch so the live
// service's own source tree is never edited and master is never touched
// directly — code only leaves the branch through a pull request.
package chatops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"worldcup-broadcaster/internal/config"
	"worldcup-broadcaster/internal/dingbot"
)

// execFunc runs a command in dir and returns combined output. Injectable for
// tests; the real implementation shells out.
type execFunc func(ctx context.Context, dir, name string, args ...string) (string, error)

func realExec(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Handler dispatches DingTalk commands.
type Handler struct {
	admins      map[string]bool
	repoDir     string
	worktreeDir string
	branch      string
	maxRun      time.Duration
	runner      Runner
	exec        execFunc
	logger      *slog.Logger

	mu sync.Mutex // serializes jobs: one code change at a time
}

// New builds a Handler from config. repo_dir defaults to the current working
// directory; worktree_dir defaults to a sibling "<repo>-chatops".
func New(cfg config.DingBot, logger *slog.Logger) (*Handler, error) {
	repo := cfg.RepoDir
	if repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		repo = wd
	}
	repo, _ = filepath.Abs(repo)
	wt := cfg.WorktreeDir
	if wt == "" {
		wt = repo + "-chatops"
	}
	wt, _ = filepath.Abs(wt)
	admins := make(map[string]bool, len(cfg.AdminStaffIDs))
	for _, a := range cfg.AdminStaffIDs {
		admins[a] = true
	}
	maxRun := time.Duration(cfg.MaxRunMin) * time.Minute
	if maxRun <= 0 {
		maxRun = 20 * time.Minute
	}
	return &Handler{
		admins:      admins,
		repoDir:     repo,
		worktreeDir: wt,
		branch:      "chatops/work",
		maxRun:      maxRun,
		runner: &claudeRunner{
			bin:          orDefault(cfg.ClaudeBin, "claude"),
			model:        cfg.ClaudeModel,
			permMode:     orDefault(cfg.PermissionMode, "acceptEdits"),
			allowedTools: cfg.AllowedTools,
			dir:          wt,
			logger:       logger,
		},
		exec:   realExec,
		logger: logger,
	}, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

const helpText = `🤖 世界杯Bot ChatOps —— 在群里指挥 Claude 改代码
（改动类命令仅管理员可用，全部在隔离分支 chatops/work 进行，不碰 master）

• 做 <需求>  / claude <需求> — 让 Claude 改代码并自测（自动编辑）
• 问 <问题>  / ask <问题>   — 只读分析，不改文件
• 测试 / test               — 在工作区跑 go test ./...
• 状态 / status             — 看工作区 git 状态
• diff                      — 看改动统计
• pr                        — 提交并推分支、开 PR
• 帮助 / help               — 本帮助`

// Handle is the dingbot.Handler entrypoint.
func (h *Handler) Handle(ctx context.Context, msg dingbot.InboundMsg, reply dingbot.Replier) {
	cmd, arg := parseCommand(msg.Text)
	if cmd == "" {
		return // not a recognized command: stay silent
	}
	if cmd == "help" {
		reply(helpText)
		return
	}
	if !h.admins[msg.SenderStaffID] {
		h.logger.Warn("chatops command from non-admin", "staff", msg.SenderStaffID, "cmd", cmd)
		reply("⛔ 只有管理员能用 ChatOps 命令（联系管理员把你的 staffId 加进 admin_staff_ids）")
		return
	}
	h.logger.Info("chatops command", "cmd", cmd, "from", msg.SenderNick, "staff", msg.SenderStaffID)

	switch cmd {
	case "status":
		reply(h.gitText(ctx, "📋 工作区状态", "git", "status", "--short", "--branch"))
	case "diff":
		reply(h.gitText(ctx, "📝 改动统计", "git", "diff", "--stat"))
	case "test":
		h.runJob(ctx, reply, func(jctx context.Context) string {
			out, err := h.exec(jctx, h.worktreeDir, "go", "test", "./...")
			status := "✅ 测试通过"
			if err != nil {
				status = "❌ 测试失败"
			}
			return status + "\n\n" + tail(out, 1500)
		})
	case "do", "ask":
		if arg == "" {
			reply("❓ 用法：做 <需求> / 问 <问题>")
			return
		}
		edit := cmd == "do"
		h.runJob(ctx, reply, func(jctx context.Context) string {
			if edit {
				if err := h.ensureWorktree(jctx); err != nil {
					return "❌ 准备工作区失败：" + err.Error()
				}
			}
			res := h.runner.Run(jctx, arg, edit)
			out := res.summary()
			if edit {
				if diff, err := h.exec(jctx, h.worktreeDir, "git", "diff", "--stat"); err == nil && strings.TrimSpace(diff) != "" {
					out += "\n\n📝 改动：\n" + tail(diff, 800) + "\n分支 " + h.branch + "，用 `pr` 提 PR"
				}
			}
			return out
		})
	case "pr":
		h.runJob(ctx, reply, h.openPR)
	}
}

// runJob serializes work, acks immediately, runs fn under the max-run timeout,
// and posts the result. One job runs at a time so concurrent commands queue.
func (h *Handler) runJob(ctx context.Context, reply dingbot.Replier, fn func(context.Context) string) {
	reply("🤖 收到，开始干活…（完成后回报，最长 " + h.maxRun.String() + "）")
	h.mu.Lock()
	defer h.mu.Unlock()
	jctx, cancel := context.WithTimeout(ctx, h.maxRun)
	defer cancel()
	reply(fn(jctx))
}

// ensureWorktree creates the isolated worktree + branch on first use, reusing
// it on later commands.
func (h *Handler) ensureWorktree(ctx context.Context) error {
	// A linked worktree marks itself with a .git file (not a dir); its mere
	// presence means the worktree is already set up, so reuse it.
	if _, err := os.Stat(filepath.Join(h.worktreeDir, ".git")); err == nil {
		return nil
	}
	out, err := h.exec(ctx, h.repoDir, "git", "worktree", "add", "-B", h.branch, h.worktreeDir)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	h.logger.Info("chatops worktree created", "dir", h.worktreeDir, "branch", h.branch)
	return nil
}

func (h *Handler) gitText(ctx context.Context, title string, args ...string) string {
	out, err := h.exec(ctx, h.worktreeDir, args[0], args[1:]...)
	if err != nil {
		return title + "\n（工作区还没创建？先用「做」发一个需求）\n" + strings.TrimSpace(out)
	}
	if strings.TrimSpace(out) == "" {
		out = "（无改动）"
	}
	return title + "\n" + tail(out, 1500)
}

// openPR commits any pending changes, pushes the branch, and opens a PR via gh.
func (h *Handler) openPR(ctx context.Context) string {
	if _, err := os.Stat(h.worktreeDir); err != nil {
		return "❌ 还没有工作区，先用「做」发一个需求"
	}
	_, _ = h.exec(ctx, h.worktreeDir, "git", "add", "-A")
	// commit may fail if nothing to commit — that's fine, continue to push.
	commitOut, _ := h.exec(ctx, h.worktreeDir, "git", "commit", "-m", "chatops: changes from DingTalk")
	if out, err := h.exec(ctx, h.worktreeDir, "git", "push", "-u", "origin", h.branch); err != nil {
		return "❌ 推送失败：\n" + tail(out, 800)
	}
	prOut, err := h.exec(ctx, h.worktreeDir, "gh", "pr", "create", "--fill", "--head", h.branch)
	if err != nil {
		return "✅ 已推送分支 " + h.branch + "，但开 PR 失败（gh 未配置？）：\n" + tail(prOut, 600) + "\n" + tail(commitOut, 200)
	}
	return "✅ 已开 PR：\n" + strings.TrimSpace(prOut)
}

// tail returns the last n bytes of s with an elision marker when truncated.
func tail(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…(截断)\n" + string(r[len(r)-n:])
}

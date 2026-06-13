package chatops

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
)

// RunResult is the outcome of one Claude Code headless run.
type RunResult struct {
	Text     string // final result text the agent produced
	IsError  bool   // the run ended in an error state
	Subtype  string // result subtype (success / error_max_turns / ...)
	NumTurns int
}

// Runner executes a Claude Code prompt against the worktree. Injectable so the
// command handler can be tested without spawning the real CLI.
type Runner interface {
	Run(ctx context.Context, prompt string, edit bool) RunResult
}

// claudeRunner shells out to the `claude` CLI in headless stream-json mode.
type claudeRunner struct {
	bin          string
	model        string
	permMode     string
	allowedTools []string
	dir          string
	logger       *slog.Logger
}

// buildArgs assembles the headless CLI flags. Read-only ("ask") runs use plan
// mode so the agent never edits; editing runs use the configured permission
// mode (acceptEdits by default).
func (r *claudeRunner) buildArgs(prompt string, edit bool) []string {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
	if r.model != "" {
		args = append(args, "--model", r.model)
	}
	mode := r.permMode
	if mode == "" {
		mode = "acceptEdits"
	}
	if !edit {
		mode = "plan"
	}
	args = append(args, "--permission-mode", mode)
	if len(r.allowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(r.allowedTools, ","))
	}
	return args
}

func (r *claudeRunner) Run(ctx context.Context, prompt string, edit bool) RunResult {
	args := r.buildArgs(prompt, edit)
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Dir = r.dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{IsError: true, Text: "无法启动 claude: " + err.Error()}
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return RunResult{IsError: true, Text: "启动 claude 失败: " + err.Error()}
	}
	res := parseStreamJSON(stdout)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return RunResult{IsError: true, Text: "⏱️ 运行超时，已中止"}
		}
		// a non-zero exit with no parsed result is itself the failure
		if res.Text == "" {
			res.IsError = true
			res.Text = "claude 退出异常: " + err.Error()
		}
	}
	return res
}

// streamEvent is the subset of Claude Code stream-json events we read.
type streamEvent struct {
	Type     string          `json:"type"`
	Subtype  string          `json:"subtype"`
	IsError  bool            `json:"is_error"`
	Result   string          `json:"result"`
	NumTurns int             `json:"num_turns"`
	Message  json.RawMessage `json:"message"`
}

// parseStreamJSON reads newline-delimited stream-json and returns the final
// result. If no terminal result event appears, it falls back to the last
// assistant text block.
func parseStreamJSON(r io.Reader) RunResult {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // tool outputs can be large
	var res RunResult
	var lastAssistant string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var ev streamEvent
		if json.Unmarshal([]byte(line), &ev); ev.Type == "" {
			continue
		}
		switch ev.Type {
		case "result":
			res.Text = strings.TrimSpace(ev.Result)
			res.IsError = ev.IsError
			res.Subtype = ev.Subtype
			res.NumTurns = ev.NumTurns
		case "assistant":
			if t := assistantText(ev.Message); t != "" {
				lastAssistant = t
			}
		}
	}
	if res.Text == "" {
		res.Text = lastAssistant
	}
	return res
}

// assistantText extracts concatenated text blocks from an assistant message.
func assistantText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// summary renders a RunResult for posting back to DingTalk.
func (res RunResult) summary() string {
	head := fmt.Sprintf("🤖 干完了（%d 轮）", res.NumTurns)
	if res.IsError {
		head = fmt.Sprintf("⚠️ 运行结束但有问题（%s）", res.Subtype)
	}
	body := res.Text
	if body == "" {
		body = "（没有产生文本输出）"
	}
	return head + "\n\n" + body
}

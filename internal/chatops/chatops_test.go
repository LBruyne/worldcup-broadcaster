package chatops

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"worldcup-broadcaster/internal/dingbot"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in, cmd, arg string
	}{
		{"做 给 README 加一行注释", "do", "给 README 加一行注释"},
		{"做给README加注释", "do", "给README加注释"}, // no space
		{"问 这个函数干嘛的", "ask", "这个函数干嘛的"},
		{"claude refactor the parser", "do", "refactor the parser"},
		{"/claude add a test", "do", "add a test"},
		{"ask what does diff do", "ask", "what does diff do"},
		{"测试", "test", ""},
		{"test", "test", ""},
		{"状态", "status", ""},
		{"/status", "status", ""},
		{"diff", "diff", ""},
		{"pr", "pr", ""},
		{"帮助", "help", ""},
		{"help", "help", ""},
		{"今天天气不错", "", ""},   // ordinary chatter → silent
		{"随便聊聊球", "", ""},     // not a command
		{"", "", ""},
	}
	for _, c := range cases {
		cmd, arg := parseCommand(c.in)
		if cmd != c.cmd || arg != c.arg {
			t.Errorf("parseCommand(%q) = (%q,%q), want (%q,%q)", c.in, cmd, arg, c.cmd, c.arg)
		}
	}
}

func TestBuildArgs(t *testing.T) {
	r := &claudeRunner{bin: "claude", model: "deepseek-v4-pro", permMode: "acceptEdits",
		allowedTools: []string{"Bash(go test:*)", "Read"}}

	edit := strings.Join(r.buildArgs("fix the bug", true), " ")
	for _, want := range []string{"-p fix the bug", "--output-format stream-json", "--verbose",
		"--model deepseek-v4-pro", "--permission-mode acceptEdits", "--allowedTools Bash(go test:*),Read"} {
		if !strings.Contains(edit, want) {
			t.Errorf("edit args missing %q: %s", want, edit)
		}
	}
	// read-only (ask) must force plan mode regardless of permMode
	ask := strings.Join(r.buildArgs("explain", false), " ")
	if !strings.Contains(ask, "--permission-mode plan") {
		t.Errorf("ask must use plan mode: %s", ask)
	}
	if strings.Contains(ask, "acceptEdits") {
		t.Errorf("ask must not allow edits: %s", ask)
	}
}

func TestParseStreamJSON(t *testing.T) {
	in := `{"type":"system","subtype":"init","session_id":"s1"}
{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"加好了注释","num_turns":3}`
	res := parseStreamJSON(strings.NewReader(in))
	if res.Text != "加好了注释" || res.NumTurns != 3 || res.IsError {
		t.Errorf("result = %+v", res)
	}

	// no terminal result → fall back to last assistant text
	fallback := parseStreamJSON(strings.NewReader(
		`{"type":"assistant","message":{"content":[{"type":"text","text":"半截"}]}}`))
	if fallback.Text != "半截" {
		t.Errorf("fallback = %+v", fallback)
	}

	// error result surfaces the flag + subtype
	errRes := parseStreamJSON(strings.NewReader(
		`{"type":"result","subtype":"error_max_turns","is_error":true,"result":""}`))
	if !errRes.IsError || errRes.Subtype != "error_max_turns" {
		t.Errorf("errRes = %+v", errRes)
	}
}

// --- handler dispatch with fakes ---

type fakeRunner struct {
	gotPrompt string
	gotEdit   bool
	result    RunResult
}

func (f *fakeRunner) Run(_ context.Context, prompt string, edit bool) RunResult {
	f.gotPrompt, f.gotEdit = prompt, edit
	return f.result
}

func testHandler(runner Runner, exec execFunc, admins ...string) *Handler {
	m := map[string]bool{}
	for _, a := range admins {
		m[a] = true
	}
	return &Handler{
		admins: m, repoDir: "/tmp/repo", worktreeDir: "/tmp/repo-chatops-doesnotexist",
		branch: "chatops/work", maxRun: time.Minute, runner: runner, exec: exec,
		logger: discardLogger(),
	}
}

func collectReplies(replies *[]string) dingbot.Replier {
	return func(s string) { *replies = append(*replies, s) }
}

func TestAdminGate(t *testing.T) {
	fr := &fakeRunner{result: RunResult{Text: "done"}}
	h := testHandler(fr, func(_ context.Context, _, _ string, _ ...string) (string, error) { return "", nil }, "boss")

	// non-admin mutating command → refused, runner not called
	var r1 []string
	h.Handle(context.Background(), dingbot.InboundMsg{Text: "做 删库跑路", SenderStaffID: "rando"}, collectReplies(&r1))
	if fr.gotPrompt != "" {
		t.Error("runner must not run for non-admin")
	}
	if len(r1) != 1 || !strings.Contains(r1[0], "只有管理员") {
		t.Errorf("non-admin reply = %v", r1)
	}

	// help is open to everyone
	var r2 []string
	h.Handle(context.Background(), dingbot.InboundMsg{Text: "help", SenderStaffID: "rando"}, collectReplies(&r2))
	if len(r2) != 1 || !strings.Contains(r2[0], "ChatOps") {
		t.Errorf("help reply = %v", r2)
	}
}

func TestDispatchDoAsk(t *testing.T) {
	fakeExec := func(_ context.Context, _, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "diff" {
			return " README.md | 1 +\n 1 file changed", nil
		}
		return "", nil // worktree add etc.
	}

	// "做" → editing run, gets a diff section
	fr := &fakeRunner{result: RunResult{Text: "加好了", NumTurns: 2}}
	h := testHandler(fr, fakeExec, "boss")
	var replies []string
	h.Handle(context.Background(), dingbot.InboundMsg{Text: "做 给README加注释", SenderStaffID: "boss"}, collectReplies(&replies))
	if !fr.gotEdit || fr.gotPrompt != "给README加注释" {
		t.Errorf("do run = edit:%v prompt:%q", fr.gotEdit, fr.gotPrompt)
	}
	// ack + result
	if len(replies) != 2 || !strings.Contains(replies[0], "收到") {
		t.Fatalf("replies = %v", replies)
	}
	if !strings.Contains(replies[1], "加好了") || !strings.Contains(replies[1], "改动") {
		t.Errorf("do result = %q", replies[1])
	}

	// "问" → read-only run
	fr2 := &fakeRunner{result: RunResult{Text: "这是只读分析"}}
	h2 := testHandler(fr2, fakeExec, "boss")
	var rep2 []string
	h2.Handle(context.Background(), dingbot.InboundMsg{Text: "问 这函数干嘛", SenderStaffID: "boss"}, collectReplies(&rep2))
	if fr2.gotEdit {
		t.Error("ask must be read-only (edit=false)")
	}
	if strings.Contains(rep2[len(rep2)-1], "改动") {
		t.Error("ask must not show a diff section")
	}
}

func TestDispatchTest(t *testing.T) {
	var gotName string
	var gotArgs []string
	fakeExec := func(_ context.Context, _, name string, args ...string) (string, error) {
		gotName, gotArgs = name, args
		return "ok\nPASS", nil
	}
	h := testHandler(&fakeRunner{}, fakeExec, "boss")
	var replies []string
	h.Handle(context.Background(), dingbot.InboundMsg{Text: "测试", SenderStaffID: "boss"}, collectReplies(&replies))
	if gotName != "go" || strings.Join(gotArgs, " ") != "test ./..." {
		t.Errorf("test ran %s %v", gotName, gotArgs)
	}
	if !strings.Contains(replies[len(replies)-1], "测试通过") {
		t.Errorf("test reply = %v", replies)
	}
}

// --- real git worktree integration (no claude, no network) ---

func TestWorktreeIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-qm", "init")

	h := &Handler{
		repoDir: repo, worktreeDir: wt, branch: "chatops/work",
		exec: realExec, logger: discardLogger(),
	}
	if err := h.ensureWorktree(context.Background()); err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("worktree .git missing: %v", err)
	}
	// idempotent: a second call must not error
	if err := h.ensureWorktree(context.Background()); err != nil {
		t.Fatalf("ensureWorktree second call: %v", err)
	}
	// status reflects the clean branch
	status := h.gitText(context.Background(), "📋", "git", "status", "--short", "--branch")
	if !strings.Contains(status, "chatops/work") {
		t.Errorf("status missing branch: %s", status)
	}
	// make a change → diff --stat shows it
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := h.gitText(context.Background(), "📝", "git", "diff", "--stat")
	if !strings.Contains(diff, "README.md") {
		t.Errorf("diff missing change: %s", diff)
	}
	// cleanup the linked worktree so TempDir removal is clean
	_, _ = realExec(context.Background(), repo, "git", "worktree", "remove", "--force", wt)
}

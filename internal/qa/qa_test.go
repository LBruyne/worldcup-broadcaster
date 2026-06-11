package qa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"worldcup-broadcaster/internal/digest"
	"worldcup-broadcaster/internal/espn"
	"worldcup-broadcaster/internal/store"
)

type fakeSender struct {
	mu      sync.Mutex
	msgs    []string
	gids    []int64
	private []string
}

func (f *fakeSender) EnqueueGroupTo(gid int64, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gids = append(f.gids, gid)
	f.msgs = append(f.msgs, msg)
}

func (f *fakeSender) SendPrivate(userID int64, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.private = append(f.private, msg)
	return nil
}

// EnqueueGroup satisfies digest.Sender (unused in these tests).
func (f *fakeSender) EnqueueGroup(msg string) { f.EnqueueGroupTo(0, msg) }

func (f *fakeSender) last(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) == 0 {
		t.Fatal("no message sent")
	}
	return f.msgs[len(f.msgs)-1]
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

type fakeLLM struct {
	reply   string
	err     error
	gotSys  string
	gotUser string
}

func (f *fakeLLM) Generate(_ context.Context, system, user string) (string, error) {
	f.gotSys, f.gotUser = system, user
	return f.reply, f.err
}

// fakeBackend serves standings/scoreboard/summary fixtures.
func fakeBackend(t *testing.T) *espn.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var file string
		switch {
		case strings.Contains(r.URL.Path, "standings"):
			file = "standings-full.json"
		case strings.Contains(r.URL.Path, "scoreboard"):
			file = "scoreboard-20221218.json"
		default:
			file = "summary-633850.json"
		}
		raw, err := os.ReadFile("../../testdata/" + file)
		if err != nil {
			http.Error(w, "missing", 500)
			return
		}
		w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return espn.NewClientWithBase(srv.URL, "fifa.world")
}

func newHandler(t *testing.T, llm LLM) (*Handler, *fakeSender) {
	t.Helper()
	client := fakeBackend(t)
	sender := &fakeSender{}
	logger := slog.Default()
	st := store.New(t.TempDir())
	dig := digest.New(client, nil, st, sender, func(string, string) {}, logger)
	h := NewHandler(Options{
		GroupIDs:    []int64{861376113, 1093353838},
		GroupNames:  map[int64]string{1093353838: "示例群"},
		AdminQQ:     10001,
		HistorySize: 20,
		EngageProb:  0, // proactive engagement off by default in tests
		SeedPersonas: map[string]string{
			"群主哥": "群主，曼联球迷，支持葡萄牙，讨厌梅罗之争，某大厂芯片",
			"铁哥":  "曼联球迷，C罗粉，数据决定一切，同行",
		},
	}, sender, llm, dig, client, st, logger)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.Start(ctx)
	return h, sender
}

func TestHelp(t *testing.T) {
	h, sender := newHandler(t, nil)
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/help")
	got := sender.last(t)
	for _, want := range []string{"罗哥1号迷弟", "/ask", "/积分榜", "/射手榜", "/晋级", "今晚几个"} {
		if !strings.Contains(got, want) {
			t.Errorf("help missing %q\n%s", want, got)
		}
	}
}

func TestWrongGroupIgnored(t *testing.T) {
	h, sender := newHandler(t, nil)
	h.OnGroupMessage(context.Background(), 999, 0, "路人", "/help")
	if sender.count() != 0 {
		t.Fatal("message from other group must be ignored")
	}
}

func TestAskUsesContextAndData(t *testing.T) {
	llm := &fakeLLM{reply: "梅西球王毋庸置疑，C罗粉丝先别急 🐸"}
	h, sender := newHandler(t, llm)
	ctx := context.Background()
	h.OnGroupMessage(ctx, 861376113, 0, "小明", "我觉得C罗才是GOAT")
	h.OnGroupMessage(ctx, 861376113, 0, "小红", "梅西七个金球笑而不语")
	h.OnGroupMessage(ctx, 861376113, 0, "小明", "/ask 梅西和C罗到底谁厉害？")
	deadlineWait(t, sender, 1)

	if got := sender.last(t); !strings.Contains(got, "梅西球王") {
		t.Errorf("reply = %s", got)
	}
	// chat context + asker + data must reach the LLM
	for _, want := range []string{"我觉得C罗才是GOAT", "小红", "梅西和C罗到底谁厉害", "积分榜"} {
		if !strings.Contains(llm.gotUser, want) {
			t.Errorf("llm payload missing %q", want)
		}
	}
	if !strings.Contains(llm.gotSys, "罗哥1号迷弟") || !strings.Contains(llm.gotSys, "克里斯蒂亚诺") {
		t.Errorf("persona prompt missing identity:\n%s", llm.gotSys)
	}
}

func TestAskWithoutQuestion(t *testing.T) {
	h, sender := newHandler(t, &fakeLLM{reply: "x"})
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/ask")
	deadlineWait(t, sender, 1)
	if got := sender.last(t); !strings.Contains(got, "用法") {
		t.Errorf("reply = %s", got)
	}
}

func TestStandingsCommand(t *testing.T) {
	h, sender := newHandler(t, nil)
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/积分榜 A")
	got := sender.last(t)
	if !strings.Contains(got, "A组积分榜") || !strings.Contains(got, "墨西哥") {
		t.Errorf("group A table wrong:\n%s", got)
	}

	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/积分榜")
	all := sender.last(t)
	if !strings.Contains(all, "12组积分速览") || !strings.Contains(all, "L组") {
		t.Errorf("all-groups view wrong:\n%s", all)
	}

	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/积分榜 Z")
	if !strings.Contains(sender.last(t), "没有 Z 组") {
		t.Error("invalid group should be rejected")
	}
}

func TestScorersCommand(t *testing.T) {
	h, sender := newHandler(t, nil)
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/射手榜")
	got := sender.last(t)
	if !strings.Contains(got, "射手榜") || !strings.Contains(got, "Kylian Mbappé（法国）3球") {
		t.Errorf("scorers wrong:\n%s", got)
	}
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/助攻榜")
	if got := sender.last(t); !strings.Contains(got, "Alexis Mac Allister") {
		t.Errorf("assists wrong:\n%s", got)
	}
}

func TestHistoryRing(t *testing.T) {
	h, _ := newHandler(t, nil)
	for i := 0; i < 30; i++ {
		h.OnGroupMessage(context.Background(), 861376113, 0, "灌水机", fmt.Sprintf("msg-%d", i))
	}
	hist := h.recentChat(861376113)
	if len(hist) != 20 {
		t.Fatalf("history = %d, want 20", len(hist))
	}
	if hist[0].Text != "msg-10" || hist[19].Text != "msg-29" {
		t.Errorf("ring window wrong: %s..%s", hist[0].Text, hist[19].Text)
	}
}

func TestServerEndToEnd(t *testing.T) {
	h, sender := newHandler(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:39131"
	go StartServer(ctx, addr, h, slog.Default())
	time.Sleep(100 * time.Millisecond)

	ev := map[string]any{
		"post_type": "message", "message_type": "group",
		"group_id": 861376113, "user_id": 10001,
		"sender":  map[string]any{"nickname": "小明", "card": ""},
		"message": []map[string]any{{"type": "text", "data": map[string]any{"text": "/help"}}},
	}
	raw, _ := json.Marshal(ev)
	resp, err := http.Post("http://"+addr+"/", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d", resp.StatusCode)
	}
	deadline := time.Now().Add(3 * time.Second)
	for sender.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := sender.last(t); !strings.Contains(got, "/ask") {
		t.Errorf("help via server wrong:\n%s", got)
	}
}

// Per-group isolation: chat context of one group must not leak into the
// other, and replies must target the origin group.
func TestPerGroupIsolation(t *testing.T) {
	llm := &fakeLLM{reply: "ok"}
	h, sender := newHandler(t, llm)
	ctx := context.Background()
	h.OnGroupMessage(ctx, 861376113, 0, "测试群人", "测试群的秘密暗号XYZZY")
	h.OnGroupMessage(ctx, 1093353838, 0, "正式群人", "正式群只聊球")
	h.OnGroupMessage(ctx, 1093353838, 0, "正式群人", "/ask 群里刚才聊了啥")
	deadlineWait(t, sender, 1)

	if strings.Contains(llm.gotUser, "XYZZY") {
		t.Error("test-group chat leaked into prod-group /ask context")
	}
	if !strings.Contains(llm.gotUser, "正式群只聊球") {
		t.Error("prod-group context missing")
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if n := len(sender.gids); n == 0 || sender.gids[n-1] != 1093353838 {
		t.Errorf("reply target = %v, want prod group", sender.gids)
	}
}

func TestSelfJoinIntro(t *testing.T) {
	h, sender := newHandler(t, nil)
	h.OnSelfJoin(1093353838)
	got := sender.last(t)
	if !strings.Contains(got, "示例群的各位老板") || !strings.Contains(got, "/ask") {
		t.Errorf("intro wrong:\n%s", got)
	}
	h.OnSelfJoin(424242) // unknown group: silent
	if sender.count() != 1 {
		t.Error("unknown group join should be ignored")
	}
}

func TestAskCarriesGroupName(t *testing.T) {
	llm := &fakeLLM{reply: "ok"}
	h, sender := newHandler(t, llm)
	h.OnGroupMessage(context.Background(), 1093353838, 0, "社员", "/ask 咱们群叫什么")
	deadlineWait(t, sender, 1)
	if !strings.Contains(llm.gotUser, "示例群") {
		t.Errorf("group name missing from payload: %.200s", llm.gotUser)
	}
	if !strings.Contains(llm.gotSys, "示例群") {
		t.Error("persona prompt missing group-name rule")
	}
}

func TestServerSelfJoinNotice(t *testing.T) {
	h, sender := newHandler(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:39132"
	go StartServer(ctx, addr, h, slog.Default())
	time.Sleep(100 * time.Millisecond)

	raw, _ := json.Marshal(map[string]any{
		"post_type": "notice", "notice_type": "group_increase",
		"group_id": 1093353838, "user_id": 1051722693, "self_id": 1051722693,
	})
	resp, err := http.Post("http://"+addr+"/", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for sender.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := sender.last(t); !strings.Contains(got, "示例群") {
		t.Errorf("join intro via server wrong:\n%s", got)
	}
}

// Follow-up mode: after the bot replies, the next message must be evaluated
// and answered when the LLM decides it's directed at the bot.
func TestEngageFollowup(t *testing.T) {
	llm := &fakeLLM{reply: "回的就是你，急了？"}
	h, sender := newHandler(t, llm)
	ctx := context.Background()
	h.OnGroupMessage(ctx, 861376113, 0, "小明", "/ask 葡萄牙能夺冠吗")
	deadlineWait(t, sender, 1)
	// within followup window: bot must evaluate and (per fake llm) answer
	h.OnGroupMessage(ctx, 861376113, 0, "小明", "你懂个球，C罗带队必夺冠")
	deadlineWait(t, sender, 2)
	if got := sender.last(t); !strings.Contains(got, "急了") {
		t.Errorf("followup reply = %s", got)
	}
	if !strings.Contains(llm.gotUser, "followup") {
		t.Errorf("mode missing: %.200s", llm.gotUser)
	}
	// bot's own earlier reply must be visible in context
	if !strings.Contains(llm.gotUser, BotName) {
		t.Error("bot's own message absent from context")
	}
}

func TestEngagePassStaysSilent(t *testing.T) {
	llm := &fakeLLM{reply: "PASS"}
	h, sender := newHandler(t, llm)
	ctx := context.Background()
	h.OnGroupMessage(ctx, 861376113, 0, "小明", "/ask 在吗")
	deadlineWait(t, sender, 1) // the ask reply itself
	base := sender.count()
	h.OnGroupMessage(ctx, 861376113, 0, "小红", "今天中午吃啥")
	time.Sleep(100 * time.Millisecond)
	if sender.count() != base {
		t.Fatalf("PASS must stay silent, got %q", sender.last(t))
	}
}

func TestEngageProactiveProbability(t *testing.T) {
	llm := &fakeLLM{reply: "梅西不进前三的榜单都是钓鱼帖"}
	h, sender := newHandler(t, llm)
	h.opts.EngageProb = 1.0 // always
	h.randFloat = func() float64 { return 0 }
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "姆巴佩世界第一人没悬念了吧")
	deadlineWait(t, sender, 1)
	if !strings.Contains(llm.gotUser, "proactive") {
		t.Errorf("mode missing: %.200s", llm.gotUser)
	}
	// seed personas must ride along
	if !strings.Contains(llm.gotSys, "梅罗弹药库") {
		t.Error("persona core missing from engage prompt")
	}

	// p=0: never engage proactively
	llm2 := &fakeLLM{reply: "不该出现"}
	h2, sender2 := newHandler(t, llm2)
	h2.randFloat = func() float64 { return 0.99 }
	h2.OnGroupMessage(context.Background(), 861376113, 0, "小明", "随便聊聊")
	time.Sleep(100 * time.Millisecond)
	if sender2.count() != 0 {
		t.Fatal("p=0 must never proactively engage")
	}
}

func TestPersonaCommand(t *testing.T) {
	h, sender := newHandler(t, nil)
	ctx := context.Background()
	// query seed persona
	h.OnGroupMessage(ctx, 861376113, 0, "小明", "/人设 群主哥")
	if got := sender.last(t); !strings.Contains(got, "曼联") || !strings.Contains(got, "某大厂") {
		t.Errorf("seed persona query = %s", got)
	}
	// non-admin set rejected
	h.OnGroupMessage(ctx, 861376113, 12345, "小明", "/人设 小红 阿森纳球迷")
	if got := sender.last(t); !strings.Contains(got, "只有管理员") {
		t.Errorf("non-admin set = %s", got)
	}
	// admin set works
	h.OnGroupMessage(ctx, 861376113, 10001, "管理员", "/人设 小红 阿森纳球迷，嘴硬")
	if got := sender.last(t); !strings.Contains(got, "✅") {
		t.Errorf("admin set = %s", got)
	}
	if p := h.profiles.Persona("小红"); !strings.Contains(p, "阿森纳") {
		t.Errorf("persona not stored: %s", p)
	}
}

func TestAskCarriesPersonas(t *testing.T) {
	llm := &fakeLLM{reply: "ok"}
	h, sender := newHandler(t, llm)
	ctx := context.Background()
	h.OnGroupMessage(ctx, 861376113, 0, "铁哥", "数据才是硬道理")
	h.OnGroupMessage(ctx, 861376113, 0, "铁哥", "/ask C罗是不是史上最佳")
	deadlineWait(t, sender, 1)
	if !strings.Contains(llm.gotUser, "数据决定一切") {
		t.Errorf("asker persona missing: %.300s", llm.gotUser)
	}
	if !strings.Contains(llm.gotSys, "梅西强无敌") && !strings.Contains(llm.gotSys, "梅西比C罗强") {
		t.Error("messi-supremacy rule missing from persona core")
	}
}

func deadlineWait(t *testing.T, s *fakeSender, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.count() < want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.count() < want {
		t.Fatalf("timed out waiting for %d messages, have %d", want, s.count())
	}
}

// Concurrent askers: every question answered exactly once, strictly serial,
// and an overflowing queue is rejected politely instead of deadlocking.
func TestConcurrentAsksSerialized(t *testing.T) {
	var inflight, maxInflight atomic.Int32
	llm := &slowLLM{delay: 30 * time.Millisecond, inflight: &inflight, max: &maxInflight}
	h, sender := newHandler(t, llm)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.OnGroupMessage(ctx, 861376113, 0, fmt.Sprintf("提问人%d", i), fmt.Sprintf("/ask 问题%d", i))
		}(i)
	}
	wg.Wait()
	deadlineWait(t, sender, 10)
	if maxInflight.Load() > 1 {
		t.Fatalf("LLM concurrency = %d, want strictly serial", maxInflight.Load())
	}
	if sender.count() != 10 {
		t.Fatalf("replies = %d, want exactly 10", sender.count())
	}
}

func TestAskQueueOverflowRejected(t *testing.T) {
	block := make(chan struct{})
	llm := &blockingLLM{block: block}
	h, sender := newHandler(t, llm)
	ctx := context.Background()
	// 1 in-flight + 16 queued + N rejected
	for i := 0; i < 25; i++ {
		h.OnGroupMessage(ctx, 861376113, 0, "轰炸机", fmt.Sprintf("/ask 问题%d", i))
	}
	deadlineWait(t, sender, 1) // at least one rejection notice arrives sync
	found := false
	sender.mu.Lock()
	for _, m := range sender.msgs {
		if strings.Contains(m, "队列满") {
			found = true
		}
	}
	sender.mu.Unlock()
	if !found {
		t.Fatal("overflow not rejected")
	}
	close(block) // release; no goroutine leak / deadlock
}

type slowLLM struct {
	delay    time.Duration
	inflight *atomic.Int32
	max      *atomic.Int32
}

func (s *slowLLM) Generate(_ context.Context, _, user string) (string, error) {
	cur := s.inflight.Add(1)
	for {
		old := s.max.Load()
		if cur <= old || s.max.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(s.delay)
	s.inflight.Add(-1)
	return "答案", nil
}

type blockingLLM struct{ block chan struct{} }

func (b *blockingLLM) Generate(ctx context.Context, _, _ string) (string, error) {
	select {
	case <-b.block:
	case <-ctx.Done():
	}
	return "迟到的答案", nil
}

func TestPersonaCoreSafetyRedline(t *testing.T) {
	for _, want := range []string{"严禁", "身体", "职业"} {
		if !strings.Contains(personaCore, want) {
			t.Errorf("persona core missing safety rule %q", want)
		}
	}
}

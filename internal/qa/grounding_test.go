package qa

import (
	"context"
	"strings"
	"testing"
)

// routerLLM answers the routing call with a canned route and the final ask
// with a canned reply, recording both payloads.
type routerLLM struct {
	route      string
	answer     string
	routeUser  string
	askUser    string
	askThink   bool
	routeThink bool
	calls      int
}

func (r *routerLLM) Generate(ctx context.Context, sys, user string) (string, error) {
	return r.GenerateThink(ctx, sys, user, true)
}

func (r *routerLLM) GenerateThink(_ context.Context, sys, user string, think bool) (string, error) {
	r.calls++
	if strings.Contains(sys, "数据路由器") {
		r.routeUser, r.routeThink = user, think
		return r.route, nil
	}
	r.askUser, r.askThink = user, think
	return r.answer, nil
}

func TestGroundingPipeline(t *testing.T) {
	llm := &routerLLM{
		route:  `{"needs":["team:葡萄牙","standings"],"difficulty":"hard","search":""}`,
		answer: "葡萄牙稳了",
	}
	h, sender := newHandler(t, llm)
	h.OnGroupMessage(context.Background(), 861376113, 0, "铁哥", "/ask 葡萄牙小组赛对手都是谁，出线概率多大")
	deadlineWait(t, sender, 1)

	if llm.routeThink {
		t.Error("router must run with thinking disabled")
	}
	if !llm.askThink {
		t.Error("hard question must enable thinking")
	}
	// fixture scoreboard (2022 final) has no Portugal: schedule falls back to
	// persisted/none, so the key assertions are structural:
	if !strings.Contains(llm.askUser, "已核实数据") {
		t.Errorf("grounded data missing: %.200s", llm.askUser)
	}
	if !strings.Contains(llm.askUser, "小组积分榜") {
		t.Errorf("standings not assembled: %.300s", llm.askUser)
	}
	if got := sender.last(t); got != "葡萄牙稳了" {
		t.Errorf("reply = %s", got)
	}
}

func TestGroundingEasyNoThinking(t *testing.T) {
	llm := &routerLLM{
		route:  `{"needs":[],"difficulty":"easy","search":""}`,
		answer: "随便聊聊",
	}
	h, sender := newHandler(t, llm)
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/ask 你吃了吗")
	deadlineWait(t, sender, 1)
	if llm.askThink {
		t.Error("easy question must not enable thinking")
	}
	if !strings.Contains(llm.askUser, "今日数据") {
		t.Error("banter fallback live snapshot missing")
	}
}

func TestRouterParseLenient(t *testing.T) {
	llm := &routerLLM{route: "好的，这是路由：\n{\"needs\":[\"schedule\"],\"difficulty\":\"easy\",\"search\":\"\"}\n以上"}
	h, _ := newHandler(t, llm)
	r := h.route(context.Background(), "明天有什么比赛")
	if len(r.Needs) != 1 || r.Needs[0] != "schedule" {
		t.Errorf("route = %+v", r)
	}
}

func TestFullScheduleFromFixture(t *testing.T) {
	h, _ := newHandler(t, nil)
	rows := h.fullSchedule(context.Background())
	// fixture scoreboard-20221218.json: one finished match (the 2022 final)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	if r.Home != "阿根廷" || r.Away != "法国" || r.Status != "已结束" || !strings.Contains(r.Score, "点球4:2") {
		t.Errorf("row = %+v", r)
	}
	// team filter via Chinese name
	m := h.teamMatches(context.Background(), "法国")
	if len(m) != 1 {
		t.Errorf("team filter = %d", len(m))
	}
}

func TestParseDDG(t *testing.T) {
	html := `<a rel="nofollow" class="result__a" href="x">Kylian <b>Mbapp&eacute;</b> - Real Madrid</a>
	<a class="result__snippet" href="x">Mbapp&eacute; joined <b>Real Madrid</b> in 2024.</a>
	<a rel="nofollow" class="result__a" href="y">Second</a>`
	res := parseDDG(html, 5)
	if len(res) != 2 {
		t.Fatalf("results = %d", len(res))
	}
	if res[0].Title != "Kylian Mbappé - Real Madrid" || !strings.Contains(res[0].Snippet, "joined Real Madrid") {
		t.Errorf("res0 = %+v", res[0])
	}
}

package qa

import (
	"context"
	"strings"
	"testing"
	"time"
)

// routerLLM answers the routing call with a canned route, the fact-verifier
// call with a canned verdict, and the final ask with a canned reply,
// recording all payloads.
type routerLLM struct {
	route       string
	verdict     string
	answer      string
	routeUser   string
	verifyUser  string
	askUser     string
	askThink    bool
	routeThink  bool
	verifyThink bool
	calls       int
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
	if strings.Contains(sys, "数据核实员") {
		r.verifyUser, r.verifyThink = user, think
		if r.verdict == "" {
			return "", context.DeadlineExceeded
		}
		return r.verdict, nil
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

// nativeLLM is a routerLLM that also advertises native web search, so the
// grounding layer must skip the DuckDuckGo scrape + verify pipeline.
type nativeLLM struct{ routerLLM }

func (n *nativeLLM) NativeSearch() bool { return true }

// With a native-search LLM, a factual question must NOT trigger the manual
// verifier or inject DuckDuckGo evidence, but authoritative ESPN data is still
// assembled inline and thinking stays on for factual questions.
func TestNativeSearchSkipsDuckDuckGo(t *testing.T) {
	llm := &nativeLLM{routerLLM{
		route:  `{"needs":["standings"],"difficulty":"hard","search":"哈兰德 进球|Haaland goals"}`,
		answer: "哈兰德上赛季英超进球很猛",
	}}
	h, sender := newHandler(t, llm)
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/ask 哈兰德上赛季进了多少球")
	deadlineWait(t, sender, 1)

	if llm.verifyUser != "" {
		t.Error("native search must skip the fact verifier")
	}
	if strings.Contains(llm.askUser, "网络搜索结果") || strings.Contains(llm.askUser, "数据核实结论") {
		t.Errorf("native search must not inject DuckDuckGo evidence: %.300s", llm.askUser)
	}
	if !strings.Contains(llm.askUser, "小组积分榜") {
		t.Errorf("ESPN standings still expected inline: %.300s", llm.askUser)
	}
	if !llm.askThink {
		t.Error("factual question must enable thinking under native search")
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

// A search-bearing route must run the deep-thinking fact verifier; its
// verdict is injected for the final answer, which then skips thinking
// (the verifier already did the heavy lifting).
func TestSearchTriggersVerification(t *testing.T) {
	llm := &routerLLM{
		route:   `{"needs":[],"difficulty":"easy","search":"哈兰德 2025-26赛季 英超 进球数|Haaland 2025-26 Premier League goals"}`,
		verdict: `{"conclusion":"哈兰德2025-26赛季英超进26球（来源:fbref）","confidence":"high"}`,
		answer:  "26球",
	}
	h, sender := newHandler(t, llm)
	// preload fake search cache entries so webSearch hits disk, not network
	for _, q := range []string{"哈兰德 2025-26赛季 英超 进球数", "Haaland 2025-26 Premier League goals"} {
		h.profiles.store.SaveJSON("wcdb", "search-"+sanitizeKey(q),
			searchEntry{Results: []byte(`[{"title":"Haaland 26 goals","snippet":"x"}]`), FetchedAt: timeNow()})
	}
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/ask 哈兰德上赛季英超进了多少球")
	deadlineWait(t, sender, 1)
	if !llm.verifyThink {
		t.Error("fact verifier must run with thinking enabled")
	}
	if llm.askThink {
		t.Error("final answer must skip thinking when a verdict is present")
	}
	if !strings.Contains(llm.askUser, "数据核实结论") || !strings.Contains(llm.askUser, "哈兰德2025-26赛季英超进26球") {
		t.Errorf("verdict missing from grounded data: %.400s", llm.askUser)
	}
	if !strings.Contains(llm.verifyUser, "Haaland 26 goals") {
		t.Errorf("verifier evidence missing search results: %.300s", llm.verifyUser)
	}
	if c := strings.Count(llm.askUser, "网络搜索结果"); c != 2 {
		t.Errorf("expected 2 search blocks, got %d", c)
	}
}

// When the verifier fails (LLM error), the final answer must fall back to
// deep thinking over raw search results.
func TestVerifierFailureFallsBackToThinking(t *testing.T) {
	llm := &routerLLM{
		route:  `{"needs":[],"difficulty":"easy","search":"哈兰德 测试失败查询"}`,
		answer: "这我还真没数",
	}
	h, sender := newHandler(t, llm)
	h.profiles.store.SaveJSON("wcdb", "search-"+sanitizeKey("哈兰德 测试失败查询"),
		searchEntry{Results: []byte(`[{"title":"t","snippet":"s"}]`), FetchedAt: timeNow()})
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/ask 哈兰德练习赛进了几个")
	deadlineWait(t, sender, 1)
	if !llm.askThink {
		t.Error("verifier failure must fall back to thinking on the final answer")
	}
	if strings.Contains(llm.askUser, "数据核实结论") {
		t.Error("no verdict should be injected when verification fails")
	}
}

func TestDDGTargetURL(t *testing.T) {
	href := "//duckduckgo.com/l/?uddg=https%3A%2F%2Ffbref.com%2Fen%2Fplayers%2F1f44ac21%2FErling-Haaland&rut=abc"
	if got := ddgTargetURL(href); got != "https://fbref.com/en/players/1f44ac21/Erling-Haaland" {
		t.Errorf("ddgTargetURL = %q", got)
	}
	if got := ddgTargetURL("https://example.com/x"); got != "https://example.com/x" {
		t.Errorf("direct url = %q", got)
	}
}

func TestTrustedSource(t *testing.T) {
	for _, u := range []string{"https://fbref.com/en/x", "https://www.transfermarkt.us/y", "https://en.wikipedia.org/wiki/Z"} {
		if !trustedSource(u) {
			t.Errorf("%s should be trusted", u)
		}
	}
	if trustedSource("https://www.youtube.com/watch?v=x") {
		t.Error("youtube must not be a trusted stat source")
	}
}

func timeNow() time.Time { return time.Now() }

// Evaluation/comparison questions the router misroutes as banter must be
// caught by the deterministic factual-keyword guard and forced to search.
func TestFactualGuardForcesSearch(t *testing.T) {
	llm := &routerLLM{
		route:   `{"needs":[],"difficulty":"easy","search":""}`,
		verdict: `{"conclusion":"B费2025-26赛季英超9球14助（来源:fbref）","confidence":"high"}`,
		answer:  "ok",
	}
	h, sender := newHandler(t, llm)
	q := "b费上赛季表现怎么样"
	h.profiles.store.SaveJSON("wcdb", "search-"+sanitizeKey(q),
		searchEntry{Results: []byte(`[{"title":"Bruno Fernandes 25/26 stats","snippet":"x"}]`), FetchedAt: timeNow()})
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/ask "+q)
	deadlineWait(t, sender, 1)
	if !strings.Contains(llm.askUser, "数据核实结论") {
		t.Errorf("factual guard did not force search+verify: %.300s", llm.askUser)
	}
	if !strings.Contains(llm.askUser, "今天") {
		t.Error("ask payload must carry today's date")
	}
}

func TestFactualGuardSkipsBanter(t *testing.T) {
	llm := &routerLLM{
		route:  `{"needs":[],"difficulty":"easy","search":""}`,
		answer: "ok",
	}
	h, sender := newHandler(t, llm)
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/ask 你是谁派来的")
	deadlineWait(t, sender, 1)
	if strings.Contains(llm.askUser, "数据核实结论") || llm.verifyUser != "" {
		t.Error("pure banter must not trigger the verify pipeline")
	}
}

// "这场比赛" questions must be grounded in the per-match ESPN feed and must
// not fall through to a web search (which surfaces unrelated matches).
func TestDeicticMatchGrounding(t *testing.T) {
	llm := &routerLLM{
		route:  `{"needs":[],"difficulty":"easy","search":"首发阵容 比赛"}`,
		answer: "ok",
	}
	h, sender := newHandler(t, llm)
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "/ask 看看这场比赛首发阵容")
	deadlineWait(t, sender, 1)
	if !strings.Contains(llm.askUser, "比赛详情（当前）") {
		t.Errorf("match detail missing: %.300s", llm.askUser)
	}
	if strings.Contains(llm.askUser, "网络搜索结果") {
		t.Error("deictic match question must not carry web search noise")
	}
	// fixture is the finished 2022 final: lineups come from its summary
	if !strings.Contains(llm.askUser, "首发阵容") {
		t.Errorf("lineups missing: %.300s", llm.askUser)
	}
}

func TestResolveMatchRefByTeam(t *testing.T) {
	h, _ := newHandler(t, nil)
	row := h.resolveMatchRef(context.Background(), "法国")
	if row == nil || row.Away != "法国" {
		t.Fatalf("row = %+v", row)
	}
	if h.resolveMatchRef(context.Background(), "不存在的队") != nil {
		t.Error("unknown team must resolve to nil")
	}
}

// A direct @-question to the bot (called mode) about the current match must
// flow through the grounding pipeline and carry the per-match feed.
func TestCalledEngageGetsGrounding(t *testing.T) {
	llm := &routerLLM{
		route:  `{"needs":[],"difficulty":"easy","search":""}`,
		answer: "首发给你列好了",
	}
	h, sender := newHandler(t, llm)
	h.OnGroupMessage(context.Background(), 861376113, 0, "铁哥", "AAA 这场比赛首发阵容是什么")
	deadlineWait(t, sender, 1)
	if !strings.Contains(llm.askUser, "已核实数据") || !strings.Contains(llm.askUser, "比赛详情（当前）") {
		t.Errorf("called engage missing grounded match detail: %.300s", llm.askUser)
	}
}

// Boxscore stats (possession, shots) must reach the match-detail blob.
func TestMatchDetailCarriesStats(t *testing.T) {
	h, _ := newHandler(t, nil)
	detail := h.matchDetail(context.Background(), "当前")
	if detail == nil {
		t.Fatal("no match detail from fixture")
	}
	stats, ok := detail["技术统计"].([]map[string]any)
	if !ok || len(stats) != 2 {
		t.Fatalf("stats = %#v", detail["技术统计"])
	}
	joined := strings.Join(stats[0]["统计"].([]string), " ")
	for _, want := range []string{"控球率%", "射门", "传球成功"} {
		if !strings.Contains(joined, want) {
			t.Errorf("stats missing %q: %s", want, joined)
		}
	}
}

// A real @-mention arrives as an "at" segment; it must be perceived exactly
// like calling the bot by name, with full /ask-grade grounding.
func TestAtSegmentTreatedAsCalled(t *testing.T) {
	ev := onebotEvent{SelfID: 1051722693}
	ev.Message = []byte(`[{"type":"at","data":{"qq":"1051722693"}},{"type":"text","data":{"text":" 这场比赛首发阵容是什么"}}]`)
	text := ev.messageText()
	if !addressesBot(text) {
		t.Fatalf("at segment not perceived as called: %q", text)
	}

	llm := &routerLLM{
		route:  `{"needs":[],"difficulty":"easy","search":""}`,
		answer: "首发已列",
	}
	h, sender := newHandler(t, llm)
	h.OnGroupMessage(context.Background(), 861376113, 0, "铁哥", text)
	deadlineWait(t, sender, 1)
	if !strings.Contains(llm.askUser, "已核实数据") || !strings.Contains(llm.askUser, "比赛详情（当前）") {
		t.Errorf("@-question missing grounded data: %.300s", llm.askUser)
	}
}

// Called mode runs grounding even WITHOUT factual keywords — identical
// perception to /ask, whose router sees every question.
func TestCalledAlwaysGrounded(t *testing.T) {
	llm := &routerLLM{
		route:  `{"needs":[],"difficulty":"easy","search":""}`,
		answer: "在呢",
	}
	h, sender := newHandler(t, llm)
	h.OnGroupMessage(context.Background(), 861376113, 0, "小明", "AAA 你在吗")
	deadlineWait(t, sender, 1)
	if llm.routeUser == "" {
		t.Error("called engage must run the grounding router")
	}
	if !strings.Contains(llm.askUser, "已核实数据") {
		t.Errorf("called engage missing grounded payload: %.200s", llm.askUser)
	}
}

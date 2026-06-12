package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"worldcup-broadcaster/internal/cnmap"
)

// matchRow is one compact schedule entry of the local World Cup database —
// small enough to ship all 104 to the LLM when needed.
type matchRow struct {
	ID      string `json:"id"`
	Stage   string `json:"stage"`
	Home    string `json:"home"`   // Chinese name
	Away    string `json:"away"`   // Chinese name
	Score   string `json:"score"`  // "2:1"（含点球标注）, "" when not started
	Status  string `json:"status"` // 未开赛 / 进行中 / 已结束
	Kickoff string `json:"kickoff_beijing"`
}

// scheduleDB caches the full-tournament schedule on disk and in memory.
type scheduleDB struct {
	mu        sync.Mutex
	rows      []matchRow
	fetchedAt time.Time
}

const scheduleTTL = 15 * time.Minute

// fullSchedule returns the (possibly cached) 104-match schedule, refreshing
// from ESPN when stale and persisting to the local database dir.
func (h *Handler) fullSchedule(ctx context.Context) []matchRow {
	h.schedDB.mu.Lock()
	defer h.schedDB.mu.Unlock()
	if len(h.schedDB.rows) > 0 && time.Since(h.schedDB.fetchedAt) < scheduleTTL {
		return h.schedDB.rows
	}
	sb, err := h.espn.Scoreboard(ctx, "20260611-20260719")
	if err != nil {
		h.logger.Error("schedule db refresh failed", "error", err)
		if len(h.schedDB.rows) > 0 {
			return h.schedDB.rows // stale beats empty
		}
		var saved []matchRow
		if h.profiles.store.LoadJSON("wcdb", "schedule", &saved) == nil {
			h.schedDB.rows = saved
		}
		return h.schedDB.rows
	}
	rows := make([]matchRow, 0, len(sb.Events))
	for i := range sb.Events {
		ev := &sb.Events[i]
		home, away := evHomeAway(ev)
		r := matchRow{
			ID:    ev.ID,
			Stage: stageCN(ev.Season.Slug),
			Home:  cnmap.Name(home.Team.DisplayName),
			Away:  cnmap.Name(away.Team.DisplayName),
		}
		if ko, err := ev.Kickoff(); err == nil {
			cstT := ko.UTC().Add(8 * time.Hour)
			r.Kickoff = fmt.Sprintf("%d月%d日 %s", int(cstT.Month()), cstT.Day(), cstT.Format("15:04"))
		}
		switch ev.Status.Type.State {
		case "post":
			r.Status = "已结束"
			r.Score = home.Score + ":" + away.Score
			if home.ShootoutScore > 0 || away.ShootoutScore > 0 {
				r.Score += fmt.Sprintf("（点球%d:%d）", int(home.ShootoutScore), int(away.ShootoutScore))
			}
		case "in":
			r.Status = "进行中"
			r.Score = home.Score + ":" + away.Score
		default:
			r.Status = "未开赛"
		}
		rows = append(rows, r)
	}
	h.schedDB.rows = rows
	h.schedDB.fetchedAt = time.Now()
	if err := h.profiles.store.SaveJSON("wcdb", "schedule", rows); err != nil {
		h.logger.Error("persist schedule db failed", "error", err)
	}
	return rows
}

func stageCN(slug string) string {
	switch slug {
	case "group-stage":
		return "小组赛"
	case "round-of-32":
		return "1/16决赛"
	case "round-of-16":
		return "1/8决赛"
	case "quarterfinals":
		return "1/4决赛"
	case "semifinals":
		return "半决赛"
	case "third-place-playoff", "third-place":
		return "季军赛"
	case "final":
		return "决赛"
	}
	return slug
}

// teamMatches filters the schedule for one team (Chinese or English name).
func (h *Handler) teamMatches(ctx context.Context, team string) []matchRow {
	cn := cnmap.Name(cnmap.EnglishName(strings.TrimSpace(team)))
	var out []matchRow
	for _, r := range h.fullSchedule(ctx) {
		if r.Home == cn || r.Away == cn {
			out = append(out, r)
		}
	}
	return out
}

const routerPrompt = `你是数据路由器。给你一个QQ群足球问题，判断回答它需要哪些数据。只输出一行JSON，不要任何其他文字：
{"needs":[...],"difficulty":"easy|hard","search":""}
needs 可选值（按需多选，无需数据时为空数组）：
- "schedule"：完整世界杯赛程（问赛程/某天比赛/什么时候踢）
- "team:队名"：某支球队的比赛与赛果（问到具体球队时，每队一条）
- "standings"：小组积分榜/出线形势
- "leaderboards"：射手榜/助攻榜
- "today"：今明两天的比赛
search：以下情况【必须】填搜索关键词：问题涉及任何具体球员（含外号：B费、丁丁、拉师傅等都是球员）或球队的数据/近况/表现/成绩/评价/比较/排名/逐场明细，或本届世界杯数据之外的足球事实（历史战绩、转会、伤病、俱乐部赛事等）。"XX表现怎么样""谁成绩好"这类评价比较类问题也是事实数据问题，必须搜索。可以给最多2个查询（用|分隔），中英文各一个效果最好，外号要换成正式名字（B费→Bruno Fernandes）。纯闲聊/对线/观点类问题才留空。
搜索词里的相对时间必须先换算成具体赛季再写入：欧洲联赛赛季从每年8月跨到次年5月，按给出的"今天"换算（例：今天是2026年6月，刚结束的"上赛季/本赛季"=2025-26赛季，再往前一年才是2024-25）。涉及赛季数据时，搜索词必须同时含球员/球队名、换算后的具体赛季（如"2025-26"）、联赛或赛事名、指标名（进球/助攻等）。
difficulty：涉及具体球员/球队事实数据的问题、需要多步推理/复杂分析/出线概率计算的，一律 hard；纯闲聊对线为 easy。`

type routeResult struct {
	Needs      []string `json:"needs"`
	Difficulty string   `json:"difficulty"`
	Search     string   `json:"search"`
}

// route classifies the question with a cheap no-thinking LLM call.
func (h *Handler) route(ctx context.Context, question string) routeResult {
	res := routeResult{Difficulty: "easy"}
	tl, ok := h.llm.(ThinkingLLM)
	if !ok {
		return res
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := tl.GenerateThink(cctx, routerPrompt,
		fmt.Sprintf("今天是%s（北京时间）。问题：%s", time.Now().UTC().Add(8*time.Hour).Format("2006-01-02"), question), false)
	if err != nil {
		h.logger.Error("router call failed", "error", err)
		return res
	}
	// be lenient: extract the first {...} block
	if i := strings.Index(out, "{"); i >= 0 {
		if j := strings.LastIndex(out, "}"); j > i {
			out = out[i : j+1]
		}
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		h.logger.Error("router parse failed", "raw", out, "error", err)
		return routeResult{Difficulty: "easy"}
	}
	if res.Difficulty != "hard" {
		res.Difficulty = "easy"
	}
	return res
}

// grounding assembles the verified-data blob for a question and reports
// whether the question warrants thinking mode.
func (h *Handler) grounding(ctx context.Context, question string) (string, bool) {
	r := h.route(ctx, question)
	data := map[string]any{}
	for _, need := range r.Needs {
		switch {
		case need == "schedule":
			data["世界杯完整赛程"] = h.fullSchedule(ctx)
		case strings.HasPrefix(need, "team:"):
			team := strings.TrimPrefix(need, "team:")
			if m := h.teamMatches(ctx, team); len(m) > 0 {
				data["球队赛程_"+team] = m
			}
		case need == "standings":
			if standings, err := h.espn.FullStandings(ctx); err == nil {
				data["小组积分榜"] = standings
			}
		case need == "leaderboards":
			if boards, err := h.dig.Leaderboards(ctx); err == nil {
				data["射手助攻榜"] = boards
			}
		case need == "today":
			var todays json.RawMessage = json.RawMessage(h.liveData(ctx))
			data["今日数据"] = todays
		}
	}
	// Safety net: the no-thinking router sometimes misjudges evaluation or
	// comparison questions as banter. Anything carrying a season/stat keyword
	// must hit the search+verify path — fall back to the raw question as the
	// query (the verifier can refine it in its second round).
	if r.Search == "" && factualQuestionRe.MatchString(question) {
		r.Search = question
		r.Difficulty = "hard"
		h.logger.Info("router missed factual question, forcing search", "question", question)
	}
	searched := false
	evidence := map[string]any{}
	for _, q := range strings.Split(r.Search, "|") {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if results := h.webSearch(ctx, q); len(results) > 0 {
			data["网络搜索结果（查询:"+q+"）"] = results
			evidence["搜索（"+q+"）"] = results
			searched = true
		}
	}
	verdict := ""
	if searched {
		// Snippet synthesis is exactly where the cheap path hallucinates:
		// run the deep-thinking fact verifier, which reads actual source
		// pages and adjudicates a single answer.
		r.Difficulty = "hard"
		verdict = h.verifyFacts(ctx, question, evidence)
		if verdict != "" {
			data["数据核实结论"] = verdict
		}
	}
	if len(data) == 0 {
		// banter questions still get the cheap live snapshot for grounding
		data["今日数据"] = json.RawMessage(h.liveData(ctx))
	}
	// When the verifier already produced an adjudicated conclusion, the final
	// persona call is pure styling — skip a second (slow) thinking pass.
	think := r.Difficulty == "hard" && verdict == ""
	raw, err := json.Marshal(data)
	if err != nil {
		return "{}", think
	}
	h.logger.Info("question grounded",
		"needs", r.Needs, "search", r.Search, "difficulty", r.Difficulty, "verified", verdict != "")
	return string(raw), think
}

// factualQuestionRe catches questions that must never skip the search path:
// season references, stat words, evaluation/comparison asks.
var factualQuestionRe = regexp.MustCompile(
	`上赛季|本赛季|这赛季|赛季|去年|今年|最近|数据|进球|助攻|零封|扑救|表现|成绩|排名|纪录|记录|历史|交锋|转会|身价|伤病|受伤|多少|几个|几球|几次|金靴|金球|冠军|夺冠|出场|首发|评分`)

const verifierPrompt = `你是足球数据核实员。给你一个问题和若干网络证据（搜索结果的标题/摘要/URL，可能还有权威来源的页面正文）。你的任务：深入思考，裁决出唯一、准确的事实结论。
规则：
- 先确定时间口径：会给你"今天"的日期。欧洲联赛赛季从每年8月跨到次年5月（例：今天是2026年6月，则"上赛季"=2025-26赛季）。
- 严格区分统计口径：联赛进球≠各项赛事总进球≠生涯总进球≠为国家队进球；自媒体/视频标题（YouTube等）不可信；优先权威统计源（fbref、ESPN、联赛官网、Transfermarkt、StatMuse、维基百科的正文数据）。
- 多个来源数字冲突时，分析冲突原因（口径不同？赛季不同？来源不可靠？），裁决出最可信的唯一答案。
- 现有证据不足以下结论时，可要求补充搜索（换更精确的关键词）或抓取某条搜索结果URL的正文。
- 问题要求列表/逐场明细（如"每个助攻给了谁"）时，conclusion 就给完整的多行列表（仍须注明赛季/口径和来源）；证据页面正文里有明细就逐条提取，别偷懒概括。
只输出JSON，不要其他文字：
{"conclusion":"结论（通常一句话；列表类问题给完整多行列表），必须包含赛季/口径、数字和依据来源","confidence":"high|medium|low","need_queries":"还需要的搜索词，最多2个用|分隔，不需要留空","need_url":"需要抓取正文的URL，不需要留空"}`

type verdictEntry struct {
	Verdict   string    `json:"verdict"`
	FetchedAt time.Time `json:"fetched_at"`
}

// verifyFacts runs a deep-thinking adjudication pass over search evidence,
// optionally fetching source pages or refining queries (one extra round),
// and returns a single verified conclusion ("" when verification failed).
func (h *Handler) verifyFacts(ctx context.Context, question string, evidence map[string]any) string {
	tl, ok := h.llm.(ThinkingLLM)
	if !ok {
		return ""
	}
	cacheKey := "verdict-" + sanitizeKey(question)
	var cached verdictEntry
	if err := h.profiles.store.LoadJSON("wcdb", cacheKey, &cached); err == nil &&
		time.Since(cached.FetchedAt) < searchTTL && cached.Verdict != "" {
		return cached.Verdict
	}

	// Proactively read the first trusted source per query (2 pages max):
	// real page text beats snippets and usually saves the refine round.
	fetched := 0
	for key, v := range evidence {
		if fetched >= 2 {
			break
		}
		results, ok := v.([]searchResult)
		if !ok {
			continue
		}
		for _, res := range results {
			if res.URL == "" || !trustedSource(res.URL) {
				continue
			}
			if text := fetchPageText(ctx, res.URL); text != "" {
				evidence["页面正文（"+res.URL+"）"] = text
				fetched++
				h.logger.Info("verifier fetched source page", "for", key, "url", res.URL)
			}
			break
		}
	}

	today := time.Now().UTC().Add(8 * time.Hour).Format("2006-01-02")
	for round := 0; round < 2; round++ {
		payload, err := json.Marshal(map[string]any{"今天": today, "问题": question, "证据": evidence})
		if err != nil {
			return ""
		}
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		out, err := tl.GenerateThink(cctx, verifierPrompt, string(payload), true)
		cancel()
		if err != nil {
			h.logger.Error("fact verifier call failed", "error", err)
			return ""
		}
		if i := strings.Index(out, "{"); i >= 0 {
			if j := strings.LastIndex(out, "}"); j > i {
				out = out[i : j+1]
			}
		}
		var v struct {
			Conclusion  string `json:"conclusion"`
			Confidence  string `json:"confidence"`
			NeedQueries string `json:"need_queries"`
			NeedURL     string `json:"need_url"`
		}
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			h.logger.Error("fact verifier parse failed", "raw", out, "error", err)
			return ""
		}
		if round == 0 && (v.NeedQueries != "" || v.NeedURL != "") {
			grew := false
			for _, q := range strings.SplitN(v.NeedQueries, "|", 2) {
				q = strings.TrimSpace(q)
				if q == "" {
					continue
				}
				if results := h.webSearch(ctx, q); len(results) > 0 {
					evidence["搜索（"+q+"）"] = results
					grew = true
				}
			}
			if v.NeedURL != "" {
				if text := fetchPageText(ctx, v.NeedURL); text != "" {
					evidence["页面正文（"+v.NeedURL+"）"] = text
					grew = true
				}
			}
			if grew {
				h.logger.Info("verifier requested more evidence", "queries", v.NeedQueries, "url", v.NeedURL)
				continue
			}
		}
		if v.Conclusion == "" {
			return ""
		}
		verdict := v.Conclusion
		if v.Confidence != "" {
			verdict += "（置信度:" + v.Confidence + "）"
		}
		if err := h.profiles.store.SaveJSON("wcdb", cacheKey, verdictEntry{Verdict: verdict, FetchedAt: time.Now()}); err != nil {
			h.logger.Error("persist verdict cache failed", "error", err)
		}
		return verdict
	}
	return ""
}

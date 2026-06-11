package qa

import (
	"context"
	"encoding/json"
	"fmt"
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
search：以下情况【必须】填搜索关键词：问题涉及任何具体球员（人名）的数据/近况/效力球队/进球数，或本届世界杯数据之外的足球事实（历史战绩、转会、伤病、俱乐部赛事等）。可以给最多2个查询（用|分隔，例如"哈兰德 2024-25赛季 进球数|Haaland 2024-25 season goals"），中英文各一个效果最好。纯闲聊/对线/观点类问题才留空。
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
	searched := false
	for _, q := range strings.Split(r.Search, "|") {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if results := h.webSearch(ctx, q); results != "" {
			data["网络搜索结果（查询:"+q+"）"] = json.RawMessage(results)
			searched = true
		}
	}
	// Any question that needed a web search gets deep thinking: synthesizing
	// snippets correctly is exactly where the cheap path hallucinates.
	if searched {
		r.Difficulty = "hard"
	}
	if len(data) == 0 {
		// banter questions still get the cheap live snapshot for grounding
		data["今日数据"] = json.RawMessage(h.liveData(ctx))
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return "{}", r.Difficulty == "hard"
	}
	h.logger.Info("question grounded", "needs", r.Needs, "search", r.Search, "difficulty", r.Difficulty)
	return string(raw), r.Difficulty == "hard"
}

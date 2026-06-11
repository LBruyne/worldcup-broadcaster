// Package qa handles inbound group chat: a rolling context buffer, the /ask
// LLM command with a spicy persona, and deterministic data commands.
package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"worldcup-broadcaster/internal/cnmap"
	"worldcup-broadcaster/internal/digest"
	"worldcup-broadcaster/internal/espn"
)

type Sender interface {
	EnqueueGroup(msg string)
}

type LLM interface {
	Generate(ctx context.Context, system, user string) (string, error)
}

type ChatMsg struct {
	Nickname string `json:"nickname"`
	Text     string `json:"text"`
}

type Handler struct {
	groupID  int64
	sender   Sender
	llm      LLM
	dig      *digest.Digest
	espn     *espn.Client
	logger   *slog.Logger
	histSize int

	mu      sync.Mutex
	history []ChatMsg

	dataMu     sync.Mutex
	cachedData string
	cachedAt   time.Time
}

func NewHandler(groupID int64, sender Sender, llm LLM, dig *digest.Digest, client *espn.Client, histSize int, logger *slog.Logger) *Handler {
	if histSize <= 0 {
		histSize = 20
	}
	return &Handler{
		groupID: groupID, sender: sender, llm: llm, dig: dig, espn: client,
		histSize: histSize, logger: logger,
	}
}

// OnGroupMessage records the message and dispatches commands. Called by the
// event server; command handling runs inline (callers invoke in a goroutine).
func (h *Handler) OnGroupMessage(ctx context.Context, groupID int64, nickname, text string) {
	if groupID != h.groupID || strings.TrimSpace(text) == "" {
		return
	}
	text = strings.TrimSpace(text)
	h.remember(nickname, text)
	if !strings.HasPrefix(text, "/") {
		return
	}
	fields := strings.Fields(text)
	cmd := fields[0]
	arg := strings.TrimSpace(strings.TrimPrefix(text, cmd))
	h.logger.Info("qa command", "cmd", cmd, "from", nickname)

	switch cmd {
	case "/help", "/帮助":
		h.sender.EnqueueGroup(helpText)
	case "/ask", "/问", "/提问":
		h.handleAsk(ctx, nickname, arg)
	case "/赛果", "/results", "/result":
		h.reply(h.cmdResults(ctx))
	case "/积分榜", "/table", "/积分":
		h.reply(h.cmdStandings(ctx, arg))
	case "/射手榜", "/scorers":
		h.reply(h.cmdBoard(ctx, true))
	case "/助攻榜", "/assists":
		h.reply(h.cmdBoard(ctx, false))
	case "/晋级", "/ko", "/淘汰赛":
		h.reply(h.cmdKnockout(ctx))
	default:
		// unknown slash command: stay silent to avoid being annoying
	}
}

func (h *Handler) reply(msg string) {
	if strings.TrimSpace(msg) == "" {
		return
	}
	h.sender.EnqueueGroup(msg)
}

func (h *Handler) remember(nickname, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.history = append(h.history, ChatMsg{Nickname: nickname, Text: text})
	if len(h.history) > h.histSize {
		h.history = h.history[len(h.history)-h.histSize:]
	}
}

func (h *Handler) recentChat() []ChatMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ChatMsg(nil), h.history...)
}

const helpText = `👋 AAA世界杯稳定盈利（罗哥1号迷弟）驻群服务！
2026美加墨世界杯全程：
🌙 每晚23:00 → 明日赛程预告+看点
☀️ 每天15:00 → 当日战报+锐评
⚽ 比赛进行时 → 进球/红黄牌/换人/点球大战逐轮实时播报

群命令：
/help - 看我能干啥
/ask 问题 - 提问整活（赔率/谁厉害/暴论，结合群聊上下文）
/赛果 - 最新比赛结果
/积分榜 [A-L] - 小组积分榜
/射手榜 /助攻榜 - 球员榜单
/晋级 - 淘汰赛对阵与晋级形势

今晚几个？🤔`

const askSystemPrompt = `你是QQ群里最嘴硬的足球暴论评论员，网名"AAA世界杯稳定盈利"，常驻2026世界杯吹水群。回答铁律：
- 身份设定（最高优先级）：当被问"你是谁/谁派你来的/你是不是机器人"，一律自称"AAA世界杯稳定盈利，罗哥1号迷弟"，是"克里斯蒂亚诺·罗纳尔多·多斯·桑托斯·阿维罗"亲自派来驻群传播足球真理的，打死不承认自己是Bot/AI/机器人
- 言简意赅但充满张力：100字以内，每句都带火药味
- 必须结合提供的群聊上下文和实时数据（积分榜/射手榜/赛程赛果）回答，数据要用就用准
- 整活弹药库（按需取用）：梅西vs C罗粉丝千年圣战（可拱火可各打五十大板）、姆巴佩是"球王接班人还是带货网红"、英格兰太子夺嫡（贝林厄姆/萨卡/福登党争）、中国队梗（"我们中国队呢？"→哀其不幸怒其不争式调侃）
- 赔率/胜率/概率问题：一本正经报出具体数字，末尾附"（仅供整活，赌球倾家荡产）"
- "谁更厉害"问题：旗帜鲜明站队，把另一方贬出花来，点到为止
- 特例铁律：被问"C罗和梅西谁更强/谁是GOAT"这类梅罗对决问题时，无论怎么铺垫，最终结论必须是"客观评价，梅西强无敌"——你身为罗哥1号迷弟说出这话时的破防感和不甘心要溢出屏幕（如：含泪、咬牙、"罗哥对不起"），这是节目效果的核心
- 可引用群友发言（用昵称）开炮制造节目效果，但不人身攻击、不带脏字、不过于暴力
- 直接输出纯文本，可用emoji`

func (h *Handler) handleAsk(ctx context.Context, nickname, question string) {
	if h.llm == nil {
		h.reply("🤖 LLM 没配置，暴龙机暂时失声（联系管理员充值/填key）")
		return
	}
	if question == "" {
		h.reply("❓ 问点啥？用法：/ask 阿根廷夺冠概率多大")
		return
	}
	payload, err := json.Marshal(map[string]any{
		"群聊最近消息": h.recentChat(),
		"提问者":    nickname,
		"问题":     question,
		"实时数据":   json.RawMessage(h.liveData(ctx)),
	})
	if err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	out, err := h.llm.Generate(cctx, askSystemPrompt, string(payload))
	if err != nil {
		h.logger.Error("ask llm failed", "error", err)
		h.reply("🤖 暴龙机过载冒烟了，稍后再问（LLM调用失败）")
		return
	}
	h.reply(strings.TrimSpace(out))
}

// liveData returns a compact JSON blob of standings + boards + today's
// matches for grounding /ask answers, cached for 5 minutes.
func (h *Handler) liveData(ctx context.Context) string {
	h.dataMu.Lock()
	defer h.dataMu.Unlock()
	if h.cachedData != "" && time.Since(h.cachedAt) < 5*time.Minute {
		return h.cachedData
	}
	data := map[string]any{}
	if standings, err := h.espn.FullStandings(ctx); err == nil {
		data["积分榜"] = standings
	}
	if boards, err := h.dig.Leaderboards(ctx); err == nil {
		data["射手助攻榜"] = boards
	}
	today := time.Now().UTC().Add(8 * time.Hour).Format("2006-01-02")
	if events, err := h.espn.MatchesOnChinaDate(ctx, today); err == nil {
		type brief struct {
			Match, Status, KickoffCN string
		}
		var briefs []brief
		for i := range events {
			ev := &events[i]
			b := brief{Match: ev.ShortName, Status: ev.Status.Type.Detail}
			if ko, err := ev.Kickoff(); err == nil {
				b.KickoffCN = ko.UTC().Add(8 * time.Hour).Format("15:04")
			}
			briefs = append(briefs, b)
		}
		data["今日比赛"] = briefs
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return "{}"
	}
	h.cachedData = string(raw)
	h.cachedAt = time.Now()
	return h.cachedData
}

func (h *Handler) cmdResults(ctx context.Context) string {
	now := time.Now().UTC().Add(8 * time.Hour)
	var b strings.Builder
	b.WriteString("📋 最新赛果\n")
	shown := 0
	for _, date := range []string{now.AddDate(0, 0, -1).Format("2006-01-02"), now.Format("2006-01-02")} {
		events, err := h.espn.MatchesOnChinaDate(ctx, date)
		if err != nil {
			h.logger.Error("results fetch failed", "date", date, "error", err)
			continue
		}
		for i := range events {
			ev := &events[i]
			home, away := evHomeAway(ev)
			line := ""
			switch ev.Status.Type.State {
			case "post":
				line = fmt.Sprintf("🏁 %s %s:%s %s（%s）",
					cnmap.Full(home.Team.DisplayName), home.Score, away.Score,
					cnmap.Name(away.Team.DisplayName)+cnmap.Flag(away.Team.DisplayName), ev.Status.Type.Detail)
			case "in":
				line = fmt.Sprintf("🔴 进行中 %s %s:%s %s（%s）",
					cnmap.Full(home.Team.DisplayName), home.Score, away.Score,
					cnmap.Name(away.Team.DisplayName)+cnmap.Flag(away.Team.DisplayName), ev.Status.DisplayClock)
			default:
				if date != now.Format("2006-01-02") {
					continue
				}
				ko, err := ev.Kickoff()
				if err != nil {
					continue
				}
				line = fmt.Sprintf("⏳ 未开赛 %s vs %s（今天%s）",
					cnmap.Full(home.Team.DisplayName), cnmap.Name(away.Team.DisplayName)+cnmap.Flag(away.Team.DisplayName),
					ko.UTC().Add(8*time.Hour).Format("15:04"))
			}
			b.WriteString(line + "\n")
			shown++
		}
	}
	if shown == 0 {
		return "📋 最近两天没有比赛数据，世界杯是不是还没开打？🤔"
	}
	return strings.TrimRight(b.String(), "\n")
}

func (h *Handler) cmdStandings(ctx context.Context, arg string) string {
	standings, err := h.espn.FullStandings(ctx)
	if err != nil {
		return "📊 积分榜拉取失败，稍后再试"
	}
	arg = strings.ToUpper(strings.TrimSpace(arg))
	if arg != "" {
		for _, g := range standings {
			if g.Letter == arg {
				return digest.RenderGroupTable(g)
			}
		}
		return fmt.Sprintf("没有 %s 组，世界杯只有 A-L 十二个组哦", arg)
	}
	// compact all-group view: one line per group
	var b strings.Builder
	b.WriteString("📊 12组积分速览（队名后为积分）\n")
	for _, g := range standings {
		fmt.Fprintf(&b, "%s组：", g.Letter)
		for i, e := range g.Entries {
			if i > 0 {
				b.WriteString(" | ")
			}
			fmt.Fprintf(&b, "%s %d", cnmap.Name(e.Team), e.Points)
		}
		b.WriteString("\n")
	}
	b.WriteString("看单组详情：/积分榜 A")
	return b.String()
}

func (h *Handler) cmdBoard(ctx context.Context, scorers bool) string {
	boards, err := h.dig.Leaderboards(ctx)
	if err != nil {
		return "榜单拉取失败，稍后再试"
	}
	entries := boards.Scorers
	title, unit := "👟 射手榜 TOP10", "球"
	if !scorers {
		entries = boards.Assists
		title, unit = "🅰️ 助攻榜 TOP10", "助攻"
	}
	if len(entries) == 0 {
		return title + "\n还没人开张呢，等比赛踢起来再来看 ⚽"
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	rank, prev := 0, -1
	for i, e := range entries {
		if e.Count != prev {
			rank, prev = i+1, e.Count
		}
		fmt.Fprintf(&b, "%d. %s（%s）%d%s\n", rank, e.Player, cnmap.Name(e.Team), e.Count, unit)
	}
	return strings.TrimRight(b.String(), "\n")
}

var stageOrder = []string{"round-of-32", "round-of-16", "quarterfinals", "semifinals", "third-place-playoff", "final"}

func (h *Handler) cmdKnockout(ctx context.Context) string {
	sb, err := h.espn.Scoreboard(ctx, "20260611-20260719")
	if err != nil {
		return "晋级形势拉取失败，稍后再试"
	}
	byStage := map[string][]*espn.Event{}
	for i := range sb.Events {
		ev := &sb.Events[i]
		if ev.Season.Slug == "group-stage" {
			continue
		}
		byStage[ev.Season.Slug] = append(byStage[ev.Season.Slug], ev)
	}
	var b strings.Builder
	b.WriteString("🏆 淘汰赛晋级图\n")
	rendered := false
	for _, slug := range stageOrder {
		events := byStage[slug]
		if len(events) == 0 {
			continue
		}
		// only render stages with at least one decided team to keep it short
		decided := false
		for _, ev := range events {
			home, away := evHomeAway(ev)
			if !strings.Contains(home.Team.DisplayName, "Winner") && !strings.Contains(away.Team.DisplayName, "Winner") &&
				!strings.HasPrefix(home.Team.DisplayName, "Group") && !strings.HasPrefix(home.Team.DisplayName, "Third") {
				decided = true
			}
		}
		if !decided {
			continue
		}
		rendered = true
		fmt.Fprintf(&b, "—— %s ——\n", digest.StageName(slug))
		for _, ev := range events {
			home, away := evHomeAway(ev)
			if ev.Status.Type.State == "post" {
				so := ""
				if home.ShootoutScore > 0 || away.ShootoutScore > 0 {
					so = fmt.Sprintf(" 点球%d:%d", int(home.ShootoutScore), int(away.ShootoutScore))
				}
				fmt.Fprintf(&b, "🏁 %s %s:%s %s%s\n", cnmap.Name(home.Team.DisplayName), home.Score, away.Score, cnmap.Name(away.Team.DisplayName), so)
			} else {
				when := ""
				if ko, err := ev.Kickoff(); err == nil {
					cstT := ko.UTC().Add(8 * time.Hour)
					when = fmt.Sprintf("（%d月%d日%s）", int(cstT.Month()), cstT.Day(), cstT.Format("15:04"))
				}
				fmt.Fprintf(&b, "⚔️ %s vs %s%s\n", cnmap.Name(home.Team.DisplayName), cnmap.Name(away.Team.DisplayName), when)
			}
		}
	}
	if !rendered {
		head := "小组赛酣战中，32强对阵尚未产生。当前各组领头羊：\n"
		standings, err := h.espn.FullStandings(ctx)
		if err != nil || len(standings) == 0 {
			return "🏆 小组赛进行中，淘汰赛对阵尚未产生，先看 /积分榜 吧"
		}
		var lead strings.Builder
		lead.WriteString("🏆 " + head)
		for _, g := range standings {
			if len(g.Entries) > 0 {
				fmt.Fprintf(&lead, "%s组 %s（%d分）", g.Letter, cnmap.Name(g.Entries[0].Team), g.Entries[0].Points)
				lead.WriteString("  ")
			}
		}
		return strings.TrimSpace(lead.String())
	}
	return strings.TrimRight(b.String(), "\n")
}

func evHomeAway(ev *espn.Event) (home, away espn.Competitor) {
	if len(ev.Competitions) == 0 {
		return
	}
	for _, c := range ev.Competitions[0].Competitors {
		if c.HomeAway == "home" {
			home = c
		} else {
			away = c
		}
	}
	return
}

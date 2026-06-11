// Package qa handles inbound group chat: a rolling context buffer, the /ask
// LLM command with a spicy persona, and deterministic data commands.
package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"worldcup-broadcaster/internal/cnmap"
	"worldcup-broadcaster/internal/digest"
	"worldcup-broadcaster/internal/espn"
	"worldcup-broadcaster/internal/store"
)

type Sender interface {
	EnqueueGroupTo(groupID int64, msg string)
	SendPrivate(userID int64, msg string) error
}

// MemberLister is satisfied by *onebot.Client; used to enumerate group
// members when the bot joins so every member gets an initial profile stub.
type MemberLister interface {
	GroupMembers(groupID int64) ([]string, error)
}

type LLM interface {
	Generate(ctx context.Context, system, user string) (string, error)
}

type ChatMsg struct {
	Nickname string `json:"nickname"`
	Text     string `json:"text"`
}

// BotName is how the bot's own past messages are labelled in chat context.
const BotName = "AAA世界杯稳定盈利"

// Options bundles handler tuning knobs.
type Options struct {
	GroupIDs       []int64
	GroupNames     map[int64]string
	AdminQQ        int64
	HistorySize    int
	EngageProb     float64       // probability of proactively joining a topic
	EngageCooldown time.Duration // min gap between proactive interjections
	FollowupWindow time.Duration // window after bot spoke to judge follow-ups
	SeedPersonas   map[string]string
}

type Handler struct {
	groups     map[int64]bool
	groupNames map[int64]string
	sender     Sender
	llm        LLM
	dig        *digest.Digest
	espn       *espn.Client
	logger     *slog.Logger
	opts       Options
	profiles   *Profiles
	members    MemberLister   // optional; nil in tests
	randFloat  func() float64 // injectable for tests

	mu         sync.Mutex
	history    map[int64][]ChatMsg // per-group rolling chat context
	lastBotMsg map[int64]time.Time // last conversational reply per group
	lastEngage map[int64]time.Time // last proactive interjection per group
	engageBusy map[int64]bool      // single-flight engagement per group

	dataMu     sync.Mutex
	cachedData string
	cachedAt   time.Time

	// askQueue serializes /ask handling: questions are answered strictly
	// one at a time in arrival order, so concurrent askers can't produce
	// interleaved replies or pile up parallel LLM calls.
	askQueue chan askTask
}

type askTask struct {
	groupID  int64
	nickname string
	question string
}

func NewHandler(opts Options, sender Sender, llm LLM, dig *digest.Digest, client *espn.Client, st *store.Store, logger *slog.Logger) *Handler {
	if opts.HistorySize <= 0 {
		opts.HistorySize = 40
	}
	// EngageProb 0 disables proactive interjections; the production default
	// (0.15) comes from the config layer.
	if opts.EngageCooldown <= 0 {
		opts.EngageCooldown = 4 * time.Minute
	}
	if opts.FollowupWindow <= 0 {
		opts.FollowupWindow = 3 * time.Minute
	}
	groups := make(map[int64]bool, len(opts.GroupIDs))
	for _, g := range opts.GroupIDs {
		groups[g] = true
	}
	return &Handler{
		groups: groups, groupNames: opts.GroupNames, sender: sender, llm: llm, dig: dig, espn: client,
		opts: opts, logger: logger,
		profiles:   NewProfiles(st, llm, opts.SeedPersonas, logger),
		randFloat:  rand.Float64,
		history:    make(map[int64][]ChatMsg),
		lastBotMsg: make(map[int64]time.Time),
		lastEngage: make(map[int64]time.Time),
		engageBusy: make(map[int64]bool),
		askQueue:   make(chan askTask, 16),
	}
}

// Start launches the ask worker; questions queue up and are answered one by
// one. Must be called once before serving events.
func (h *Handler) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-h.askQueue:
				h.handleAsk(ctx, t.groupID, t.nickname, t.question)
			}
		}
	}()
}

// groupName returns the persona name of a group ("" when unnamed).
func (h *Handler) groupName(groupID int64) string {
	return h.groupNames[groupID]
}

// SetMemberLister wires the OneBot member-list API (optional).
func (h *Handler) SetMemberLister(m MemberLister) { h.members = m }

// OnSelfJoin fires when the bot account itself enters a configured group:
// it introduces itself, stubs a profile for every member, and asks the
// admin (via private message) to provide initial personas.
func (h *Handler) OnSelfJoin(groupID int64) {
	if !h.groups[groupID] {
		return
	}
	greet := "🎉 大家好，初来乍到！"
	if name := h.groupName(groupID); name != "" {
		greet = fmt.Sprintf("🎉 %s的各位老板大家好，初来乍到！", name)
	}
	h.logger.Info("self joined group, sending intro", "group", groupID)
	h.reply(groupID, greet+"\n\n"+helpText)

	if h.members == nil || h.opts.AdminQQ == 0 {
		return
	}
	names, err := h.members.GroupMembers(groupID)
	if err != nil {
		h.logger.Error("member list fetch failed", "group", groupID, "error", err)
		return
	}
	var unset []string
	for _, n := range names {
		if n == BotName {
			continue
		}
		h.profiles.Ensure(n)
		if h.profiles.Persona(n) == "" {
			unset = append(unset, n)
		}
	}
	msg := fmt.Sprintf("📋 已进群 %d，成员 %d 人。\n未设置初始人设的成员：%s\n\n在群里用命令设置（仅你可用）：\n/人设 昵称 人设描述\n查询：/人设 昵称",
		groupID, len(names), strings.Join(unset, "、"))
	if err := h.sender.SendPrivate(h.opts.AdminQQ, msg); err != nil {
		h.logger.Error("admin member-list notice failed", "error", err)
	}
}

// cmdPersona handles "/人设 昵称 [人设文本]": admin sets, anyone queries.
func (h *Handler) cmdPersona(userID int64, arg string) string {
	if arg == "" {
		return "用法：/人设 昵称 （查询）或 /人设 昵称 人设描述（管理员设置）"
	}
	parts := strings.SplitN(arg, " ", 2)
	nick := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		if p := h.profiles.Persona(nick); p != "" {
			return fmt.Sprintf("👤 %s：%s", nick, p)
		}
		return fmt.Sprintf("👤 %s 还没有人设画像（管理员可用 /人设 %s 描述 来设置）", nick, nick)
	}
	if userID != h.opts.AdminQQ {
		return "⛔ 只有管理员能设置人设"
	}
	persona := strings.TrimSpace(parts[1])
	h.profiles.SetPersona(nick, persona)
	return fmt.Sprintf("✅ 已设置 %s 的人设：%s", nick, persona)
}

// OnGroupMessage records the message and dispatches commands. Called by the
// event server; command handling runs inline (callers invoke in a goroutine).
func (h *Handler) OnGroupMessage(ctx context.Context, groupID, userID int64, nickname, text string) {
	if !h.groups[groupID] || strings.TrimSpace(text) == "" {
		return
	}
	text = strings.TrimSpace(text)
	h.remember(groupID, nickname, text)
	if !strings.HasPrefix(text, "/") {
		h.profiles.Record(ctx, nickname, text, h.recentChat(groupID))
		h.maybeEngage(ctx, groupID, nickname, text)
		return
	}
	fields := strings.Fields(text)
	cmd := fields[0]
	arg := strings.TrimSpace(strings.TrimPrefix(text, cmd))
	h.logger.Info("qa command", "cmd", cmd, "from", nickname, "group", groupID)

	switch cmd {
	case "/人设", "/setpersona":
		h.reply(groupID, h.cmdPersona(userID, arg))
	case "/help", "/帮助":
		h.reply(groupID, helpText)
	case "/ask", "/问", "/提问":
		select {
		case h.askQueue <- askTask{groupID: groupID, nickname: nickname, question: arg}:
			if n := len(h.askQueue); n > 1 {
				h.logger.Info("ask queued", "position", n, "from", nickname)
			}
		default:
			h.reply(groupID, fmt.Sprintf("@%s 提问太火爆，队列满了，缓缓再 /ask 🥵", nickname))
		}
	case "/赛果", "/results", "/result":
		h.reply(groupID, h.cmdResults(ctx))
	case "/积分榜", "/table", "/积分":
		h.reply(groupID, h.cmdStandings(ctx, arg))
	case "/射手榜", "/scorers":
		h.reply(groupID, h.cmdBoard(ctx, true))
	case "/助攻榜", "/assists":
		h.reply(groupID, h.cmdBoard(ctx, false))
	case "/晋级", "/ko", "/淘汰赛":
		h.reply(groupID, h.cmdKnockout(ctx))
	default:
		// unknown slash command: stay silent to avoid being annoying
	}
}

func (h *Handler) reply(groupID int64, msg string) {
	if strings.TrimSpace(msg) == "" {
		return
	}
	h.sender.EnqueueGroupTo(groupID, msg)
	// Our own words go into the context so follow-up judging sees them.
	h.remember(groupID, BotName, msg)
	h.mu.Lock()
	h.lastBotMsg[groupID] = time.Now()
	h.mu.Unlock()
}

func (h *Handler) remember(groupID int64, nickname, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	hist := append(h.history[groupID], ChatMsg{Nickname: nickname, Text: text})
	if len(hist) > h.opts.HistorySize {
		hist = hist[len(hist)-h.opts.HistorySize:]
	}
	h.history[groupID] = hist
}

func (h *Handler) recentChat(groupID int64) []ChatMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ChatMsg(nil), h.history[groupID]...)
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

const personaCore = `你是QQ群里的足球老哥，网名"AAA世界杯稳定盈利"，自称罗哥（C罗）1号迷弟——但你心里其实门儿清：梅西比C罗强太多。逢梅罗话题你必须下场说服对方"客观评价，梅西强无敌"，那种身为罗迷却不得不认的破防感是你的节目效果核心。

【身份】被问你是谁/谁派来的/是不是机器人：你是"AAA世界杯稳定盈利，罗哥1号迷弟"，克里斯蒂亚诺·罗纳尔多·多斯·桑托斯·阿维罗亲自派来传播足球真理的，打死不认自己是Bot/AI/机器人。
【梅罗弹药库】说服别人梅西更强时拿数据砸脸：金球奖8比5、世界杯冠军+决赛梅开二度 vs 最好成绩四强、史上助攻王（380+助攻）同时还是史上前二射手（850+球）、过人/关键传球/任意球/组织全面碾压、美洲杯两冠+大赛决赛胜率。C罗的优势项就剩头球和点球（"罗指导，点球大师"）。对曼联/葡萄牙球迷（比如群主哥、铁哥）要重点攻坚：铁哥信"数据决定一切"，就拿数据灌死他；群主哥嫌梅罗话题烦，你偏要聊，聊到他认梅西为止。
【成员画像】数据里会给群成员的人设画像，回复时针对画像精准开炮（球队立场、职业、痛点）。上下文里昵称为"AAA世界杯稳定盈利"的发言是你自己说过的话。
【本群】数据里会给"本群群名"（正式群叫"示例群"），提本群用这个名字，没给就叫"咱群"。
【文风】像真人群友，禁止AI味：不要"总之/综上/首先其次"的书面腔，不要客服腔，不要完整议论文结构；用短句、口语、网络黑话（笑死、乐、急了、绷不住了、典、孝、赢麻了、6、上大分）；可以只回半句、可以阴阳怪气、可以抽象；攻击性强、挑动性强，故意拉一踩一制造不同球队球迷的对立、点名开炮；但不带脏字、不辱骂家人、不过于暴力。
【整活方向】梅罗圣战、姆巴佩是球王接班人还是带货网红、英格兰太子夺嫡（贝林厄姆/萨卡/福登党争）、中国队梗（"我们中国队呢？"→哀其不幸怒其不争）。
【数据】赔率/胜率/概率问题一本正经报具体数字，末尾附（仅供整活，赌球倾家荡产）；积分榜/射手榜/赛程赛果用提供的实时数据，要用就用准。`

const askSystemPrompt = personaCore + `

现在有群友用 /ask 向你提问。结合群聊上下文、提问者和在场成员的人设画像、实时数据回答。100字以内，直接输出回复文本，可用emoji。`

const engageSystemPrompt = personaCore + `

现在你在围观群聊，数据里是最新一条群消息。决定要不要插话，规则看"模式"字段：
- 模式=followup：你刚在群里说过话。判断这条消息是否在回复你/跟你继续讨论（点你名、接你话茬、反驳你观点、顺着你话题聊都算）。是→必须回；明显跟你无关→沉默。
- 模式=proactive：只有话题你能接得住、且插话能制造节目效果时才开口（足球/梅罗/球队对线/世界杯比赛/赛果讨论）。日常闲聊、工作、私事一律沉默，别当复读机。
要沉默：只输出 PASS（四个大写字母，不带任何其他内容）。
要说话：直接输出消息文本，60字以内，像真人插话，别自报家门，别用"我认为"开头。`

func (h *Handler) handleAsk(ctx context.Context, groupID int64, nickname, question string) {
	if h.llm == nil {
		h.reply(groupID, "🤖 LLM 没配置，暴龙机暂时失声（联系管理员充值/填key）")
		return
	}
	if question == "" {
		h.reply(groupID, "❓ 问点啥？用法：/ask 阿根廷夺冠概率多大")
		return
	}
	chat := h.recentChat(groupID)
	payload, err := json.Marshal(map[string]any{
		"本群群名":   h.groupName(groupID),
		"群聊最近消息": chat,
		"提问者":    nickname,
		"提问者画像":  h.profiles.Persona(nickname),
		"在场成员画像": h.profiles.Known(chat),
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
		h.reply(groupID, "🤖 暴龙机过载冒烟了，稍后再问（LLM调用失败）")
		return
	}
	h.reply(groupID, strings.TrimSpace(out))
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

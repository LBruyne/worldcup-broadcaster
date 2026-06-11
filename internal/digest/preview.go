package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"worldcup-broadcaster/internal/cnmap"
	"worldcup-broadcaster/internal/espn"
)

// previewMatch is the LLM-friendly per-match record, also persisted to disk.
type previewMatch struct {
	ID        string          `json:"id"`
	KickoffCN string          `json:"kickoff_beijing_time"`
	Stage     string          `json:"stage"`
	Home      string          `json:"home"`
	Away      string          `json:"away"`
	HomeForm  string          `json:"home_recent_form,omitempty"`
	AwayForm  string          `json:"away_recent_form,omitempty"`
	Venue     string          `json:"venue,omitempty"`
	City      string          `json:"city,omitempty"`
	H2H       []espn.H2HGame  `json:"head_to_head,omitempty"`
	Standings json.RawMessage `json:"group_standings,omitempty"`
}

const previewSystemPrompt = `你是一位风趣幽默的中文足球解说员，擅长用活泼带梗的语言点评世界杯。` +
	`用户会给你明日世界杯赛程的JSON数据（含完整积分榜和射手/助攻榜）。请为每场比赛写2-3句"看点"分析` +
	`（聚焦出线/晋级形势、恩怨情仇、球星对决、冷门可能），每场以"🔸 队名 vs 队名"开头。` +
	`2026世界杯规则：48队12组，每组前2名直接晋级32强，12个小组第三中成绩最好的8个也晋级。` +
	`请结合积分榜数据明确指出：谁赢了就基本出线、谁输了就悬、是否有提前晋级/出局的生死战。` +
	`最后用一句话给整个比赛日总评。直接输出纯文本，不要markdown标题，不要重复赛程时间等已知信息，总长度控制在600字以内。`

// Preview fetches the schedule for the given China date (normally D+1),
// persists data, generates LLM highlights, and broadcasts the digest.
func (d *Digest) Preview(ctx context.Context, date string) error {
	events, err := d.espn.MatchesOnChinaDate(ctx, date)
	if err != nil {
		d.alert("preview", fmt.Sprintf("获取 %s 赛程失败: %v", date, err))
		return fmt.Errorf("preview %s: %w", date, err)
	}
	if len(events) == 0 {
		d.logger.Info("no matches on date, sending rest-day note", "date", date)
		d.sender.EnqueueGroup(fmt.Sprintf("🌙 晚上好！%s 是休赛日，没有比赛。养精蓄锐，咱们改日再战 😴", displayDate(date)))
		return nil
	}

	matches := make([]previewMatch, 0, len(events))
	for i := range events {
		ev := &events[i]
		home, away := homeAway(ev)
		pm := previewMatch{
			ID:       ev.ID,
			Stage:    StageName(ev.Season.Slug),
			Home:     home.Team.DisplayName,
			Away:     away.Team.DisplayName,
			HomeForm: home.Form,
			AwayForm: away.Form,
		}
		if ko, err := ev.Kickoff(); err == nil {
			pm.KickoffCN = ko.In(cst).Format("15:04")
		}
		if len(ev.Competitions) > 0 {
			pm.Venue = ev.Competitions[0].Venue.FullName
			pm.City = ev.Competitions[0].Venue.Address.City
		}
		// Per-match summary brings H2H + group standings; failures here only
		// degrade the preview detail.
		if sum, _, err := d.espn.Summary(ctx, ev.ID); err != nil {
			d.logger.Error("preview summary fetch failed", "match", ev.ID, "error", err)
		} else {
			if len(sum.HeadToHeadGames) > 0 {
				pm.H2H = sum.HeadToHeadGames[0].Events
			}
			pm.Standings = sum.Standings
		}
		matches = append(matches, pm)
	}

	if err := d.store.SaveJSON(date, "schedule", matches); err != nil {
		d.logger.Error("persist schedule failed", "error", err)
	}

	standings, boards := d.tablesAndBoards(ctx, date)

	text := d.renderPreview(date, events, matches)
	teams := map[string]bool{}
	for _, pm := range matches {
		teams[pm.Home], teams[pm.Away] = true, true
	}
	if section := renderStandingsSection(standings, teams); section != "" {
		text += blockSep + section
	}
	if section := renderBoards(boards); section != "" {
		text += blockSep + section
	}
	if highlights := d.previewHighlights(ctx, date, matches, standings, boards); highlights != "" {
		text += blockSep + "⭐ 明日看点（DeepSeek 锐评版）\n" + highlights
	}
	d.sendSplit(text)
	d.logger.Info("preview broadcast", "date", date, "matches", len(matches))
	return nil
}

// tablesAndBoards fetches full standings and leaderboards, best-effort: a
// failure of either only drops its section.
func (d *Digest) tablesAndBoards(ctx context.Context, date string) ([]espn.GroupStanding, *Boards) {
	standings, err := d.espn.FullStandings(ctx)
	if err != nil {
		d.logger.Error("full standings fetch failed", "error", err)
	} else if err := d.store.SaveJSON(date, "standings", standings); err != nil {
		d.logger.Error("persist standings failed", "error", err)
	}
	boards, err := d.Leaderboards(ctx)
	if err != nil {
		d.logger.Error("leaderboards build failed", "error", err)
	} else if err := d.store.SaveJSON(date, "leaderboards", boards); err != nil {
		d.logger.Error("persist leaderboards failed", "error", err)
	}
	return standings, boards
}

// renderStandingsSection renders the group tables involving the given teams
// (group stage only; empty during knockout rounds or before data exists).
func renderStandingsSection(all []espn.GroupStanding, teams map[string]bool) string {
	groups := relevantGroups(all, teams)
	played := false
	for _, g := range groups {
		for _, e := range g.Entries {
			if e.Played > 0 {
				played = true
			}
		}
	}
	if !played {
		return "" // all-zero tables before the first whistle are noise
	}
	parts := make([]string, 0, len(groups))
	for _, g := range groups {
		parts = append(parts, RenderGroupTable(g))
	}
	return strings.Join(parts, "\n\n")
}

func (d *Digest) renderPreview(date string, events []espn.Event, matches []previewMatch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🌙 各位老板晚上好！明天又是被足球唤醒的一天～\n🏆 2026世界杯 %s 赛程（共%d场）", displayDate(date), len(matches))
	for i, pm := range matches {
		b.WriteString(blockSep)
		fmt.Fprintf(&b, "📍 第%d场 | %s\n", i+1, pm.Stage)
		fmt.Fprintf(&b, "%s vs %s\n", cnmap.Full(pm.Home), cnmap.Name(pm.Away)+cnmap.Flag(pm.Away))
		fmt.Fprintf(&b, "⏰ 北京时间 %s", pm.KickoffCN)
		if pm.Venue != "" {
			fmt.Fprintf(&b, " | %s（%s）", pm.Venue, pm.City)
		}
		if pm.HomeForm != "" || pm.AwayForm != "" {
			fmt.Fprintf(&b, "\n📈 近况：%s %s / %s %s", cnmap.Name(pm.Home), formCN(pm.HomeForm), cnmap.Name(pm.Away), formCN(pm.AwayForm))
		}
		var h2hTeams []espn.H2HTeam
		if len(pm.H2H) > 0 {
			h2hTeams = []espn.H2HTeam{{Team: espn.Team{DisplayName: pm.Home}, Events: pm.H2H}}
		}
		if line := h2hLine(h2hTeams); line != "" {
			b.WriteString("\n" + line)
		}
	}
	return b.String()
}

// formCN turns "WWDLW" into "胜胜平负胜".
func formCN(form string) string {
	if form == "" {
		return "—"
	}
	var b strings.Builder
	for _, r := range form {
		switch r {
		case 'W':
			b.WriteString("胜")
		case 'D':
			b.WriteString("平")
		case 'L':
			b.WriteString("负")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (d *Digest) previewHighlights(ctx context.Context, date string, matches []previewMatch, standings []espn.GroupStanding, boards *Boards) string {
	if d.llm == nil {
		return ""
	}
	payload, err := json.MarshalIndent(map[string]any{
		"date": date, "matches": matches,
		"all_group_standings": standings, "leaderboards": boards,
	}, "", " ")
	if err != nil {
		return ""
	}
	out, err := d.llm.Generate(ctx, previewSystemPrompt, string(payload))
	if err != nil {
		d.logger.Error("llm preview highlights failed, degrading", "error", err)
		d.alert("llm", fmt.Sprintf("看点生成失败（预告照发）: %v", err))
		return ""
	}
	if err := d.store.SaveJSON(date, "preview-highlights", map[string]string{"text": out}); err != nil {
		d.logger.Error("persist highlights failed", "error", err)
	}
	return strings.TrimSpace(out)
}

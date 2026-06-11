package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"worldcup-broadcaster/internal/cnmap"
	"worldcup-broadcaster/internal/espn"
)

type recapGoal struct {
	Clock   string `json:"clock"`
	Team    string `json:"team"`
	Player  string `json:"player"`
	Assist  string `json:"assist,omitempty"`
	Penalty bool   `json:"penalty,omitempty"`
	OwnGoal bool   `json:"own_goal,omitempty"`
}

type recapMatch struct {
	ID        string          `json:"id"`
	Stage     string          `json:"stage"`
	Home      string          `json:"home"`
	Away      string          `json:"away"`
	HomeScore string          `json:"home_score"`
	AwayScore string          `json:"away_score"`
	Detail    string          `json:"status_detail"` // FT / AET / FT-Pens
	SoHome    int             `json:"shootout_home,omitempty"`
	SoAway    int             `json:"shootout_away,omitempty"`
	Goals     []recapGoal     `json:"goals,omitempty"`
	RedCards  []string        `json:"red_cards,omitempty"`
	Finished  bool            `json:"finished"`
	Standings json.RawMessage `json:"group_standings,omitempty"`
}

const recapSystemPrompt = `你是一位风趣幽默的中文足球解说员。用户会给你今日世界杯已结束比赛的JSON战报` +
	`（含完整积分榜和射手/助攻榜）。请写一段150-250字的整体锐评：点出最大冷门/最精彩比赛，` +
	`并结合积分榜明确说出出线形势变化（谁基本稳了、谁命悬一线、谁提前回家）。` +
	`2026世界杯规则：48队12组，每组前2直接晋级32强，8个成绩最好的小组第三也晋级。` +
	`语言活泼带梗，可以玩"今晚几个"等足球圈梗。直接输出纯文本，不要markdown。`

// Recap summarizes all matches kicked off on the given China date
// (normally today: the matches that finished this morning).
func (d *Digest) Recap(ctx context.Context, date string) error {
	events, err := d.espn.MatchesOnChinaDate(ctx, date)
	if err != nil {
		d.alert("recap", fmt.Sprintf("获取 %s 赛果失败: %v", date, err))
		return fmt.Errorf("recap %s: %w", date, err)
	}
	if len(events) == 0 {
		d.logger.Info("no matches to recap", "date", date)
		return nil
	}

	matches := make([]recapMatch, 0, len(events))
	for i := range events {
		ev := &events[i]
		home, away := homeAway(ev)
		rm := recapMatch{
			ID: ev.ID, Stage: StageName(ev.Season.Slug),
			Home: home.Team.DisplayName, Away: away.Team.DisplayName,
			HomeScore: home.Score, AwayScore: away.Score,
			Detail:   ev.Status.Type.Detail,
			Finished: ev.Status.Type.State == "post",
		}
		if rm.Finished {
			if sum, _, err := d.espn.Summary(ctx, ev.ID); err != nil {
				d.logger.Error("recap summary fetch failed", "match", ev.ID, "error", err)
			} else {
				fillRecapDetails(&rm, sum)
			}
		}
		matches = append(matches, rm)
	}

	if err := d.store.SaveJSON(date, "recap", matches); err != nil {
		d.logger.Error("persist recap failed", "error", err)
	}

	standings, boards := d.tablesAndBoards(ctx, date)

	text := d.renderRecap(date, matches)
	teams := map[string]bool{}
	for _, rm := range matches {
		teams[rm.Home], teams[rm.Away] = true, true
	}
	if section := renderStandingsSection(standings, teams); section != "" {
		text += blockSep + section
	}
	if section := renderBoards(boards); section != "" {
		text += blockSep + section
	}
	if comment := d.recapComment(ctx, date, matches, standings, boards); comment != "" {
		text += blockSep + "🎙️ 今日锐评（DeepSeek 说的，跟我无关）\n" + comment
	}
	d.sendSplit(text)
	d.logger.Info("recap broadcast", "date", date, "matches", len(matches))
	return nil
}

func fillRecapDetails(rm *recapMatch, sum *espn.Summary) {
	_, home, away := sum.Live()
	rm.SoHome, rm.SoAway = int(home.ShootoutScore), int(away.ShootoutScore)
	if home.Score != "" {
		rm.HomeScore, rm.AwayScore = home.Score, away.Score
	}
	rm.Standings = sum.Standings
	for i := range sum.KeyEvents {
		k := &sum.KeyEvents[i]
		if k.Shootout {
			continue
		}
		text := k.Type.Text
		switch {
		case k.ScoringPlay || text == "Goal" || text == "Penalty - Scored" || strings.Contains(text, "Own Goal"):
			g := recapGoal{
				Clock:   k.Clock.DisplayValue,
				Team:    k.Team.DisplayName,
				Penalty: text == "Penalty - Scored",
				OwnGoal: strings.Contains(text, "Own Goal"),
			}
			if len(k.Participants) > 0 {
				g.Player = k.Participants[0].Athlete.DisplayName
			}
			if !g.Penalty && !g.OwnGoal && len(k.Participants) > 1 {
				g.Assist = k.Participants[1].Athlete.DisplayName
			}
			rm.Goals = append(rm.Goals, g)
		case strings.Contains(text, "Red Card"):
			if len(k.Participants) > 0 {
				rm.RedCards = append(rm.RedCards,
					fmt.Sprintf("%s（%s，%s）", k.Participants[0].Athlete.DisplayName, cnmap.Name(k.Team.DisplayName), k.Clock.DisplayValue))
			}
		}
	}
}

func (d *Digest) renderRecap(date string, matches []recapMatch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "☀️ 下午好！%s 的战报新鲜出炉～\n🏆 2026世界杯 当日赛果（%d场）", displayDate(date), len(matches))
	for _, rm := range matches {
		b.WriteString(blockSep)
		score := fmt.Sprintf("%s %s : %s %s", cnmap.Full(rm.Home), rm.HomeScore, rm.AwayScore, cnmap.Name(rm.Away)+cnmap.Flag(rm.Away))
		switch {
		case !rm.Finished:
			fmt.Fprintf(&b, "⏳ %s（%s）\n%s vs %s — 比赛尚未结束/未开始", rm.Stage, rm.Detail, cnmap.Full(rm.Home), cnmap.Name(rm.Away)+cnmap.Flag(rm.Away))
			continue
		case rm.SoHome > 0 || rm.SoAway > 0:
			fmt.Fprintf(&b, "🏁 %s\n%s（点球 %d:%d）", rm.Stage, score, rm.SoHome, rm.SoAway)
		case strings.Contains(rm.Detail, "AET"):
			fmt.Fprintf(&b, "🏁 %s\n%s（加时）", rm.Stage, score)
		default:
			fmt.Fprintf(&b, "🏁 %s\n%s", rm.Stage, score)
		}
		for _, g := range rm.Goals {
			tag := ""
			if g.Penalty {
				tag = "（点球）"
			} else if g.OwnGoal {
				tag = "（乌龙）"
			}
			fmt.Fprintf(&b, "\n⚽ %s %s%s（%s）", g.Clock, g.Player, tag, cnmap.Name(g.Team))
			if g.Assist != "" {
				fmt.Fprintf(&b, " 助攻:%s", g.Assist)
			}
		}
		for _, rc := range rm.RedCards {
			fmt.Fprintf(&b, "\n🟥 %s", rc)
		}
	}
	return b.String()
}

func (d *Digest) recapComment(ctx context.Context, date string, matches []recapMatch, standings []espn.GroupStanding, boards *Boards) string {
	if d.llm == nil {
		return ""
	}
	finished := 0
	for _, m := range matches {
		if m.Finished {
			finished++
		}
	}
	if finished == 0 {
		return ""
	}
	payload, err := json.MarshalIndent(map[string]any{
		"date": date, "matches": matches,
		"all_group_standings": standings, "leaderboards": boards,
	}, "", " ")
	if err != nil {
		return ""
	}
	out, err := d.llm.Generate(ctx, recapSystemPrompt, string(payload))
	if err != nil {
		d.logger.Error("llm recap comment failed, degrading", "error", err)
		d.alert("llm", fmt.Sprintf("锐评生成失败（战报照发）: %v", err))
		return ""
	}
	if err := d.store.SaveJSON(date, "recap-comment", map[string]string{"text": out}); err != nil {
		d.logger.Error("persist recap comment failed", "error", err)
	}
	return strings.TrimSpace(out)
}

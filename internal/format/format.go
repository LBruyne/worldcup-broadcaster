// Package format renders all QQ messages in the bot's signature flashy
// style. Team names are translated to Chinese with flag emojis.
package format

import (
	"fmt"
	"strings"

	"worldcup-broadcaster/internal/cnmap"
)

// MatchCtx carries the current scoreline for event messages.
// Names are ESPN displayNames; translation happens here.
type MatchCtx struct {
	HomeName, AwayName   string
	HomeScore, AwayScore string
}

// mirrored layout: flag-name on the left, name-flag on the right.
func awayFull(name string) string {
	return cnmap.Name(name) + cnmap.Flag(name)
}

func (m MatchCtx) scoreline() string {
	return fmt.Sprintf("%s %s : %s %s",
		cnmap.Full(m.HomeName), m.HomeScore, m.AwayScore, awayFull(m.AwayName))
}

func (m MatchCtx) versus() string {
	return fmt.Sprintf("%s vs %s", cnmap.Full(m.HomeName), awayFull(m.AwayName))
}

func Kickoff(m MatchCtx) string {
	return fmt.Sprintf("🔥 比赛开始！\n⚽ %s\n搬好小板凳，发车了发车了～🚌", m.versus())
}

// Goal renders goals, penalties (in regulation) and own goals.
func Goal(m MatchCtx, scoringTeam, scorer, assist, clock string, penalty, ownGoal bool) string {
	var b strings.Builder
	switch {
	case ownGoal:
		b.WriteString("😱 乌龙球！这球进自家门了！\n")
	case penalty:
		b.WriteString("⚽💥 点球命中！稳稳推射！\n")
	default:
		b.WriteString("⚽💥 GOOOOOAL!!! Siuuuu~~\n")
	}
	fmt.Fprintf(&b, "%s (%s)\n", m.scoreline(), clock)
	team := cnmap.Name(scoringTeam)
	switch {
	case ownGoal:
		fmt.Fprintf(&b, "💀 %s 不慎自摆乌龙（%s 笑纳大礼）", scorer, team)
	case penalty:
		fmt.Fprintf(&b, "🎯 %s 主罚命中！（%s）", scorer, team)
	default:
		fmt.Fprintf(&b, "🎯 %s 破门！（%s）", scorer, team)
		if assist != "" {
			fmt.Fprintf(&b, "\n🅰️ %s 送出助攻", assist)
		}
	}
	return b.String()
}

func PenaltyMissed(m MatchCtx, team, player, clock string) string {
	return fmt.Sprintf("🙈 点球不进！%s（%s）的点球被化解/射失！(%s)\n%s\n门将封神还是射手拉胯？",
		player, cnmap.Name(team), clock, m.scoreline())
}

func YellowCard(m MatchCtx, team, player, clock string) string {
	return fmt.Sprintf("🟨 黄牌警告 (%s)\n%s（%s）吃到一张黄牌，下脚注意点啊兄弟～", clock, player, cnmap.Name(team))
}

func RedCard(m MatchCtx, team, player, clock string) string {
	return fmt.Sprintf("🟥 红牌！！直接洗澡！(%s)\n%s（%s）被罚下场，%s 十人应战！🚿",
		clock, player, cnmap.Name(team), cnmap.Name(team))
}

func Substitution(m MatchCtx, team, playerIn, playerOut, clock string) string {
	return fmt.Sprintf("🔄 换人调整（%s，%s）\n⬆️ %s 替下 ⬇️ %s", cnmap.Name(team), clock, playerIn, playerOut)
}

func Halftime(m MatchCtx) string {
	return fmt.Sprintf("⏸️ 中场休息\n%s\n上半场战罢，先去倒杯快乐水 🥤", m.scoreline())
}

func SecondHalfStart(m MatchCtx) string {
	return fmt.Sprintf("▶️ 下半场开始！\n%s\n好戏还在后头，别走开～", m.scoreline())
}

func EndRegularTime(m MatchCtx) string {
	return fmt.Sprintf("⏱️ 90分钟战罢，难解难分！\n%s\n要进加时了，刺激⚡", m.scoreline())
}

func StartExtraTime(m MatchCtx) string {
	return fmt.Sprintf("⚡ 加时赛开始！\n%s\n30分钟定生死，顶住！", m.scoreline())
}

func HalftimeExtraTime(m MatchCtx) string {
	return fmt.Sprintf("⏸️ 加时赛中场\n%s\n最后15分钟，谁能绝杀？", m.scoreline())
}

func StartSecondHalfExtraTime(m MatchCtx) string {
	return fmt.Sprintf("⚡ 加时下半场开始！\n%s\n窒息时刻，心脏不好的捂眼睛 🫣", m.scoreline())
}

func EndExtraTime(m MatchCtx) string {
	return fmt.Sprintf("⏱️ 加时赛结束！\n%s\n点球大战走起，门将的舞台到了 🥅", m.scoreline())
}

func StartShootout(m MatchCtx) string {
	return fmt.Sprintf("🥅 点球大战开始！！！\n%s\n一轮一轮来，大家把心提到嗓子眼～", m.versus())
}

// ShootoutRound renders one penalty attempt with the running shootout score.
func ShootoutRound(m MatchCtx, team, player string, round int, scored bool, soHome, soAway int) string {
	result := "罚进 ✅"
	if !scored {
		result = "不进 ❌"
	}
	return fmt.Sprintf("🥅 点球大战 第%d轮 | %s（%s）%s\n当前点球比分 %s %d - %d %s",
		round, player, cnmap.Name(team), result,
		cnmap.Name(m.HomeName), soHome, soAway, cnmap.Name(m.AwayName))
}

// Fulltime renders the final whistle. detail is ESPN's status detail
// ("FT", "AET", "FT-Pens"); shootout scores are shown when present.
func Fulltime(m MatchCtx, detail string, soHome, soAway int) string {
	var b strings.Builder
	b.WriteString("🔚 全场结束！\n")
	fmt.Fprintf(&b, "%s", m.scoreline())
	switch {
	case strings.Contains(detail, "Pens") || soHome > 0 || soAway > 0:
		fmt.Fprintf(&b, "（点球 %d:%d）\n", soHome, soAway)
		winner := m.HomeName
		if soAway > soHome {
			winner = m.AwayName
		}
		fmt.Fprintf(&b, "🎉 %s 点球大战胜出，心脏派比赛！", cnmap.Full(winner))
	case strings.Contains(detail, "AET"):
		b.WriteString("（加时赛后）\n⚡ 加时分出胜负，看球一秒都不能走开！")
	default:
		b.WriteString("\n")
	}
	b.WriteString("\n今晚几个？🤔")
	return b.String()
}

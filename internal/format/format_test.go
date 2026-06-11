package format

import (
	"strings"
	"testing"
)

var final = MatchCtx{
	HomeName: "Argentina", AwayName: "France",
	HomeScore: "2", AwayScore: "0",
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("message missing %q:\n%s", w, got)
		}
	}
}

func TestGoal(t *testing.T) {
	got := Goal(final, "Argentina", "Ángel Di María", "Alexis Mac Allister", "36'", false, false)
	mustContain(t, got, "GOOOOOAL", "Siuuu", "🇦🇷阿根廷 2 : 0 法国🇫🇷", "36'",
		"Ángel Di María 破门", "阿根廷", "Alexis Mac Allister 送出助攻")
}

func TestPenaltyGoalNoAssist(t *testing.T) {
	got := Goal(MatchCtx{HomeName: "Argentina", AwayName: "France", HomeScore: "1", AwayScore: "0"},
		"Argentina", "Lionel Messi", "", "23'", true, false)
	mustContain(t, got, "点球命中", "Lionel Messi 主罚命中")
	if strings.Contains(got, "助攻") {
		t.Error("penalty should not mention assist")
	}
}

func TestOwnGoal(t *testing.T) {
	got := Goal(final, "Argentina", "Some Defender", "", "55'", false, true)
	mustContain(t, got, "乌龙", "Some Defender 不慎自摆乌龙")
}

func TestCards(t *testing.T) {
	mustContain(t, YellowCard(final, "France", "Adrien Rabiot", "55'"), "🟨", "Adrien Rabiot", "法国", "55'")
	mustContain(t, RedCard(final, "France", "Marcus Thuram", "88'"), "🟥", "十人应战", "Marcus Thuram")
}

func TestPhases(t *testing.T) {
	mustContain(t, Kickoff(final), "比赛开始", "🇦🇷阿根廷 vs 法国🇫🇷")
	mustContain(t, Halftime(final), "中场休息", "2 : 0")
	mustContain(t, SecondHalfStart(final), "下半场开始", "2 : 0")
	mustContain(t, EndRegularTime(final), "90分钟", "加时")
	mustContain(t, StartExtraTime(final), "加时赛开始")
	mustContain(t, EndExtraTime(final), "点球大战")
	mustContain(t, StartShootout(final), "点球大战开始")
}

func TestSubstitution(t *testing.T) {
	got := Substitution(final, "France", "Randal Kolo Muani", "Ousmane Dembélé", "41'")
	mustContain(t, got, "🔄", "⬆️ Randal Kolo Muani", "⬇️ Ousmane Dembélé", "法国")
}

func TestShootoutRound(t *testing.T) {
	got := ShootoutRound(final, "France", "Kylian Mbappé", 1, true, 0, 1)
	mustContain(t, got, "第1轮", "Kylian Mbappé", "罚进 ✅", "阿根廷 0 - 1 法国")

	miss := ShootoutRound(final, "France", "Kingsley Coman", 2, false, 1, 1)
	mustContain(t, miss, "不进 ❌")
}

func TestFulltimePlain(t *testing.T) {
	got := Fulltime(MatchCtx{HomeName: "Mexico", AwayName: "South Africa", HomeScore: "2", AwayScore: "1"}, "FT", 0, 0)
	mustContain(t, got, "全场结束", "🇲🇽墨西哥 2 : 1 南非🇿🇦", "今晚几个？🤔")
	if strings.Contains(got, "点球") {
		t.Error("plain FT should not mention shootout")
	}
}

func TestFulltimePens(t *testing.T) {
	got := Fulltime(MatchCtx{HomeName: "Argentina", AwayName: "France", HomeScore: "3", AwayScore: "3"}, "FT-Pens", 4, 2)
	mustContain(t, got, "点球 4:2", "🇦🇷阿根廷 点球大战胜出", "今晚几个？🤔")
}

func TestFulltimeAET(t *testing.T) {
	got := Fulltime(MatchCtx{HomeName: "Argentina", AwayName: "France", HomeScore: "1", AwayScore: "0"}, "AET", 0, 0)
	mustContain(t, got, "加时")
}

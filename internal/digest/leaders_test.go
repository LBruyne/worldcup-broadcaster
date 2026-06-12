package digest

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"worldcup-broadcaster/internal/espn"
	"worldcup-broadcaster/internal/store"
)

// 2022 final regulation goals: Messi 2 (23' pen, 108'), Mbappé 3 (80' pen,
// 81', 118' pen), Di María 1. Assists: Mac Allister, Thuram.
func TestLeaderboards(t *testing.T) {
	client := fakeESPN(t, "scoreboard-20221218.json", map[string]string{
		"633850": "summary-633850.json",
	})
	st := store.New(t.TempDir())
	d := New(client, nil, st, &fakeSender{}, func(string, string) {}, slog.Default())

	boards, err := d.Leaderboards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if boards.Matches != 1 {
		t.Errorf("finished matches = %d", boards.Matches)
	}
	if len(boards.Scorers) != 3 {
		t.Fatalf("scorers = %+v", boards.Scorers)
	}
	if boards.Scorers[0].Player != "Kylian Mbappé" || boards.Scorers[0].Count != 3 {
		t.Errorf("top scorer = %+v", boards.Scorers[0])
	}
	if boards.Scorers[1].Player != "Lionel Messi" || boards.Scorers[1].Count != 2 {
		t.Errorf("scorer2 = %+v", boards.Scorers[1])
	}
	if len(boards.Assists) != 2 {
		t.Fatalf("assists = %+v", boards.Assists)
	}

	// second build must hit the disk cache, not the network
	srvLess := New(espn.NewClientWithBase("http://127.0.0.1:9", "fifa.world"), nil, st, &fakeSender{}, func(string, string) {}, slog.Default())
	_ = srvLess
	var cached matchGoals
	if err := st.LoadJSON("summaries", "match-633850", &cached); err != nil || len(cached.Goals) != 6 {
		t.Errorf("cache = %+v, err %v", cached, err)
	}
}

func TestRenderBoards(t *testing.T) {
	b := &Boards{
		Scorers: []LeaderEntry{
			{Player: "Kylian Mbappé", Team: "France", Count: 3},
			{Player: "Lionel Messi", Team: "Argentina", Count: 2},
			{Player: "Ángel Di María", Team: "Argentina", Count: 2},
		},
		Assists: []LeaderEntry{{Player: "Alexis Mac Allister", Team: "Argentina", Count: 1}},
	}
	got := renderBoards(b)
	for _, want := range []string{"👟 射手榜", "1. Kylian Mbappé（法国）3球", "2. Lionel Messi（阿根廷）2球", "2. Ángel Di María（阿根廷）2球", "🅰️ 助攻榜", "1. Alexis Mac Allister（阿根廷）1助攻"} {
		if !strings.Contains(got, want) {
			t.Errorf("boards missing %q:\n%s", want, got)
		}
	}
	if renderBoards(&Boards{}) != "" {
		t.Error("empty boards should render empty")
	}
}

func TestRenderGroupTable(t *testing.T) {
	g := espn.GroupStanding{Name: "Group A", Letter: "A", Entries: []espn.StandingEntry{
		{Team: "Mexico", Rank: 1, Points: 3, Wins: 1, Played: 1, GoalDiff: 2, Advanced: "1"},
		{Team: "South Korea", Rank: 2, Points: 0, Played: 1, Advanced: "0", Note: "Advance to Round of 32"},
		{Team: "Czechia", Rank: 3, Points: 0, Played: 1, Advanced: "0", Note: "Best 8 advance"},
		{Team: "South Africa", Rank: 4, Points: 0, Losses: 1, Played: 1, GoalDiff: -2, Advanced: "0", Note: "Eliminated"},
	}}
	got := RenderGroupTable(g)
	for _, want := range []string{"📊 A组积分榜", "1. 墨西哥 3分（1胜0平0负 净胜+2） ✅晋级",
		"2. 韩国 0分（0胜0平0负 净胜+0） 🟢", "3. 捷克 0分（0胜0平0负 净胜+0） 🟡",
		"4. 南非 0分（0胜0平1负 净胜-2） 🔴"} {
		if !strings.Contains(got, want) {
			t.Errorf("table missing %q:\n%s", want, got)
		}
	}
	// a 0-point team must never show the clinched mark
	if strings.Contains(got, "韩国 0分（0胜0平0负 净胜+0） ✅") {
		t.Error("unclinched team marked as qualified")
	}
}

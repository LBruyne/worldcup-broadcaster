package cnmap

import (
	"encoding/json"
	"os"
	"testing"
)

func TestKnownTeams(t *testing.T) {
	cases := map[string]string{
		"Mexico":        "墨西哥",
		"South Korea":   "韩国",
		"Türkiye":       "土耳其",
		"Curaçao":       "库拉索",
		"United States": "美国",
	}
	for in, want := range cases {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
	if Flag("Mexico") != "🇲🇽" {
		t.Errorf("Flag(Mexico) = %q", Flag("Mexico"))
	}
	if Full("Mexico") != "🇲🇽墨西哥" {
		t.Errorf("Full(Mexico) = %q", Full("Mexico"))
	}
}

func TestUnknownFallsBack(t *testing.T) {
	if got := Name("Atlantis"); got != "Atlantis" {
		t.Errorf("unknown team = %q", got)
	}
	if Flag("Atlantis") != "" {
		t.Error("unknown flag should be empty")
	}
}

func TestPlaceholders(t *testing.T) {
	cases := map[string]string{
		"Group A Winner":             "A组第一",
		"Group L 2nd Place":          "L组第二",
		"Round of 32 14 Winner":      "32强赛第14场胜者",
		"Quarterfinal 2 Winner":      "1/4决赛第2场胜者",
		"Semifinal 1 Loser":          "半决赛第1场负者",
		"Third Place Group A/B/C/D/F": "小组第三（A/B/C/D/F组之一）",
	}
	for in, want := range cases {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every real team in the full 2026 schedule fixture must have a Chinese
// mapping — placeholders excluded by checking the teams map directly.
func TestAllFixtureTeamsMapped(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/scoreboard-full.json")
	if err != nil {
		t.Skip("fixture not present")
	}
	var sb struct {
		Events []struct {
			Competitions []struct {
				Competitors []struct {
					Team struct {
						DisplayName string `json:"displayName"`
					} `json:"team"`
				} `json:"competitors"`
			} `json:"competitions"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &sb); err != nil {
		t.Fatal(err)
	}
	missing := map[string]bool{}
	for _, e := range sb.Events {
		for _, c := range e.Competitions[0].Competitors {
			name := c.Team.DisplayName
			if _, ok := teams[name]; ok {
				continue
			}
			if Name(name) == name { // neither team map nor placeholder matched
				missing[name] = true
			}
		}
	}
	for name := range missing {
		t.Errorf("unmapped team name: %q", name)
	}
}

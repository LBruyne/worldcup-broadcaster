package espn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func loadFixture(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
}

func TestParseSummaryFixture(t *testing.T) {
	var s Summary
	loadFixture(t, "summary-633850.json", &s)

	st, home, away := s.Live()
	if st.Type.Detail != "FT-Pens" || st.Type.State != "post" {
		t.Errorf("status = %+v", st.Type)
	}
	if home.Team.DisplayName != "Argentina" || home.Score != "3" || home.ShootoutScore != 4 {
		t.Errorf("home = %+v", home)
	}
	if away.Team.DisplayName != "France" || away.ShootoutScore != 2 {
		t.Errorf("away = %+v", away)
	}
	if len(s.KeyEvents) != 37 {
		t.Errorf("keyEvents = %d, want 37", len(s.KeyEvents))
	}

	var goal *KeyEvent
	for i := range s.KeyEvents {
		if s.KeyEvents[i].Type.Text == "Goal" && s.KeyEvents[i].Clock.DisplayValue == "36'" {
			goal = &s.KeyEvents[i]
		}
	}
	if goal == nil {
		t.Fatal("36' goal not found")
	}
	if goal.Participants[0].Athlete.DisplayName != "Ángel Di María" ||
		goal.Participants[1].Athlete.DisplayName != "Alexis Mac Allister" {
		t.Errorf("goal participants = %+v", goal.Participants)
	}
	if !goal.ScoringPlay || goal.Team.DisplayName != "Argentina" {
		t.Errorf("goal flags = %+v", goal)
	}

	if len(s.Shootout) != 2 {
		t.Fatalf("shootout teams = %d", len(s.Shootout))
	}
	var fr ShootoutTeam
	for _, so := range s.Shootout {
		if so.Team == "France" {
			fr = so
		}
	}
	if len(fr.Shots) != 4 || fr.Shots[0].Player != "Kylian Mbappé" || !fr.Shots[0].DidScore || fr.Shots[1].DidScore {
		t.Errorf("france shootout = %+v", fr.Shots)
	}

	if len(s.HeadToHeadGames) == 0 || len(s.HeadToHeadGames[0].Events) == 0 {
		t.Fatal("h2h missing")
	}
	if s.GameInfo.Venue.FullName != "Lusail Stadium" {
		t.Errorf("venue = %q", s.GameInfo.Venue.FullName)
	}
}

func TestParseScoreboardFixture(t *testing.T) {
	var sb Scoreboard
	loadFixture(t, "scoreboard-upcoming.json", &sb)
	if len(sb.Events) != 7 {
		t.Fatalf("events = %d, want 7", len(sb.Events))
	}
	e := sb.Events[0]
	if e.ID != "760415" || e.Season.Slug != "group-stage" {
		t.Errorf("event = %+v", e)
	}
	ko, err := e.Kickoff()
	if err != nil {
		t.Fatal(err)
	}
	if ko.UTC().Format("2006-01-02 15:04") != "2026-06-11 19:00" {
		t.Errorf("kickoff = %v", ko)
	}
	if e.Competitions[0].Venue.Address.City != "Mexico City" {
		t.Errorf("venue = %+v", e.Competitions[0].Venue)
	}
	var home Competitor
	for _, c := range e.Competitions[0].Competitors {
		if c.HomeAway == "home" {
			home = c
		}
	}
	if home.Team.DisplayName != "Mexico" || home.Form == "" {
		t.Errorf("home = %+v", home)
	}
}

// China date 2026-06-12 covers 760415 (Jun 11 19:00Z = 03:00 CST) and
// 760414 (Jun 12 02:00Z = 10:00 CST), but NOT 760416 (Jun 12 19:00Z =
// 03:00 CST Jun 13).
func TestFilterChinaDate(t *testing.T) {
	var sb Scoreboard
	loadFixture(t, "scoreboard-upcoming.json", &sb)
	got, err := FilterChinaDate(sb.Events, "2026-06-12")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if len(ids) != 2 || ids[0] != "760415" || ids[1] != "760414" {
		t.Errorf("china-day ids = %v, want [760415 760414]", ids)
	}

	got13, err := FilterChinaDate(sb.Events, "2026-06-13")
	if err != nil {
		t.Fatal(err)
	}
	if len(got13) != 2 { // 760416 (Jun13 03:00 CST), 760417 (Jun13 09:00 CST)
		t.Errorf("jun13 count = %d, want 2", len(got13))
	}
}

func TestClientRetriesAndFakeServer(t *testing.T) {
	var calls atomic.Int32
	raw, err := os.ReadFile("../../testdata/scoreboard-upcoming.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway) // first attempt fails
			return
		}
		if !strings.Contains(r.URL.Path, "/fifa.world/scoreboard") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write(raw)
	}))
	defer srv.Close()

	c := NewClientWithBase(srv.URL, "fifa.world")
	sb, err := c.Scoreboard(context.Background(), "20260611-20260613")
	if err != nil {
		t.Fatalf("Scoreboard: %v", err)
	}
	if len(sb.Events) != 7 {
		t.Errorf("events = %d", len(sb.Events))
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", calls.Load())
	}
}

func TestParseAthleteStats(t *testing.T) {
	body, err := os.ReadFile("../../testdata/athlete-stats-haaland.json")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := parseAthleteStats(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	r0 := rows[0]
	if r0.Season != "2025-26 English Premier League" || r0.Team != "Manchester City" {
		t.Errorf("row0 = %s / %s", r0.Season, r0.Team)
	}
	if r0.Stats["G"] != "27" || r0.Stats["A"] != "8" {
		t.Errorf("row0 stats = %v", r0.Stats)
	}
}

// Search hits sometimes carry a GUID in the id field; the numeric athlete
// id must come from the uid ("s:600~a:253989").
func TestParseAthleteSearchNumericID(t *testing.T) {
	body := []byte(`{"results":[{"type":"player","contents":[
		{"id":"d41614d6-11bc-0e9b","uid":"s:600~a:424204","displayName":"Alphonso Davies","description":"Bayern Munich"},
		{"id":"253989","uid":"s:600~a:253989","displayName":"Erling Haaland","description":"Premier League"},
		{"id":"999","uid":"s:40~l:41~a:999","displayName":"Some NBA Guy","description":"NBA"}]}]}`)
	got, err := parseAthleteSearch(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("athletes = %d, want 2 (non-soccer filtered)", len(got))
	}
	if got[0].ID != "424204" || got[1].ID != "253989" {
		t.Errorf("ids = %s, %s", got[0].ID, got[1].ID)
	}
}

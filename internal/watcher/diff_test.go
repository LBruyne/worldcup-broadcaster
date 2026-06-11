package watcher

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"worldcup-broadcaster/internal/espn"
)

func loadFinal(t *testing.T) *espn.Summary {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/summary-633850.json")
	if err != nil {
		t.Fatal(err)
	}
	var s espn.Summary
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func neverSeen(string) bool { return false }

func TestDiffFullMatch(t *testing.T) {
	sum := loadFinal(t)
	events := Diff(sum, neverSeen)

	// 37 key events + 8 shootout attempts
	if len(events) != 45 {
		t.Fatalf("events = %d, want 45", len(events))
	}
	if events[0].Type != EvKickoff {
		t.Errorf("first = %v, want kickoff", events[0].Type)
	}
	if last := events[len(events)-1]; last.Type != EvEndMatch {
		t.Errorf("last = %v, want end match", last.Type)
	} else {
		if last.Detail != "FT-Pens" || last.SoHome != 4 || last.SoAway != 2 {
			t.Errorf("end match = %+v", last)
		}
	}

	counts := map[EventType]int{}
	for _, e := range events {
		counts[e.Type]++
	}
	want := map[EventType]int{
		EvKickoff: 1, EvGoal: 6, EvSubstitution: 13, EvYellowCard: 8,
		EvHalftime: 1, EvSecondHalfStart: 1, EvEndRegularTime: 1,
		EvStartExtraTime: 1, EvHalftimeExtraTime: 1, EvStartSecondHalfExtraTime: 1,
		EvEndExtraTime: 1, EvStartShootout: 1, EvShootoutRound: 8, EvEndMatch: 1,
	}
	for ty, n := range want {
		if counts[ty] != n {
			t.Errorf("count[%v] = %d, want %d", ty, counts[ty], n)
		}
	}
}

func TestDiffGoalDetails(t *testing.T) {
	events := Diff(loadFinal(t), neverSeen)

	var messiPen, diMaria Event
	for _, e := range events {
		if e.Type != EvGoal {
			continue
		}
		switch {
		case e.Player == "Lionel Messi" && e.Clock == "23'":
			messiPen = e
		case e.Player == "Ángel Di María":
			diMaria = e
		}
	}
	if !messiPen.Penalty || messiPen.Team != "Argentina" {
		t.Errorf("messi pen = %+v", messiPen)
	}
	// Score parsed from the event text, not the final 3:3 snapshot.
	if messiPen.Ctx.HomeScore != "1" || messiPen.Ctx.AwayScore != "0" {
		t.Errorf("messi pen score = %s:%s, want 1:0", messiPen.Ctx.HomeScore, messiPen.Ctx.AwayScore)
	}
	if diMaria.Assist != "Alexis Mac Allister" {
		t.Errorf("di maria assist = %q", diMaria.Assist)
	}
	if diMaria.Ctx.HomeScore != "2" || diMaria.Ctx.AwayScore != "0" {
		t.Errorf("di maria score = %s:%s, want 2:0", diMaria.Ctx.HomeScore, diMaria.Ctx.AwayScore)
	}
}

func TestDiffShootoutRounds(t *testing.T) {
	events := Diff(loadFinal(t), neverSeen)
	var rounds []Event
	for _, e := range events {
		if e.Type == EvShootoutRound {
			rounds = append(rounds, e)
		}
	}
	if len(rounds) != 8 {
		t.Fatalf("rounds = %d", len(rounds))
	}
	// Wall-clock order: Mbappé (France, away) scores first.
	r0 := rounds[0]
	if r0.Player != "Kylian Mbappé" || !r0.Scored || r0.Round != 1 || r0.SoHome != 0 || r0.SoAway != 1 {
		t.Errorf("round0 = %+v", r0)
	}
	// Coman (3rd attempt overall, after Mbappé and Messi) misses: 1-1 holds.
	r2 := rounds[2]
	if r2.Player != "Kingsley Coman" || r2.Scored || r2.SoHome != 1 || r2.SoAway != 1 {
		t.Errorf("round2 = %+v", r2)
	}
	last := rounds[7]
	if last.SoHome != 4 || last.SoAway != 2 {
		t.Errorf("final shootout = %d-%d, want 4-2", last.SoHome, last.SoAway)
	}
}

func TestDiffDedup(t *testing.T) {
	sum := loadFinal(t)
	seen := map[string]bool{}
	first := Diff(sum, func(k string) bool { return seen[k] })
	for _, e := range first {
		seen[e.Key] = true
	}
	second := Diff(sum, func(k string) bool { return seen[k] })
	if len(second) != 0 {
		t.Errorf("second diff = %d events, want 0", len(second))
	}
}

func TestRenderSubstitutionDirection(t *testing.T) {
	events := Diff(loadFinal(t), neverSeen)
	for _, e := range events {
		if e.Type == EvSubstitution && e.Player == "Randal Kolo Muani" {
			msg := Render(e)
			if !strings.Contains(msg, "⬆️ Randal Kolo Muani") || !strings.Contains(msg, "⬇️ Ousmane Dembélé") {
				t.Errorf("sub direction wrong:\n%s", msg)
			}
			return
		}
	}
	t.Fatal("kolo muani substitution not found")
}

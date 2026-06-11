package espn

import (
	"os"
	"testing"
)

func TestParseStandingsFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/standings-full.json")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := ParseStandings(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 12 {
		t.Fatalf("groups = %d, want 12", len(groups))
	}
	if groups[0].Name != "Group A" || groups[0].Letter != "A" {
		t.Errorf("group0 = %+v", groups[0])
	}
	if len(groups[0].Entries) != 4 {
		t.Fatalf("group A entries = %d", len(groups[0].Entries))
	}
	// entries must come out rank-sorted
	for i, e := range groups[0].Entries {
		if e.Rank != i+1 {
			t.Errorf("entry %d rank = %d", i, e.Rank)
		}
		if e.Team == "" {
			t.Errorf("entry %d has empty team", i)
		}
	}
	// Mexico is in group A in the 2026 draw
	found := false
	for _, e := range groups[0].Entries {
		if e.Team == "Mexico" {
			found = true
		}
	}
	if !found {
		t.Error("Mexico not in group A")
	}
}

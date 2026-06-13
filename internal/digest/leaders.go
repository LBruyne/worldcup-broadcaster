package digest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"worldcup-broadcaster/internal/cnmap"
	"worldcup-broadcaster/internal/espn"
)

// tournamentStartUTC is the first UTC day of the 2026 World Cup, used as the
// lower bound when aggregating leaderboards.
const tournamentStartUTC = "20260611"

// matchGoals is the per-match cache persisted under data/summaries/ so the
// leaderboard never re-fetches a finished match.
type matchGoals struct {
	ID    string      `json:"id"`
	Home  string      `json:"home"`
	Away  string      `json:"away"`
	Goals []recapGoal `json:"goals"`
}

type LeaderEntry struct {
	Player string `json:"player"`
	Team   string `json:"team"`
	Count  int    `json:"count"`
}

type Boards struct {
	Scorers []LeaderEntry `json:"scorers"`
	Assists []LeaderEntry `json:"assists"`
	Matches int           `json:"finished_matches"`
}

// Leaderboards aggregates scorer/assist tables from every finished match up
// to now. Finished matches are cached on disk; only new ones cost a fetch.
func (d *Digest) Leaderboards(ctx context.Context) (*Boards, error) {
	to := time.Now().UTC().AddDate(0, 0, 1).Format("20060102")
	sb, err := d.espn.Scoreboard(ctx, tournamentStartUTC+"-"+to)
	if err != nil {
		return nil, fmt.Errorf("leaderboard scoreboard: %w", err)
	}

	goals := map[string]int{}   // "player|team" -> goals
	assists := map[string]int{} // "player|team" -> assists
	finished := 0
	for i := range sb.Events {
		ev := &sb.Events[i]
		if ev.Status.Type.State != "post" {
			continue
		}
		mg, err := d.matchGoalsCached(ctx, ev)
		if err != nil {
			d.logger.Error("leaderboard match fetch failed", "match", ev.ID, "error", err)
			continue
		}
		finished++
		for _, g := range mg.Goals {
			if g.Player == "" {
				continue
			}
			if !g.OwnGoal {
				goals[g.Player+"|"+g.Team]++
			}
			if g.Assist != "" {
				assists[g.Assist+"|"+g.Team]++
			}
		}
	}

	b := &Boards{
		Scorers: topEntries(goals, 10),
		Assists: topEntries(assists, 10),
		Matches: finished,
	}
	// Localise player names once here: every consumer (commands, previews,
	// recaps, /ask grounding) renders from this struct.
	if d.names != nil {
		var all []string
		for _, e := range append(b.Scorers, b.Assists...) {
			all = append(all, e.Player)
		}
		d.names.EnsureBatch(ctx, all)
		for i := range b.Scorers {
			b.Scorers[i].Player = d.names.Name(b.Scorers[i].Player)
		}
		for i := range b.Assists {
			b.Assists[i].Player = d.names.Name(b.Assists[i].Player)
		}
	}
	return b, nil
}

func (d *Digest) matchGoalsCached(ctx context.Context, ev *espn.Event) (*matchGoals, error) {
	var mg matchGoals
	if err := d.store.LoadJSON("summaries", "match-"+ev.ID, &mg); err == nil && mg.ID == ev.ID {
		return &mg, nil
	}
	sum, _, err := d.espn.Summary(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	home, away := homeAway(ev)
	rm := recapMatch{ID: ev.ID}
	fillRecapDetails(&rm, sum)
	mg = matchGoals{ID: ev.ID, Home: home.Team.DisplayName, Away: away.Team.DisplayName, Goals: rm.Goals}
	if err := d.store.SaveJSON("summaries", "match-"+ev.ID, mg); err != nil {
		d.logger.Error("persist match goals failed", "match", ev.ID, "error", err)
	}
	return &mg, nil
}

func topEntries(counts map[string]int, n int) []LeaderEntry {
	entries := make([]LeaderEntry, 0, len(counts))
	for key, c := range counts {
		parts := strings.SplitN(key, "|", 2)
		entries = append(entries, LeaderEntry{Player: parts[0], Team: parts[1], Count: c})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].Player < entries[j].Player // stable, deterministic
	})
	if len(entries) > n {
		entries = entries[:n]
	}
	return entries
}

// renderBoards produces the 射手榜/助攻榜 section. Empty boards (matchday 1
// before any goal) return "".
func renderBoards(b *Boards) string {
	if b == nil || (len(b.Scorers) == 0 && len(b.Assists) == 0) {
		return ""
	}
	var sb strings.Builder
	if len(b.Scorers) > 0 {
		sb.WriteString("👟 射手榜：\n")
		writeBoard(&sb, b.Scorers, "球")
	}
	if len(b.Assists) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("🅰️ 助攻榜：\n")
		writeBoard(&sb, b.Assists, "助攻")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func writeBoard(sb *strings.Builder, entries []LeaderEntry, unit string) {
	shown := entries
	if len(shown) > 5 {
		shown = shown[:5]
	}
	rank := 0
	prev := -1
	for i, e := range shown {
		if e.Count != prev {
			rank = i + 1
			prev = e.Count
		}
		fmt.Fprintf(sb, "%d. %s（%s）%d%s\n", rank, e.Player, cnmap.Name(e.Team), e.Count, unit)
	}
}

// renderGroupTable renders one group's table with qualification marks.
func RenderGroupTable(g espn.GroupStanding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 %s组积分榜：\n", g.Letter)
	anyPlayed := false
	for _, e := range g.Entries {
		if e.Played > 0 {
			anyPlayed = true
		}
	}
	usedMark := false
	for _, e := range g.Entries {
		mark := ""
		// Positional marks are meaningless before a team has kicked a ball
		// (a 0-game team isn't really "eliminated"); only the clinched mark
		// can apply at 0 games, and that never happens.
		if e.Played > 0 {
			switch {
			case e.Advanced == "1": // mathematically through
				mark = " ✅晋级"
			case strings.Contains(e.Note, "Advance"):
				mark = " 🟢" // current rank is a qualifying spot
			case strings.Contains(e.Note, "Best"):
				mark = " 🟡" // fighting for a best-third spot
			case strings.Contains(e.Note, "Eliminated"):
				mark = " 🔴" // currently in an elimination spot
			}
		}
		if mark != "" {
			usedMark = true
		}
		fmt.Fprintf(&b, "%d. %s %d分（%d胜%d平%d负 净胜%+d）%s\n",
			e.Rank, cnmap.Name(e.Team), e.Points, e.Wins, e.Ties, e.Losses, e.GoalDiff, mark)
	}
	if anyPlayed && usedMark {
		b.WriteString("🟢当前处晋级位 🟡争最佳第三 🔴当前处淘汰位 ✅已锁定晋级")
	}
	return strings.TrimRight(b.String(), "\n")
}

// relevantGroups returns the group tables containing any of the given teams.
func relevantGroups(all []espn.GroupStanding, teams map[string]bool) []espn.GroupStanding {
	var out []espn.GroupStanding
	for _, g := range all {
		for _, e := range g.Entries {
			if teams[e.Team] {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

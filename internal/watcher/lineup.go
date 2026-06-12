package watcher

import (
	"fmt"
	"strings"

	"worldcup-broadcaster/internal/cnmap"
	"worldcup-broadcaster/internal/espn"
)

// RenderLineups formats both starting XIs (with formation and bench) for the
// kickoff broadcast; nameFn localises player names. Returns "" when rosters
// are not published.
func RenderLineups(sum *espn.Summary, nameFn func(string) string) string {
	if len(sum.Rosters) == 0 {
		return ""
	}
	if nameFn == nil {
		nameFn = func(s string) string { return s }
	}
	_, home, away := sum.Live()
	var b strings.Builder
	fmt.Fprintf(&b, "📋 首发阵容 | %s%s vs %s%s\n",
		cnmap.Name(home.Team.DisplayName), cnmap.Flag(home.Team.DisplayName),
		cnmap.Name(away.Team.DisplayName), cnmap.Flag(away.Team.DisplayName))
	for _, ros := range sum.Rosters {
		var starters, bench []string
		for _, p := range ros.Roster {
			if p.Starter {
				s := p.Jersey + " " + nameFn(p.Athlete.DisplayName)
				if p.Position.Abbreviation != "" {
					s += "(" + p.Position.Abbreviation + ")"
				}
				starters = append(starters, s)
			} else {
				bench = append(bench, nameFn(p.Athlete.DisplayName))
			}
		}
		if len(starters) == 0 {
			continue
		}
		name := cnmap.Name(ros.Team.DisplayName)
		fmt.Fprintf(&b, "\n%s%s", cnmap.Flag(ros.Team.DisplayName), name)
		if ros.Formation != "" {
			fmt.Fprintf(&b, "（%s）", ros.Formation)
		}
		b.WriteString("\n" + strings.Join(starters, "、") + "\n")
		if len(bench) > 0 {
			b.WriteString("替补：" + strings.Join(bench, "、") + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

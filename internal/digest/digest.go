// Package digest builds the nightly schedule preview (23:00 CST) and the
// afternoon results recap (15:00 CST). LLM content is best-effort: any
// failure degrades to the data-only message.
package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"worldcup-broadcaster/internal/cnmap"
	"worldcup-broadcaster/internal/espn"
	"worldcup-broadcaster/internal/store"
)

type Sender interface {
	EnqueueGroup(msg string)
}

type LLM interface {
	Generate(ctx context.Context, system, user string) (string, error)
}

type Digest struct {
	espn   *espn.Client
	llm    LLM
	store  *store.Store
	sender Sender
	alert  func(category, msg string)
	logger *slog.Logger
}

func New(c *espn.Client, l LLM, st *store.Store, s Sender, alertFn func(string, string), logger *slog.Logger) *Digest {
	return &Digest{espn: c, llm: l, store: st, sender: s, alert: alertFn, logger: logger}
}

var cst = time.FixedZone("CST", 8*3600)

// maxMessageRunes is a conservative QQ message size bound; longer digests
// are split at match-block boundaries.
const maxMessageRunes = 2800

const blockSep = "\n━━━━━━━━━━━━\n"

func displayDate(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	weekdays := [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	return fmt.Sprintf("%d月%d日 %s", int(t.Month()), t.Day(), weekdays[t.Weekday()])
}

func stageName(slug string) string {
	switch slug {
	case "group-stage":
		return "小组赛"
	case "round-of-32":
		return "1/16决赛"
	case "round-of-16":
		return "1/8决赛"
	case "quarterfinals":
		return "1/4决赛"
	case "semifinals":
		return "半决赛"
	case "third-place-playoff", "third-place":
		return "季军赛"
	case "final":
		return "决赛"
	}
	return slug
}

func homeAway(ev *espn.Event) (home, away espn.Competitor) {
	if len(ev.Competitions) == 0 {
		return
	}
	for _, c := range ev.Competitions[0].Competitors {
		if c.HomeAway == "home" {
			home = c
		} else {
			away = c
		}
	}
	return
}

// sendSplit enqueues text, splitting at block boundaries when it exceeds
// the size bound.
func (d *Digest) sendSplit(text string) {
	if len([]rune(text)) <= maxMessageRunes {
		d.sender.EnqueueGroup(text)
		return
	}
	blocks := strings.Split(text, blockSep)
	cur := ""
	flush := func() {
		if strings.TrimSpace(cur) != "" {
			d.sender.EnqueueGroup(strings.TrimSpace(cur))
		}
		cur = ""
	}
	for _, b := range blocks {
		candidate := cur
		if candidate != "" {
			candidate += blockSep
		}
		candidate += b
		if len([]rune(candidate)) > maxMessageRunes && cur != "" {
			flush()
			cur = b
		} else {
			cur = candidate
		}
	}
	flush()
}

// h2hLine renders the head-to-head record from the perspective team.
func h2hLine(games []espn.H2HTeam) string {
	if len(games) == 0 || len(games[0].Events) == 0 {
		return ""
	}
	t := games[0]
	w, dr, l := 0, 0, 0
	for _, g := range t.Events {
		switch g.GameResult {
		case "W":
			w++
		case "L":
			l++
		default:
			dr++
		}
	}
	return fmt.Sprintf("🔁 近%d次交锋：%s %d胜%d平%d负", len(t.Events), cnmap.Name(t.Team.DisplayName), w, dr, l)
}

// standingsBlock renders a compact group table from the summary's raw
// standings JSON. Returns "" when there is no meaningful data yet.
func standingsBlock(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var doc struct {
		Groups []struct {
			Standings struct {
				Entries []struct {
					Team  string `json:"team"`
					Stats []struct {
						Name         string `json:"name"`
						DisplayValue string `json:"displayValue"`
					} `json:"stats"`
				} `json:"entries"`
			} `json:"standings"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Groups) == 0 {
		return ""
	}
	var b strings.Builder
	for _, g := range doc.Groups {
		if len(g.Standings.Entries) == 0 {
			continue
		}
		played := false
		type row struct {
			rank             int
			team, pts, w, t, l, gd string
		}
		rows := make([]row, 0, len(g.Standings.Entries))
		for _, e := range g.Standings.Entries {
			r := row{team: cnmap.Name(e.Team)}
			for _, s := range e.Stats {
				switch s.Name {
				case "rank":
					fmt.Sscanf(s.DisplayValue, "%d", &r.rank)
				case "points":
					r.pts = s.DisplayValue
				case "wins":
					r.w = s.DisplayValue
				case "ties":
					r.t = s.DisplayValue
				case "losses":
					r.l = s.DisplayValue
				case "pointDifferential":
					r.gd = s.DisplayValue
				case "gamesPlayed":
					if s.DisplayValue != "0" {
						played = true
					}
				}
			}
			rows = append(rows, r)
		}
		if !played {
			continue // matchday 1: an all-zero table is noise
		}
		for i := 0; i < len(rows); i++ {
			for j := i + 1; j < len(rows); j++ {
				if rows[j].rank < rows[i].rank {
					rows[i], rows[j] = rows[j], rows[i]
				}
			}
		}
		b.WriteString("📊 小组积分榜：\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "%d. %s %s分（%s胜%s平%s负，净胜%s）\n", r.rank, r.team, r.pts, r.w, r.t, r.l, r.gd)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

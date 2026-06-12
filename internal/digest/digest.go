// Package digest builds the nightly schedule preview (23:00 CST) and the
// afternoon results recap (15:00 CST). LLM content is best-effort: any
// failure degrades to the data-only message.
package digest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"worldcup-broadcaster/internal/cnmap"
	"worldcup-broadcaster/internal/espn"
	"worldcup-broadcaster/internal/playername"
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
	names  *playername.Translator // optional Chinese player-name localiser
}

func New(c *espn.Client, l LLM, st *store.Store, s Sender, alertFn func(string, string), logger *slog.Logger) *Digest {
	return &Digest{espn: c, llm: l, store: st, sender: s, alert: alertFn, logger: logger}
}

// SetNameTranslator wires the Chinese player-name translator (optional).
func (d *Digest) SetNameTranslator(t *playername.Translator) { d.names = t }

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

func StageName(slug string) string {
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
		// A single oversized block (e.g. runaway LLM output) is hard-split
		// so the message never exceeds the send limit.
		for len([]rune(b)) > maxMessageRunes {
			r := []rune(b)
			flush()
			d.sender.EnqueueGroup(string(r[:maxMessageRunes]))
			b = string(r[maxMessageRunes:])
		}
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

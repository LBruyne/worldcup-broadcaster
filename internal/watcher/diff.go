package watcher

import (
	"regexp"
	"sort"
	"strings"

	"worldcup-broadcaster/internal/espn"
	"worldcup-broadcaster/internal/format"
)

type EventType int

const (
	EvKickoff EventType = iota
	EvGoal
	EvPenaltyMissed
	EvYellowCard
	EvRedCard
	EvSubstitution
	EvHalftime
	EvSecondHalfStart
	EvEndRegularTime
	EvStartExtraTime
	EvHalftimeExtraTime
	EvStartSecondHalfExtraTime
	EvEndExtraTime
	EvStartShootout
	EvShootoutRound
	EvEndMatch
	EvUnknown
)

// Event is one new broadcastable occurrence extracted from a summary.
type Event struct {
	Key   string // dedup key, stable across polls
	Type  EventType
	Clock string
	Team  string // scoring/card/sub team displayName
	Ctx   format.MatchCtx

	Player  string // scorer / carded player / sub-in / shootout taker
	Assist  string // assister (goals only)
	SubOut  string // substitution: player leaving
	Penalty bool   // goal from a regulation penalty
	OwnGoal bool

	Round  int  // shootout round number
	Scored bool // shootout attempt result
	SoHome int  // running shootout score
	SoAway int

	Detail string // ESPN status detail, for fulltime rendering
}

// keyEvent type.id constants observed from ESPN data.
const (
	typeGoal           = "70"
	typeSubstitution   = "76"
	typeKickoff        = "80"
	typeHalftime       = "81"
	typeStart2ndHalf   = "82"
	typeEndRegularTime = "83"
	typeStartExtraTime = "84"
	typeHalftimeET     = "85"
	typeStart2ndHalfET = "86"
	typeEndExtraTime   = "87"
	typeStartShootout  = "88"
	typeEndMatch       = "89"
	typeYellowCard     = "94"
	typePenaltyScored  = "98"
)

// scoreFromText parses "Goal!  Argentina 2, France 0. ..." so buffered goal
// events carry the score as of that goal rather than the latest snapshot.
var scoreTextRe = regexp.MustCompile(`([^.!]+?) (\d+), ([^.!]+?) (\d+)\.`)

func scoreFromText(text, homeName, awayName string) (home, away string, ok bool) {
	m := scoreTextRe.FindStringSubmatch(text)
	if m == nil {
		return "", "", false
	}
	a, as, b, bs := strings.TrimSpace(m[1]), m[2], strings.TrimSpace(m[3]), m[4]
	switch {
	case strings.HasSuffix(a, homeName) && b == awayName:
		return as, bs, true
	case strings.HasSuffix(a, awayName) && b == homeName:
		return bs, as, true
	}
	return "", "", false
}

func classify(k *espn.KeyEvent) EventType {
	switch k.Type.ID {
	case typeKickoff:
		return EvKickoff
	case typeHalftime:
		return EvHalftime
	case typeStart2ndHalf:
		return EvSecondHalfStart
	case typeEndRegularTime:
		return EvEndRegularTime
	case typeStartExtraTime:
		return EvStartExtraTime
	case typeHalftimeET:
		return EvHalftimeExtraTime
	case typeStart2ndHalfET:
		return EvStartSecondHalfExtraTime
	case typeEndExtraTime:
		return EvEndExtraTime
	case typeStartShootout:
		return EvStartShootout
	case typeEndMatch:
		return EvEndMatch
	case typeSubstitution:
		return EvSubstitution
	case typeYellowCard:
		return EvYellowCard
	case typeGoal, typePenaltyScored:
		return EvGoal
	}
	// Fall back to text matching for ids we have not observed (red cards,
	// own goals, missed penalties vary by feed).
	text := k.Type.Text
	switch {
	case strings.Contains(text, "Red Card"):
		return EvRedCard
	case strings.Contains(text, "Own Goal"):
		return EvGoal
	case strings.Contains(text, "Penalty") && (strings.Contains(text, "Missed") || strings.Contains(text, "Saved")):
		return EvPenaltyMissed
	case strings.Contains(text, "Goal"):
		return EvGoal
	case strings.Contains(text, "Yellow"):
		return EvYellowCard
	}
	return EvUnknown
}

// Diff extracts all not-yet-seen events from the summary, in chronological
// order. isSeen consults persistent dedup state; shootout attempts come from
// the dedicated shootout array with "so-" prefixed keys.
func Diff(sum *espn.Summary, isSeen func(key string) bool) []Event {
	st, home, away := sum.Live()
	ctx := format.MatchCtx{
		HomeName: home.Team.DisplayName, AwayName: away.Team.DisplayName,
		HomeScore: home.Score, AwayScore: away.Score,
	}

	var out []Event
	for i := range sum.KeyEvents {
		k := &sum.KeyEvents[i]
		if k.Shootout { // shootout attempts handled via sum.Shootout below
			continue
		}
		key := "ke-" + k.ID
		if isSeen(key) {
			continue
		}
		et := classify(k)
		if et == EvUnknown {
			continue
		}
		ev := Event{
			Key:    key,
			Type:   et,
			Clock:  k.Clock.DisplayValue,
			Team:   k.Team.DisplayName,
			Ctx:    ctx,
			Detail: st.Type.Detail,
			SoHome: int(home.ShootoutScore),
			SoAway: int(away.ShootoutScore),
		}
		if len(k.Participants) > 0 {
			ev.Player = k.Participants[0].Athlete.DisplayName
		}
		switch et {
		case EvGoal:
			ev.Penalty = k.Type.ID == typePenaltyScored
			ev.OwnGoal = strings.Contains(k.Type.Text, "Own Goal")
			if !ev.Penalty && !ev.OwnGoal && len(k.Participants) > 1 {
				ev.Assist = k.Participants[1].Athlete.DisplayName
			}
			if h, a, ok := scoreFromText(k.Text, ctx.HomeName, ctx.AwayName); ok {
				ev.Ctx.HomeScore, ev.Ctx.AwayScore = h, a
			}
		case EvSubstitution:
			if len(k.Participants) > 1 {
				ev.SubOut = k.Participants[1].Athlete.DisplayName
			}
		}
		out = append(out, ev)
	}

	out = append(out, diffShootout(sum, ctx, home, away, isSeen)...)

	// Keep End Match last: a final poll may surface both the last shootout
	// round and the end-match key event in one batch.
	sort.SliceStable(out, func(i, j int) bool {
		return (out[i].Type != EvEndMatch) && (out[j].Type == EvEndMatch)
	})
	return out
}

func diffShootout(sum *espn.Summary, ctx format.MatchCtx, home, away espn.Competitor, isSeen func(string) bool) []Event {
	if len(sum.Shootout) == 0 {
		return nil
	}
	type shot struct {
		espn.ShootoutShot
		teamID, teamName string
	}
	var shots []shot
	for _, t := range sum.Shootout {
		for _, s := range t.Shots {
			shots = append(shots, shot{s, t.ID, t.Team})
		}
	}
	// Shot ids are assigned in wall-clock order; numeric string compare via
	// length then lexicographic keeps them sorted without atoi error paths.
	sort.Slice(shots, func(i, j int) bool {
		a, b := shots[i].ID, shots[j].ID
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return a < b
	})

	soHome, soAway := 0, 0
	var out []Event
	for _, s := range shots {
		isHome := s.teamName == ctx.HomeName || s.teamID == home.Team.ID
		if s.DidScore {
			if isHome {
				soHome++
			} else {
				soAway++
			}
		}
		key := "so-" + s.ID
		if isSeen(key) {
			continue
		}
		out = append(out, Event{
			Key:    key,
			Type:   EvShootoutRound,
			Team:   s.teamName,
			Ctx:    ctx,
			Player: s.Player,
			Round:  s.ShotNumber,
			Scored: s.DidScore,
			SoHome: soHome,
			SoAway: soAway,
		})
	}
	return out
}

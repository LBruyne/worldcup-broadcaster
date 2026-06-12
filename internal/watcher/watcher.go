// Package watcher polls a live match and broadcasts new events.
package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"worldcup-broadcaster/internal/config"
	"worldcup-broadcaster/internal/espn"
	"worldcup-broadcaster/internal/format"
	"worldcup-broadcaster/internal/store"
)

// Sender is satisfied by *onebot.Client.
type Sender interface {
	EnqueueGroup(msg string)
}

type Options struct {
	PollInterval time.Duration
	PreKickoff   time.Duration // how long before kickoff polling starts
	MaxDuration  time.Duration // hard stop after kickoff (stuck feeds)
	Events       config.Events
}

func DefaultOptions(ev config.Events, poll time.Duration) Options {
	return Options{
		PollInterval: poll,
		PreKickoff:   5 * time.Minute,
		MaxDuration:  8 * time.Hour,
		Events:       ev,
	}
}

type Watcher struct {
	espn   *espn.Client
	sender Sender
	store  *store.Store
	alert  func(category, msg string)
	logger *slog.Logger
	opts   Options

	// pendingGoals holds goal keys seen once without a parseable score
	// text: they are held back one poll so ESPN can enrich the event
	// (score sentence, assist) instead of broadcasting a stale snapshot.
	pendingGoals map[string]bool
}

func New(c *espn.Client, s Sender, st *store.Store, alertFn func(string, string), logger *slog.Logger, opts Options) *Watcher {
	return &Watcher{espn: c, sender: s, store: st, alert: alertFn, logger: logger, opts: opts,
		pendingGoals: make(map[string]bool)}
}

// Watch blocks until the match finishes, ctx is cancelled, or MaxDuration
// elapses. Safe to call for already-finished matches: events already pushed
// are deduped by the store, fresh ones (crash recovery) are pushed once.
func (w *Watcher) Watch(ctx context.Context, ev espn.Event) error {
	ko, err := ev.Kickoff()
	if err != nil {
		return fmt.Errorf("match %s: bad kickoff date %q: %w", ev.ID, ev.Date, err)
	}
	log := w.logger.With("match", ev.ID, "name", ev.ShortName)

	if wait := time.Until(ko.Add(-w.opts.PreKickoff)); wait > 0 {
		log.Info("waiting for kickoff window", "kickoff", ko, "wait", wait.Round(time.Second))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	log.Info("live polling started")
	date := espn.ChinaDate(ko)
	consecutiveFails := 0
	ticker := time.NewTicker(w.opts.PollInterval)
	defer ticker.Stop()

	for {
		sum, raw, err := w.espn.Summary(ctx, ev.ID)
		if err != nil {
			consecutiveFails++
			log.Error("summary fetch failed", "error", err, "consecutive", consecutiveFails)
			if consecutiveFails == 5 {
				w.alert("espn", fmt.Sprintf("比赛 %s 数据连续 %d 次拉取失败: %v", ev.ShortName, consecutiveFails, err))
			}
		} else {
			consecutiveFails = 0
			finished := w.processSnapshot(ev.ID, date, sum, raw, log)
			if finished {
				log.Info("match finished, watcher exiting")
				return nil
			}
		}

		if time.Since(ko) > w.opts.MaxDuration {
			w.alert("watcher", fmt.Sprintf("比赛 %s 监控超过 %v 仍未结束，强制退出", ev.ShortName, w.opts.MaxDuration))
			return fmt.Errorf("match %s: watch timed out", ev.ID)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// processSnapshot diffs, renders, enqueues and persists. Returns true when
// the match reached a terminal state and the final whistle was broadcast.
func (w *Watcher) processSnapshot(matchID, date string, sum *espn.Summary, raw []byte, log *slog.Logger) bool {
	// Starting lineups go out once, when the match goes live (kickoff
	// switch governs them, like the kickoff message itself).
	if liveSt, _, _ := sum.Live(); liveSt.Type.State == "in" &&
		w.opts.Events.Kickoff && len(sum.Rosters) > 0 && !w.store.IsPushed(matchID, "lineups") {
		if msg := RenderLineups(sum); msg != "" {
			log.Info("broadcasting lineups", "teams", len(sum.Rosters))
			w.sender.EnqueueGroup(msg)
		}
		if err := w.store.MarkPushed(matchID, "lineups"); err != nil {
			log.Error("persist pushed state failed", "error", err)
		}
	}
	st0, _, _ := sum.Live()
	events := Diff(sum, func(key string) bool { return w.store.IsPushed(matchID, key) })
	for _, e := range events {
		// A live goal without its score sentence is still being enriched
		// by the feed: hold it one poll so the broadcast carries the right
		// scoreline (and the assist), not a lagging snapshot.
		if e.Type == EvGoal && !e.ScoreParsed && st0.Type.State == "in" && !w.pendingGoals[e.Key] {
			w.pendingGoals[e.Key] = true
			log.Info("deferring goal one poll for feed enrichment", "key", e.Key, "clock", e.Clock)
			continue
		}
		delete(w.pendingGoals, e.Key)
		if Enabled(e.Type, w.opts.Events) {
			msg := Render(e)
			log.Info("broadcasting event", "type", e.Type, "key", e.Key, "clock", e.Clock)
			w.sender.EnqueueGroup(msg)
		} else {
			log.Debug("event suppressed by config", "type", e.Type, "key", e.Key)
		}
		// Disabled events are marked too so enabling a switch mid-match
		// doesn't backfill stale events.
		if err := w.store.MarkPushed(matchID, e.Key); err != nil {
			log.Error("persist pushed state failed", "error", err)
		}
	}
	if len(events) > 0 {
		if err := w.store.SaveJSON(date, "match-"+matchID, json.RawMessage(raw)); err != nil {
			log.Error("persist snapshot failed", "error", err)
		}
	}

	st, _, _ := sum.Live()
	if st.Type.State != "post" {
		return false
	}
	// Ensure the final whistle is out even if the feed never emitted an
	// End Match key event. If the feed has one (pushed now or in a previous
	// run), the synthetic fallback must stay silent.
	for i := range sum.KeyEvents {
		if classify(&sum.KeyEvents[i]) == EvEndMatch {
			return true
		}
	}
	key := "synthetic-end"
	if !w.store.IsPushed(matchID, key) {
		_, home, away := sum.Live()
		e := Event{
			Type: EvEndMatch,
			Ctx: format.MatchCtx{
				HomeName: home.Team.DisplayName, AwayName: away.Team.DisplayName,
				HomeScore: home.Score, AwayScore: away.Score,
			},
			Detail: st.Type.Detail,
			SoHome: int(home.ShootoutScore), SoAway: int(away.ShootoutScore),
		}
		if Enabled(EvEndMatch, w.opts.Events) {
			w.sender.EnqueueGroup(Render(e))
		}
		if err := w.store.MarkPushed(matchID, key); err != nil {
			log.Error("persist pushed state failed", "error", err)
		}
	}
	return true
}

// Enabled maps an event type to its config switch.
func Enabled(t EventType, e config.Events) bool {
	switch t {
	case EvKickoff:
		return e.Kickoff
	case EvGoal, EvPenaltyMissed:
		return e.Goal
	case EvYellowCard:
		return e.YellowCard
	case EvRedCard:
		return e.RedCard
	case EvHalftime:
		return e.Halftime
	case EvSecondHalfStart:
		return e.SecondHalfStart
	case EvSubstitution:
		return e.Substitution
	case EvEndRegularTime, EvStartExtraTime, EvHalftimeExtraTime,
		EvStartSecondHalfExtraTime, EvEndExtraTime, EvStartShootout:
		return e.PeriodChange
	case EvShootoutRound:
		return e.ShootoutRound
	case EvEndMatch:
		return e.Fulltime
	}
	return false
}

// Render turns an Event into the QQ message text.
func Render(e Event) string {
	switch e.Type {
	case EvKickoff:
		return format.Kickoff(e.Ctx)
	case EvGoal:
		return format.Goal(e.Ctx, e.Team, e.Player, e.Assist, e.Clock, e.Penalty, e.OwnGoal)
	case EvPenaltyMissed:
		return format.PenaltyMissed(e.Ctx, e.Team, e.Player, e.Clock)
	case EvYellowCard:
		return format.YellowCard(e.Ctx, e.Team, e.Player, e.Clock)
	case EvRedCard:
		return format.RedCard(e.Ctx, e.Team, e.Player, e.Clock)
	case EvSubstitution:
		return format.Substitution(e.Ctx, e.Team, e.Player, e.SubOut, e.Clock)
	case EvHalftime:
		return format.Halftime(e.Ctx)
	case EvSecondHalfStart:
		return format.SecondHalfStart(e.Ctx)
	case EvEndRegularTime:
		return format.EndRegularTime(e.Ctx)
	case EvStartExtraTime:
		return format.StartExtraTime(e.Ctx)
	case EvHalftimeExtraTime:
		return format.HalftimeExtraTime(e.Ctx)
	case EvStartSecondHalfExtraTime:
		return format.StartSecondHalfExtraTime(e.Ctx)
	case EvEndExtraTime:
		return format.EndExtraTime(e.Ctx)
	case EvStartShootout:
		return format.StartShootout(e.Ctx)
	case EvShootoutRound:
		return format.ShootoutRound(e.Ctx, e.Team, e.Player, e.Round, e.Scored, e.SoHome, e.SoAway)
	case EvEndMatch:
		return format.Fulltime(e.Ctx, e.Detail, e.SoHome, e.SoAway)
	}
	return ""
}

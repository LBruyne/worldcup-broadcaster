package espn

import (
	"context"
	"fmt"
	"time"
)

var cst = time.FixedZone("CST", 8*3600)

// ChinaDate formats t as the China calendar date "2006-01-02".
func ChinaDate(t time.Time) string {
	return t.In(cst).Format("2006-01-02")
}

// FilterChinaDate returns the events whose kickoff falls on the given China
// calendar date. date must be "2006-01-02" formatted.
func FilterChinaDate(events []Event, date string) ([]Event, error) {
	day, err := time.ParseInLocation("2006-01-02", date, cst)
	if err != nil {
		return nil, fmt.Errorf("bad date %q: %w", date, err)
	}
	end := day.Add(24 * time.Hour)
	var out []Event
	for _, e := range events {
		ko, err := e.Kickoff()
		if err != nil {
			continue // unparsable date: skip rather than fail the whole day
		}
		if !ko.Before(day) && ko.Before(end) {
			out = append(out, e)
		}
	}
	return out, nil
}

// MatchesOnChinaDate fetches the scoreboard window covering the China
// calendar date and filters by actual kickoff time. The dates parameter's
// timezone semantics are undocumented, so we over-fetch one UTC day on each
// side and filter strictly.
func (c *Client) MatchesOnChinaDate(ctx context.Context, date string) ([]Event, error) {
	day, err := time.ParseInLocation("2006-01-02", date, cst)
	if err != nil {
		return nil, fmt.Errorf("bad date %q: %w", date, err)
	}
	from := day.UTC().AddDate(0, 0, -1).Format("20060102")
	to := day.UTC().AddDate(0, 0, 1).Format("20060102")
	sb, err := c.Scoreboard(ctx, from+"-"+to)
	if err != nil {
		return nil, err
	}
	return FilterChinaDate(sb.Events, date)
}

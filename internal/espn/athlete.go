package espn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// webBase hosts the athlete search/stats APIs (a different host from the
// scoreboard endpoints); overridable for tests.
var webBase = "https://site.web.api.espn.com"

// Athlete is one soccer player search hit.
type Athlete struct {
	ID          string
	DisplayName string
	Description string // usually the league/team context line
}

// SearchAthletes finds soccer players by (English) name.
func (c *Client) SearchAthletes(ctx context.Context, name string) ([]Athlete, error) {
	u := webBase + "/apis/search/v2?limit=5&query=" + url.QueryEscape(name)
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	return parseAthleteSearch(body)
}

func parseAthleteSearch(body []byte) ([]Athlete, error) {
	var doc struct {
		Results []struct {
			Type     string `json:"type"`
			Contents []struct {
				ID          string `json:"id"`
				UID         string `json:"uid"`
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
			} `json:"contents"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse athlete search: %w", err)
	}
	var out []Athlete
	for _, r := range doc.Results {
		if r.Type != "player" {
			continue
		}
		for _, c := range r.Contents {
			// soccer players carry the s:600 sport prefix in their uid
			if !strings.Contains(c.UID, "s:600~") {
				continue
			}
			out = append(out, Athlete{ID: c.ID, DisplayName: c.DisplayName, Description: c.Description})
		}
	}
	return out, nil
}

// SeasonStatRow is one season × league × team statistics split.
type SeasonStatRow struct {
	Season string            `json:"season"` // "2025-26 English Premier League"
	Team   string            `json:"team"`
	Stats  map[string]string `json:"stats"` // stat name -> display value
}

// AthleteSeasonStats fetches a player's per-season splits (the default view
// covers their primary league; pass league e.g. "uefa.champions" to filter).
func (c *Client) AthleteSeasonStats(ctx context.Context, athleteID, league string) ([]SeasonStatRow, error) {
	u := webBase + "/apis/common/v3/sports/soccer/athletes/" + url.PathEscape(athleteID) + "/stats?region=us&lang=en"
	if league != "" {
		u += "&league=" + url.QueryEscape(league)
	}
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	return parseAthleteStats(body)
}

func parseAthleteStats(body []byte) ([]SeasonStatRow, error) {
	var doc struct {
		Teams map[string]struct {
			DisplayName string `json:"displayName"`
		} `json:"teams"`
		Categories []struct {
			Names      []string `json:"names"`
			Statistics []struct {
				TeamSlug string `json:"teamSlug"`
				Season   struct {
					Type struct {
						Name string `json:"name"`
					} `json:"type"`
				} `json:"season"`
				Values []string `json:"values"`
				Stats  []string `json:"stats"`
			} `json:"statistics"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse athlete stats: %w", err)
	}
	// merge categories: same season rows may appear per category
	rows := map[string]*SeasonStatRow{}
	var order []string
	for _, cat := range doc.Categories {
		for _, st := range cat.Statistics {
			season := st.Season.Type.Name
			if season == "" {
				continue
			}
			key := season + "|" + st.TeamSlug
			row, ok := rows[key]
			if !ok {
				team := st.TeamSlug
				if t, found := doc.Teams[st.TeamSlug]; found {
					team = t.DisplayName
				}
				row = &SeasonStatRow{Season: season, Team: team, Stats: map[string]string{}}
				rows[key] = row
				order = append(order, key)
			}
			vals := st.Values
			if len(vals) == 0 {
				vals = st.Stats
			}
			for i, name := range cat.Names {
				if i < len(vals) {
					row.Stats[name] = vals[i]
				}
			}
		}
	}
	out := make([]SeasonStatRow, 0, len(order))
	for _, k := range order {
		out = append(out, *rows[k])
	}
	return out, nil
}

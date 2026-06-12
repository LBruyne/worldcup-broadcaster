package espn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GroupStanding is one group table from the league-wide standings endpoint.
type GroupStanding struct {
	Name    string // "Group A"
	Letter  string // "A"
	Entries []StandingEntry
}

type StandingEntry struct {
	Team     string // displayName
	Rank     int
	Points   int
	Wins     int
	Ties     int
	Losses   int
	GoalDiff int
	Played   int
	Advanced string // "1" when ESPN marks the team as mathematically through
	Note     string // positional note ("Advance to Round of 32", "Eliminated", ...)
}

// standingsURL uses the /apis/v2 path (not /apis/site/v2).
func (c *Client) standingsURL() string {
	base := strings.Replace(c.baseURL, "/apis/site/v2", "/apis/v2", 1)
	return fmt.Sprintf("%s/%s/standings?season=2026", base, c.league)
}

// FullStandings fetches all twelve group tables.
func (c *Client) FullStandings(ctx context.Context) ([]GroupStanding, error) {
	body, err := c.get(ctx, c.standingsURL())
	if err != nil {
		return nil, err
	}
	return ParseStandings(body)
}

// ParseStandings decodes the standings document (exported for tests and for
// reusing persisted snapshots).
func ParseStandings(body []byte) ([]GroupStanding, error) {
	var doc struct {
		Children []struct {
			Name      string `json:"name"`
			Standings struct {
				Entries []struct {
					Team struct {
						DisplayName string `json:"displayName"`
					} `json:"team"`
					Note struct {
						Description string `json:"description"`
					} `json:"note"`
					Stats []struct {
						Name         string  `json:"name"`
						Value        float64 `json:"value"`
						DisplayValue string  `json:"displayValue"`
					} `json:"stats"`
				} `json:"entries"`
			} `json:"standings"`
		} `json:"children"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse standings: %w", err)
	}
	out := make([]GroupStanding, 0, len(doc.Children))
	for _, ch := range doc.Children {
		g := GroupStanding{Name: ch.Name, Letter: strings.TrimPrefix(ch.Name, "Group ")}
		for _, e := range ch.Standings.Entries {
			se := StandingEntry{Team: e.Team.DisplayName, Note: e.Note.Description}
			for _, s := range e.Stats {
				v := int(s.Value)
				switch s.Name {
				case "rank":
					se.Rank = v
				case "points":
					se.Points = v
				case "wins":
					se.Wins = v
				case "ties":
					se.Ties = v
				case "losses":
					se.Losses = v
				case "pointDifferential":
					se.GoalDiff = v
				case "gamesPlayed":
					se.Played = v
				case "advanced":
					se.Advanced = s.DisplayValue
				}
			}
			g.Entries = append(g.Entries, se)
		}
		// entries arrive unsorted relative to rank
		for i := 0; i < len(g.Entries); i++ {
			for j := i + 1; j < len(g.Entries); j++ {
				if g.Entries[j].Rank < g.Entries[i].Rank {
					g.Entries[i], g.Entries[j] = g.Entries[j], g.Entries[i]
				}
			}
		}
		out = append(out, g)
	}
	return out, nil
}

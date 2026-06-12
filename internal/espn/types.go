// Package espn fetches and parses ESPN's public soccer API.
package espn

import (
	"encoding/json"
	"time"
)

type Scoreboard struct {
	Events []Event `json:"events"`
}

type Event struct {
	ID        string `json:"id"`
	Date      string `json:"date"` // "2026-06-11T19:00Z"
	Name      string `json:"name"`
	ShortName string `json:"shortName"`
	Season    struct {
		Slug string `json:"slug"` // "group-stage", "round-of-32", ...
	} `json:"season"`
	Status       Status        `json:"status"`
	Competitions []Competition `json:"competitions"`
}

const kickoffLayout = "2006-01-02T15:04Z"

// Kickoff parses the event date (UTC).
func (e *Event) Kickoff() (time.Time, error) {
	return time.Parse(kickoffLayout, e.Date)
}

type Competition struct {
	Venue       Venue        `json:"venue"`
	Competitors []Competitor `json:"competitors"`
	Status      Status       `json:"status"`
}

type Competitor struct {
	HomeAway      string  `json:"homeAway"`
	Score         string  `json:"score"`
	ShootoutScore float64 `json:"shootoutScore"`
	Form          string  `json:"form"` // recent results, e.g. "WWWDD"
	Team          Team    `json:"team"`
}

type Team struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	Abbreviation string `json:"abbreviation"`
}

type Status struct {
	Clock        float64 `json:"clock"`
	DisplayClock string  `json:"displayClock"`
	Type         struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		State       string `json:"state"` // pre | in | post
		Completed   bool   `json:"completed"`
		Description string `json:"description"`
		Detail      string `json:"detail"` // "FT", "FT-Pens", "AET", "HT", "45'+7'", ...
		ShortDetail string `json:"shortDetail"`
	} `json:"type"`
}

type Venue struct {
	FullName string `json:"fullName"`
	Address  struct {
		City    string `json:"city"`
		Country string `json:"country"`
	} `json:"address"`
}

// Summary is the per-match detail endpoint response. RawMessage fields are
// kept verbatim for disk persistence and LLM prompts.
type Summary struct {
	Header          Header          `json:"header"`
	KeyEvents       []KeyEvent      `json:"keyEvents"`
	Shootout        []ShootoutTeam  `json:"shootout"`
	HeadToHeadGames []H2HTeam       `json:"headToHeadGames"`
	Standings       json.RawMessage `json:"standings"`
	Leaders         json.RawMessage `json:"leaders"`
	Rosters         []Roster        `json:"rosters"`
	GameInfo        struct {
		Venue Venue `json:"venue"`
	} `json:"gameInfo"`
}

// Roster is one team's match-day squad with formation and starters.
type Roster struct {
	HomeAway  string        `json:"homeAway"`
	Formation string        `json:"formation"`
	Team      Team          `json:"team"`
	Roster    []RosterEntry `json:"roster"`
}

type RosterEntry struct {
	Jersey  string `json:"jersey"`
	Starter bool   `json:"starter"`
	Athlete struct {
		DisplayName string `json:"displayName"`
	} `json:"athlete"`
	Position struct {
		Abbreviation string `json:"abbreviation"`
	} `json:"position"`
}

type Header struct {
	Competitions []HeaderCompetition `json:"competitions"`
}

type HeaderCompetition struct {
	Date        string       `json:"date"`
	Competitors []Competitor `json:"competitors"`
	Status      Status       `json:"status"`
}

// Live returns the header competition status and home/away competitors.
func (s *Summary) Live() (st Status, home, away Competitor) {
	if len(s.Header.Competitions) == 0 {
		return
	}
	c := s.Header.Competitions[0]
	st = c.Status
	for _, comp := range c.Competitors {
		if comp.HomeAway == "home" {
			home = comp
		} else {
			away = comp
		}
	}
	return
}

type KeyEvent struct {
	ID   string `json:"id"`
	Type struct {
		ID   string `json:"id"`
		Text string `json:"text"` // "Goal", "Yellow Card", ...
		Type string `json:"type"` // machine token, e.g. "goal"
	} `json:"type"`
	Text      string `json:"text"`
	ShortText string `json:"shortText"`
	Period    struct {
		Number int `json:"number"`
	} `json:"period"`
	Clock struct {
		Value        float64 `json:"value"`
		DisplayValue string  `json:"displayValue"`
	} `json:"clock"`
	ScoringPlay  bool `json:"scoringPlay"`
	Shootout     bool `json:"shootout"`
	Team         Team `json:"team"`
	Participants []struct {
		Athlete struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"athlete"`
	} `json:"participants"`
}

type ShootoutTeam struct {
	ID    string         `json:"id"` // team id
	Team  string         `json:"team"`
	Shots []ShootoutShot `json:"shots"`
}

type ShootoutShot struct {
	ID         string `json:"id"`
	Player     string `json:"player"`
	ShotNumber int    `json:"shotNumber"`
	DidScore   bool   `json:"didScore"`
}

type H2HTeam struct {
	Team   Team      `json:"team"`
	Events []H2HGame `json:"events"`
}

type H2HGame struct {
	GameDate      string `json:"gameDate"`
	Score         string `json:"score"`
	AtVs          string `json:"atVs"`
	GameResult    string `json:"gameResult"`
	HomeTeamID    string `json:"homeTeamId"`
	AwayTeamID    string `json:"awayTeamId"`
	HomeTeamScore string `json:"homeTeamScore"`
	AwayTeamScore string `json:"awayTeamScore"`
}

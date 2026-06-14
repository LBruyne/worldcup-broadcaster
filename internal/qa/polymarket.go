package qa

import (
	"encoding/json"
	"os"
	"regexp"
)

// oddsQuestionRe matches questions about odds / probabilities, which should be
// grounded in live Polymarket market data instead of a fabricated number.
var oddsQuestionRe = regexp.MustCompile(`赔率|概率|夺冠|出线|晋级|金靴|盘口|胜算|几成|多大希望|能走多远|polymarket|Polymarket`)

// pmMarket is one outcome's implied probability.
type pmMarket struct {
	Name string  `json:"name"`
	Prob float64 `json:"prob"`
}

// pmData is the Polymarket odds blob written by the external fetcher.
type pmData struct {
	Source  string                `json:"source"`
	Updated string                `json:"updated"`
	Markets map[string][]pmMarket `json:"markets"`
}

// polymarketOdds returns the raw Polymarket odds JSON for grounding, or "" when
// the file is missing/unreadable/empty. Stale data is still returned (market
// odds move slowly); the Updated field lets the model note freshness.
func (h *Handler) polymarketOdds() string {
	if h.opts.PolymarketFile == "" {
		return ""
	}
	raw, err := os.ReadFile(h.opts.PolymarketFile)
	if err != nil {
		return ""
	}
	var d pmData
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Markets) == 0 {
		return ""
	}
	return string(raw)
}

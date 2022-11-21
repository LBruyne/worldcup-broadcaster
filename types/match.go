package types

type DataResponse struct {
	Status  string     `json:"status"`
	Data    []MatchDay `json:"data"`
	Message string     `json:"message"`
}

type MatchDay struct {
	Time     string  `json:"time"`
	DateText string  `json:"dateText"`
	List     []Match `json:"list"`
}

type Match struct {
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
	Time       string `json:"time"`
	StartTime  string `json:"startTime"`
	Date       string `json:"date"`
	MatchName  string `json:"matchName"`
	Left       Team   `json:"leftLogo"`
	Right      Team   `json:"rightLogo"`
}

type Team struct {
	Logo   string `json:"logo"`
	Name   string `json:"name"`
	Score  string `json:"score"`
	Stroke uint   `json:"stroke"`
}

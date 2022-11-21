package main

import "fmt"

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

type Broadcaster struct {
	dateText string
	dateDay  string

	Finished []Match
	NotStart []Match
}

func (b *Broadcaster) parseMessage() string {
	s := ""
	s += "早上好，今天是" + b.dateText + " " + b.dateDay + "\n"
	s += "昨日战报：\n"
	for _, m := range b.Finished {
		s += m.Date + " " + m.Time + " " + m.Left.Name + " " + m.Left.Score + " : " + m.Right.Score + " " + m.Right.Name + "\n"
	}
	s += "今日预告：\n"
	for _, m := range b.NotStart {
		s += m.Date + " " + m.Time + " " + m.Left.Name + " VS. " + m.Right.Name + "\n"
	}
	s += "今晚几个？"
	fmt.Println(s)
	return s
}

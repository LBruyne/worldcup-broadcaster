package types

import (
	"encoding/json"
	"fmt"
	"github.com/gocolly/colly"
	"log"
	"net/http"
	"net/url"
	"time"
)

const (
	DataBaseUrl = "https://tiyu.baidu.com/api/match/%E4%B8%96%E7%95%8C%E6%9D%AF"

	SendGroupMessageUrl = "/send_group_msg"

	StatusFinish   = "3"
	StatusNotStart = "4"
)

type Broadcaster struct {
	dateText string
	dateDay  string

	baseUrl string
	groupId string

	Finished []Match
	NotStart []Match
}

func NewBroadcaster(url string, groupId string) *Broadcaster {
	b := &Broadcaster{}
	b.dateText = time.Now().Format("01月02日")
	b.dateDay = time.Now().Weekday().String()
	b.groupId = groupId
	b.baseUrl = url
	return b
}

func (b *Broadcaster) ParseMessage() string {
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

func (b *Broadcaster) SendMessageToGroup() error {
	u := b.baseUrl + SendGroupMessageUrl
	v := url.Values{}
	v.Add("group_id", b.groupId)
	v.Add("message", b.ParseMessage())
	_, err := http.PostForm(u, v)
	if err != nil {
		return fmt.Errorf("send message to specified rpc: %w", err)
	}

	return nil
}

func (b *Broadcaster) Broadcast() error {
	// the specified timestamp
	today := time.Now().Format("2006-01-02")
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")

	c := colly.NewCollector()
	c.MaxDepth = 1
	c.OnRequest(func(r *colly.Request) {
		log.Println("Visiting", r.URL)
	})
	c.OnResponse(func(r *colly.Response) {
		res := DataResponse{}
		err := json.Unmarshal(r.Body, &res)
		if err != nil {
			log.Println("crawl data and parse res: %w", err)
		} else if res.Data == nil {
			log.Println("crawl data without response")
		}

		for _, d := range res.Data {
			for _, m := range d.List {
				if m.Status == StatusFinish {
					b.Finished = append(b.Finished, m)
				} else if m.Status == StatusNotStart {
					b.NotStart = append(b.NotStart, m)
				}
			}
		}
	})

	var err error
	// yesterday
	err = c.Visit(DataBaseUrl + "/live/date/" + today + "/direction/forward?from=self")
	if err != nil {
		return fmt.Errorf("yesterday data: %w", err)
	}

	// tomorrow
	err = c.Visit(DataBaseUrl + "/live/date/" + today + "/direction/after?from=self")
	if err != nil {
		return fmt.Errorf("yesterday data: %w", err)
	}

	// today
	err = c.Visit(DataBaseUrl + "/live/date/" + tomorrow + "/direction/forward?from=self")
	if err != nil {
		return fmt.Errorf("yesterday data: %w", err)
	}

	if err := b.SendMessageToGroup(); err != nil {
		log.Println(err)
	}

	return err
}

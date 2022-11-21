package main

import (
	"encoding/json"
	"fmt"
	"github.com/gocolly/colly"
	"net/http"
	"net/url"
	"time"
)

var (
	StatusFinish   = "3"
	StatusNotStart = "4"
)

func main() {

	b := Broadcaster{}
	today := time.Now().Format("2006-01-02")
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	b.dateText = time.Now().Format("01月02日")
	b.dateDay = time.Now().Weekday().String()

	c := colly.NewCollector()
	c.MaxDepth = 1
	c.OnRequest(func(r *colly.Request) {
		fmt.Println("Visiting", r.URL)
	})
	c.OnResponse(func(r *colly.Response) {
		res := DataResponse{}
		err := json.Unmarshal(r.Body, &res)
		if err != nil {
			fmt.Errorf("parse res: %w", err)
			return
		} else if res.Data == nil {
			fmt.Errorf("without res")
			return
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

	// yesterday
	c.Visit("https://tiyu.baidu.com/api/match/%E4%B8%96%E7%95%8C%E6%9D%AF/live/date/" + today + "/direction/forward?from=self")
	// tomorrow
	c.Visit("https://tiyu.baidu.com/api/match/%E4%B8%96%E7%95%8C%E6%9D%AF/live/date/" + today + "/direction/after?from=self")
	// today
	c.Visit("https://tiyu.baidu.com/api/match/%E4%B8%96%E7%95%8C%E6%9D%AF/live/date/" + tomorrow + "/direction/forward?from=self")

	u := "http://127.0.0.1:5700/send_group_msg"
	v := url.Values{}
	v.Add("group_id", "xxx")
	v.Add("message", b.parseMessage())
	_, _ = http.PostForm(u, v)
}

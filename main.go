package main

import (
	"github.com/robfig/cron/v3"
	"log"
	"time"
	"worldcup-broadcaster/types"
)

const (
	baseUrl = "http://127.0.0.1:5700" // baseUrl specified by CQHTTP provider

	GracefulShutdownTimeout = 60 * 60 * 24 * 30 * time.Second // 1 month
)

func main() {
	cronJob := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DiscardLogger),
	))
	_, err := cronJob.AddFunc("0 9 * * * ", func() {
		b := types.NewBroadcaster(baseUrl, "794925183")
		err := b.Broadcast()
		if err != nil {
			log.Printf("on excuting cronjob in %s, meets error: %w\n", time.Now().Format("2006-01-02 15-04"), err)
		}
	})

	if err != nil {
		panic(err)
	}

	cronJob.Start()
	log.Println("WorldCup broadcaster starts...")

	time.Sleep(GracefulShutdownTimeout)

	cronJob.Stop()
	log.Println("WorldCup broadcaster stops...")
}

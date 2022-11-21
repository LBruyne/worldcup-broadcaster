package types

import (
	"fmt"
	"github.com/robfig/cron/v3"
	"log"
	"testing"
	"time"
)

func TestCronJobRoutine(t *testing.T) {
	cronJob := cron.New()
	_, err := cronJob.AddFunc("@every 5s", func() {
		fmt.Println("Hello world")
	})

	if err != nil {
		panic(err)
	}

	cronJob.Start()
	log.Println("cronjob is starting......")

	time.Sleep(20 * time.Second)

	cronJob.Stop()
	log.Println("cronjob is stopping......")
}

func TestBroadcaster_Broadcast(t *testing.T) {
	cronJob := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DiscardLogger),
	))
	_, err := cronJob.AddFunc("@every 5s", func() {
		b := NewBroadcaster("http://127.0.0.1:5700", "625553734")
		err := b.Broadcast()
		if err != nil {
			log.Printf("on excuting cronjob in %s, meets error: %s\n", time.Now().Format("2006-01-02 15-04"), err.Error())
		}
	})

	if err != nil {
		panic(err)
	}

	cronJob.Start()
	log.Println("broadcaster starts...")

	time.Sleep(20 * time.Second)

	cronJob.Stop()
	log.Println("cronjob stops...")
}

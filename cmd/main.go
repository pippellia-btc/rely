package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely/pkg/rely"
)

func main() {
	relay := rely.NewRelay()
	relay.OnEvent = Save
	relay.OnFilters = Query

	go relay.Run()

	log.Println("running relay on port 3334")
	if err := http.ListenAndServe("localhost:3334", relay); err != nil {
		panic(err)
	}
}

func Save(c *rely.Client, e *nostr.Event) error {
	log.Println(e)
	return nil
}

func Query(ctx context.Context, c *rely.Client, f nostr.Filters) ([]nostr.Event, error) {
	time.Sleep(5 * time.Second)

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("context was cancelled")

	default:
		return []nostr.Event{{Kind: 1, ID: "hello"}}, nil
	}
}

package main

import (
	"context"
	"log"
	"net/http"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

func main() {
	relay := rely.NewRelay()
	relay.OnEvent = Save
	relay.OnFilters = Query

	go relay.Run()
	log.Println("running on localhost:3334")

	if err := http.ListenAndServe("localhost:3334", relay); err != nil {
		panic(err)
	}
}

func Save(c *rely.Client, e *nostr.Event) error {
	log.Printf("received event: %v", e)
	return nil
}

func Query(ctx context.Context, c *rely.Client, f nostr.Filters) ([]nostr.Event, error) {
	log.Printf("received filters %v", f)
	return nil, nil
}

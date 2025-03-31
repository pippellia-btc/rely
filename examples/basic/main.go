package main

import (
	"context"
	"log"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	go rely.HandleSignals(cancel)

	relay := rely.NewRelay()
	relay.OnEvent = Save
	relay.OnFilters = Query

	log.Printf("running relay on %s", relay.Address)
	if err := relay.StartAndServe(ctx); err != nil {
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

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

var blackList = []string{"IP_123", "IP_abc"}

func main() {
	relay := rely.NewRelay()
	relay.RejectConnection = append(relay.RejectConnection, BadIP)
	relay.OnEvent = Save
	relay.OnFilters = Query

	log.Printf("running relay on %s", relay.Address)
	if err := relay.StartAndServe(context.Background()); err != nil {
		panic(err)
	}
}

func BadIP(r *http.Request) error {
	if slices.Contains(blackList, rely.IP(r)) {
		return fmt.Errorf("IP is blacklisted")
	}
	return nil
}

func Save(c *rely.Client, e *nostr.Event) error {
	log.Printf("received event: %v", e)
	return nil
}

func Query(ctx context.Context, c *rely.Client, f nostr.Filters) ([]nostr.Event, error) {
	log.Printf("received filters %v", f)
	return nil, nil
}

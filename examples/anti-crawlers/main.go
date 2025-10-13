package main

import (
	"context"
	"errors"
	"log"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
This relay performs filter-based rate-limiting.
When a client sends too many filters, the relay rejects the request.
*/

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rely.HandleSignals(cancel)

	relay := rely.NewRelay()
	relay.RejectReq = append(relay.RejectReq, TooMany)

	addr := "localhost:3334"
	log.Printf("running relay on %s", addr)

	if err := relay.StartAndServe(ctx, addr); err != nil {
		panic(err)
	}
}

// TooMany rejects the REQ if the client has too many open filters.
func TooMany(client rely.Client, filters nostr.Filters) error {
	total := len(filters)
	// for _, sub := range client.Subscriptions() {
	// 	total += len(sub.Filters)
	// }

	if total > 10 {
		client.Disconnect()
		return errors.New("rate-limited: too many open filters")
	}
	return nil
}

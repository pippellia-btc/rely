package main

import (
	"context"
	"log"
	"math/rand/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
This examples shows how to enable NIP-45
*/

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rely.HandleSignals(cancel)

	relay := rely.NewRelay()
	relay.OnCount = Count

	addr := "localhost:3334"
	log.Printf("running relay on %s", addr)

	if err := relay.StartAndServe(ctx, addr); err != nil {
		panic(err)
	}
}

func Count(ctx context.Context, c rely.Client, f nostr.Filters) (count int64, approx bool, err error) {
	log.Printf("received count with filters %v", f)
	count = rand.Int64N(10000)
	return count, (count % 2) == 1, nil
}

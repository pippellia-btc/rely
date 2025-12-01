package main

import (
	"context"
	"log"
	"math/rand/v2"
	"os/signal"
	"syscall"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
This examples shows how to enable NIP-45, which is as simple as registering a function
in the relay.On.Count hook.
*/

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	relay := rely.NewRelay()
	relay.On.Count = Count

	if err := relay.StartAndServe(ctx, "localhost:3334"); err != nil {
		panic(err)
	}
}

func Count(c rely.Client, f nostr.Filters) (count int64, approx bool, err error) {
	log.Printf("received count with filters %v", f)
	count = rand.Int64N(10000)
	return count, (count % 2) == 1, nil
}

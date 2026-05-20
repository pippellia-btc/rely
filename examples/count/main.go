package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
This examples shows how to enable NIP-45, which is as simple as registering a function
in the relay.On.Count hook.
*/

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	relay := rely.NewRelay()
	relay.On.Count = Count

	if err := relay.StartAndServe(ctx, "localhost:3334"); err != nil {
		panic(err)
	}
}

func Count(c rely.Client, id string, f nostr.Filters) (count int64, approx bool, err error) {
	slog.Info("received count", "id", id, "filters", f)
	count = rand.Int64N(10000)
	return count, (count % 2) == 1, nil
}

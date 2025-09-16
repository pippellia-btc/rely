package main

import (
	"context"
	"errors"
	"log"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
A "sparing" relay that avoids responding to REQs when the client's
response buffer is nearly full. This helps prevent overwhelming
slow clients and demonstrates how to use Client.RemainingCapacity()
to apply simple backpressure.
*/

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rely.HandleSignals(cancel)

	relay := rely.NewRelay(
		rely.WithClientResponseLimit(100), // decreased from default 1000
	)

	relay.RejectReq = append(relay.RejectReq, TooGreedy)
	relay.OnEvent = Save
	relay.OnReq = Query

	addr := "localhost:3334"
	log.Printf("running relay on %s", addr)

	if err := relay.StartAndServe(ctx, addr); err != nil {
		panic(err)
	}
}

func TooGreedy(client rely.Client, filters nostr.Filters) error {
	if client.RemainingCapacity() < 10 {
		return errors.New("slow down there chief")
	}
	return nil
}

func Save(c rely.Client, e *nostr.Event) error {
	log.Printf("received event: %v", e)
	return nil
}

func Query(ctx context.Context, c rely.Client, f nostr.Filters) ([]nostr.Event, error) {
	log.Printf("received filters %v", f)
	return nil, nil
}

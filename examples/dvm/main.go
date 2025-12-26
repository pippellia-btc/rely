package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"os/signal"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
This programs shows how nostr events can be used as requests for short or long lived
asynchronous jobs like DVMs. The store is obviously a joke.
*/

var (
	store []*nostr.Event
	relay *rely.Relay
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	store = make([]*nostr.Event, 0, 1000)

	relay = rely.NewRelay(
		rely.WithQueueCapacity(10_000), // increase capacity to absorb traffic bursts (higher RAM)
		rely.WithMaxProcessors(10),     // increase concurrent processors for faster execution (higher CPU)
	)

	relay.On.Event = Process
	relay.On.Req = Query

	if err := relay.StartAndServe(ctx, "localhost:3334"); err != nil {
		panic(err)
	}
}

func Process(_ rely.Client, request *nostr.Event) error {
	switch request.Kind {
	case 5500:
		// malware scanning DVM
		response := MalwareScan(request)

		// store so it can later be retrieved
		store = append(store, response)

		relay.Broadcast(response)
		return nil

	default:
		return errors.New("unsupported kind")
	}
}

func Query(ctx context.Context, _ rely.Client, filters nostr.Filters) ([]nostr.Event, error) {
	events := make([]nostr.Event, 0, 100) // pre-allocating
	for _, event := range store {
		if filters.Match(event) {
			events = append(events, *event)
		}
	}
	return events, nil
}

func MalwareScan(request *nostr.Event) *nostr.Event {
	switch rand.IntN(3) {
	case 0:
		// no malware detected
		return responseEvent(request, true)

	case 1:
		// malware detected
		return responseEvent(request, false)

	default:
		// an error occurred
		return errorEvent(request)
	}
}

func responseEvent(request *nostr.Event, isMalware bool) *nostr.Event {
	content := "all good"
	if isMalware {
		content = "found virus"
	}

	return &nostr.Event{
		Content: content,
		Kind:    request.Kind + 1000,
		Tags:    nostr.Tags{{"e", request.ID}, {"p", request.PubKey}},
	}
}

func errorEvent(request *nostr.Event) *nostr.Event {
	return &nostr.Event{
		Kind: 7000,
		Tags: nostr.Tags{{"e", request.ID}, {"p", request.PubKey}, {"status", "whatever"}},
	}
}

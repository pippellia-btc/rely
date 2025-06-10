package main

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
	. "github.com/pippellia-btc/rely"
)

/*
This programs shows how nostr events can be used as requests for short or long lived
asyncronous jobs like DVMs.
*/

var (
	store []*nostr.Event
	relay *rely.Relay
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go HandleSignals(cancel)

	store = make([]*nostr.Event, 0, 1000)

	relay = NewRelay(
		WithQueueCapacity(10_000), // increase capacity to absorb traffic bursts (higher RAM)
		WithMaxProcessors(10),     // increase concurrent processors for faster execution (higher CPU)
	)

	relay.OnEvent = Process

	addr := "localhost:3334"
	log.Printf("running relay on %s", addr)

	if err := relay.StartAndServe(ctx, addr); err != nil {
		panic(err)
	}
}

func Process(client rely.Client, request *nostr.Event) error {
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

func MalwareScan(request *nostr.Event) *nostr.Event {
	switch rand.IntN(3) {
	case 0:
		// no malware detected
		return responseEvent(request, true)

	case 1:
		// malware detected
		return responseEvent(request, false)

	default:
		// an error occured
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

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely/pkg/ws"
)

func main() {
	relay := ws.NewRelay()
	relay.Save = Save
	relay.Query = Query

	go relay.Run()

	log.Println("running relay on port 3334")
	if err := http.ListenAndServe("localhost:3334", relay); err != nil {
		panic(err)
	}
}

func Save(e *nostr.Event) error {
	log.Println(e)
	return nil
}

func Query(ctx context.Context, f nostr.Filters) ([]nostr.Event, error) {
	time.Sleep(10 * time.Second)

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("context was cancelled")

	default:
		return []nostr.Event{{ID: "hello"}}, nil
	}
}

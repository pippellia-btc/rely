package main

import (
	"context"
	"log"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
	"github.com/pippellia-btc/rely"
	. "github.com/pippellia-btc/rely"
)

/*
This example shows how to configure NIP-11 relay information document.
*/

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go HandleSignals(cancel)

	info := nip11.RelayInformationDocument{
		Name:          "Rely",
		Description:   "this is just an example",
		PubKey:        "f683e87035f7ad4f44e0b98cfbd9537e16455a92cd38cefc4cb31db7557f5ef2",
		SupportedNIPs: []any{1, 11, 42},
	}

	relay := NewRelay(
		WithInfo(info),
	)

	relay.On.Event = Save
	relay.On.Req = Query

	if err := relay.StartAndServe(ctx, "localhost:3334"); err != nil {
		panic(err)
	}
}

func Save(c rely.Client, e *nostr.Event) error {
	log.Printf("received event: %v", e)
	return nil
}

func Query(ctx context.Context, c rely.Client, f nostr.Filters) ([]nostr.Event, error) {
	log.Printf("received filters %v", f)
	return nil, nil
}

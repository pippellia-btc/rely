package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
	. "github.com/pippellia-btc/rely"
)

/*
This relay enforces privacy rules when a client requests NIP-04 DMs:
- the client MUST be authenticated
- the client can only fetch the DMs signed by its own pubkey
*/

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go HandleSignals(cancel)

	relay := NewRelay(
		WithDomain("example.com"), // the domain must be set to correctly validate NIP-42
	)

	relay.OnConnect = func(c Client) { c.SendAuth() }
	relay.OnAuth = func(c Client) { log.Printf("client authed with pubkey %s", c.Pubkey()) }
	relay.RejectReq = append(relay.RejectReq, AuthedOnDMs)

	addr := "localhost:3334"
	log.Printf("running relay on %s", addr)

	if err := relay.StartAndServe(ctx, addr); err != nil {
		panic(err)
	}
}

func AuthedOnDMs(client rely.Client, filters nostr.Filters) error {
	for _, filter := range filters {
		if !slices.Contains(filter.Kinds, 4) {
			continue
		}

		pubkey := client.Pubkey()
		if pubkey == "" {
			// the client is not authenticated, so it can't request DMs
			return errors.New("auth-required: you must be authenticated to query for DMs")
		}

		if len(filter.Authors) != 1 || filter.Authors[0] != pubkey {
			// the client is requesting DMs of other people
			return fmt.Errorf("restricted: you can only request the DMs of the pubkey %s", pubkey)
		}
	}

	return nil
}

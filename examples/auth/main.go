package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/signal"
	"slices"
	"syscall"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
This relay enforces privacy rules when a client requests NIP-04 DMs:
- the client MUST be authenticated
- the client can only fetch the DMs signed by its own pubkey
*/

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	relay := rely.NewRelay(
		rely.WithDomain("relay.example.com"), // the domain must be set to correctly validate NIP-42
	)

	relay.On.Connect = func(c rely.Client) { c.SendAuth() }
	relay.On.Auth = func(c rely.Client) { slog.Info("client authed", "pubkeys", c.Pubkeys()) }
	relay.Reject.Req = append(relay.Reject.Req, UnauthedDMs)

	if err := relay.StartAndServe(ctx, "localhost:3334"); err != nil {
		panic(err)
	}
}

func UnauthedDMs(client rely.Client, filters nostr.Filters) error {
	for _, filter := range filters {
		if !slices.Contains(filter.Kinds, 4) {
			continue
		}

		if !client.IsAuthed() {
			return errors.New("auth-required: you must be authenticated to query for kind-4 DMs")
		}

		if len(filter.Authors) == 0 {
			return errors.New("restricted: you must specify the author to query for kind-4 DMs")
		}

		pubkeys := client.Pubkeys()
		for _, author := range filter.Authors {
			if !slices.Contains(pubkeys, author) {
				return fmt.Errorf("restricted: you can only request the DMs of the pubkeys %s", pubkeys)
			}
		}
	}
	return nil
}

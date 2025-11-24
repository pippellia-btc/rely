package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
This example presents a reputation-based rate limiter using a two-tier token bucket system:
- client must authenticate with exactly one pubkey (via NIP-42)
- each pubkey has an associate bucket holding tokens
- writing an event comsumes a token
- tokens are refilled periodically based on the pubkey's rank (global pagerank)
- ranks are fetched in batches from a service provider (Vertex)
- to prevent abuse (e.g. attackers forcing mass ranking of pubkeys), IP-based rate
limiting is applied to the ranking process

Note: Unknown pubkeys are treated as having zero reputation,
while unknown IPs are initially given a positive reputation.

Source: https://vertexlab.io/blog/reputation_rate_limit
*/

const relayBudget = 10_000_000 // maximum events per day
const ipTokens = 100

var cache *RankCache
var limiter *Limiter

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cache = NewRankCache(ctx)
	limiter = NewLimiter(ctx)

	relay := rely.NewRelay(
		rely.WithDomain("relay.example.com"), // required for validating NIP-42 auth
		rely.WithMaxClientPubkeys(1),         // enforcing max one pubkey per client
	)

	relay.Reject.Connection = append(relay.Reject.Connection, func(_ rely.Stats, r *http.Request) error {
		// rate limiting IPs
		if limiter.Allow(rely.IP(r), ipRefill) {
			return nil
		}
		return errors.New("rate-limited: please try again in a few hours")
	})

	// send an AUTH challenge as soon as the client connects
	relay.On.Connect = func(c rely.Client) { c.SendAuth() }

	relay.On.Event = func(c rely.Client, e *nostr.Event) error {
		pubkeys := c.Pubkeys()
		if len(pubkeys) == 0 {
			return errors.New("auth-required: you must be authenticated with exactly one pubkey to write here")
		}

		pubkey := pubkeys[0]
		rank, exists := cache.Rank(pubkey)
		if !exists {
			// If the client IP has enough tokens, the pubkey is queued for ranking by Vertex;
			// otherwise we disconnect the client as this is probably an attacker trying to waste our backend budget.
			if !limiter.Allow(c.IP(), ipRefill) {
				c.Disconnect()
				return errors.New("rate-limited: please try again in a few hours")
			}

			cache.refresh <- pubkey
		}

		if !limiter.Allow(pubkey, pkRefill(rank)) {
			return errors.New("rate-limited: please try again in a few hours")
		}
		return Save(e)
	}

	if err := relay.StartAndServe(ctx, "localhost:3334"); err != nil {
		panic(err)
	}
}

func pkRefill(rank float64) Refill {
	return func(b *Bucket) {
		if time.Since(b.lastRequest) > 24*time.Hour {
			b.tokens = int(rank * relayBudget)
		}
	}
}

func ipRefill(b *Bucket) {
	if time.Since(b.lastRequest) > 24*time.Hour {
		b.tokens = ipTokens
	}
}

func Save(e *nostr.Event) error {
	log.Println(e)
	return nil
}

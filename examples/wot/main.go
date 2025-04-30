package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
This example presents a reputation-based rate limiter using a two-tier token bucket system:
- client must authenticate with a pubkey (via NIP-42)
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

const relayBudget = 10000000
const ipTokens = 100

var cache *RankCache
var limiter *Limiter

var ErrAuthRequired = errors.New("auth-required:")
var ErrRateLimited = errors.New("rate-limited: please try again in a few hours")

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rely.HandleSignals(cancel)

	cache = NewRankCache(ctx)
	limiter = NewLimiter(ctx)

	relay := rely.NewRelay()
	relay.RejectConnection = append(relay.RejectConnection, func(_ rely.Stats, r *http.Request) error {
		// rate limiting IPs
		if limiter.Allow(rely.IP(r), ipRefill) {
			return nil
		}
		return ErrRateLimited
	})

	relay.OnConnect = func(c *rely.Client) error {
		// send an AUTH challange as soon as the client connects
		c.SendAuthChallenge()
		return nil
	}

	relay.OnEvent = func(c *rely.Client, e *nostr.Event) error {
		pubkey := c.Pubkey()
		if pubkey == nil {
			c.SendAuthChallenge()
			return ErrAuthRequired
		}

		rank, exists := cache.Rank(*pubkey)
		if !exists {
			// If the client IP has enough tokens, the pubkey is queued for ranking by Vertex;
			// otherwise we disconnect the client as this is probably an attacker trying to waste our backend budget.
			if !limiter.Allow(c.IP(), ipRefill) {
				c.Disconnect()
				return ErrRateLimited
			}

			cache.refresh <- *pubkey
		}

		if !limiter.Allow(*pubkey, pkRefill(rank)) {
			return ErrRateLimited
		}

		return Save(e)
	}

	addr := "localhost:3335"
	log.Printf("running relay on %s", addr)

	if err := relay.StartAndServe(ctx, addr); err != nil {
		panic(err)
	}
}

func pkRefill(rank float64) Refill {
	return func(b *Bucket) {
		if time.Since(b.lastRequest) > time.Hour {
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

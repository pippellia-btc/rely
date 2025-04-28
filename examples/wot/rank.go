package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

type RankCache struct {
	mu      sync.RWMutex
	ranks   map[string]TimeRank
	refresh chan string

	StaleThreshold     time.Duration
	MaxRefreshInterval time.Duration
}

type TimeRank struct {
	Timestamp time.Time
	Rank      float64
}

type PubRank struct {
	Pubkey string  `json:"pubkey"`
	Rank   float64 `json:"rank"`
}

func NewRankCache(ctx context.Context) *RankCache {
	cache := &RankCache{
		ranks:              make(map[string]TimeRank, 100),
		refresh:            make(chan string, 100),
		StaleThreshold:     24 * time.Hour,
		MaxRefreshInterval: 7 * 24 * time.Hour,
	}

	go cache.refresher(ctx)
	return cache
}

// Rank returns the rank of the pubkey if it exists in the cache.
// If the rank is too old, its pubkey is sent to the refresher queue.
func (c *RankCache) Rank(pubkey string) (float64, bool) {
	c.mu.RLock()
	rank, exists := c.ranks[pubkey]
	c.mu.RUnlock()

	if !exists {
		return 0, false
	}

	if time.Since(rank.Timestamp) > c.StaleThreshold {
		c.refresh <- pubkey
	}

	return rank.Rank, true
}

// Update uses the provided ranks to update the cache.
func (c *RankCache) Update(ts time.Time, ranks ...PubRank) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, r := range ranks {
		c.ranks[r.Pubkey] = TimeRank{Rank: r.Rank, Timestamp: ts}
	}
}

// Clean scans the ranks of the cache and removed old ones.
func (c *RankCache) Clean() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for pk, rank := range c.ranks {
		if time.Since(rank.Timestamp) > c.MaxRefreshInterval {
			delete(c.ranks, pk)
		}
	}
}

const MaxPubkeysToRank = 1000

// The cache refresher updates the ranks via the service provider and deletes
// old ranks. It fires when one of the following condition is met:
// - enough unique pubkeys need updated ranks
// - enough time has passed since the last refresh
func (c *RankCache) refresher(ctx context.Context) {
	batch := make([]string, 0, MaxPubkeysToRank)
	timer := time.After(c.MaxRefreshInterval)

	for {
		select {
		case <-ctx.Done():
			return

		case pubkey, ok := <-c.refresh:
			if !ok {
				log.Println("the sync channel was closed")
				return
			}

			if slices.Contains(batch, pubkey) {
				continue
			}

			batch = append(batch, pubkey)
			if len(batch) >= MaxPubkeysToRank {
				if err := c.refreshBatch(ctx, batch); err != nil {
					log.Printf("failed to refresh cache: %v", err)
					continue
				}

				batch = make([]string, 0, MaxPubkeysToRank)
				timer = time.After(c.MaxRefreshInterval)
			}

		case <-timer:
			if err := c.refreshBatch(ctx, batch); err != nil {
				log.Printf("failed to refresh cache: %v", err)
				continue
			}

			batch = make([]string, 0, MaxPubkeysToRank)
			timer = time.After(c.MaxRefreshInterval)
		}
	}
}

const vertex = "wss://relay.vertexlab.io"
const sk = "93a8fe430a6b27324c4a8fa1a68ed0088a258052bbee3bd82bc2ac49eef40613"

func (c *RankCache) refreshBatch(ctx context.Context, batch []string) error {
	if len(batch) < 1 {
		return nil
	}

	tags := make(nostr.Tags, len(batch))
	for i, pk := range batch {
		tags[i] = nostr.Tag{"param", "target", pk}
	}

	request := &nostr.Event{
		Kind:      5314,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}

	if err := request.Sign(sk); err != nil {
		return fmt.Errorf("failed to sign: %w", err)
	}

	response, err := dvmResponse(ctx, request, "http://localhost:3334")
	if err != nil {
		return err
	}

	switch response.Kind {
	case 6314:
		var ranks []PubRank
		if err := json.Unmarshal([]byte(response.Content), &ranks); err != nil {
			return fmt.Errorf("failed to unmarshal the DVM content (ID %s): %w", response.ID, err)
		}

		c.Update(response.CreatedAt.Time(), ranks...)
		c.Clean()
		return nil

	case 7000:
		return fmt.Errorf("got dvm error: %v", response.Tags.Find("status"))

	default:
		return fmt.Errorf("got unexpected kind: %d", request.Kind)
	}
}

// dvmResponse() connects to the relay, send the request and fetches the response using the request ID.
func dvmResponse(ctx context.Context, request *nostr.Event, relayURL string) (response *nostr.Event, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", relayURL, err)
	}

	if err := relay.Publish(ctx, *request); err != nil {
		return nil, fmt.Errorf("failed to publish to %s: %v", relayURL, err)
	}

	filter := nostr.Filter{
		Kinds: []int{request.Kind + 1000, 7000},
		Tags:  nostr.TagMap{"e": {request.ID}},
	}

	results, err := relay.QuerySync(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch the response: %w", err)
	}

	if len(results) != 1 {
		return nil, fmt.Errorf("failed to fetch the response: expected 1 response, got %d", len(results))
	}

	return results[0], nil
}

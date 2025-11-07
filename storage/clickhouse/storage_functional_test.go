package clickhouse

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// TestFunctionalIngestFromRelay tests ingesting real events from relay.nostr.net
func TestFunctionalIngestFromRelay(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	// Skip in short mode
	if testing.Short() {
		t.Skip("Skipping functional test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	relayURL := "wss://relay.nostr.net"
	t.Logf("Connecting to %s...", relayURL)

	// Connect to relay
	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer relay.Close()

	t.Logf("Connected to %s", relayURL)

	// Subscribe to recent events
	now := nostr.Now()
	since := now - 3600 // Last hour

	filters := []nostr.Filter{
		{
			Kinds: []int{1}, // Text notes
			Since: &since,
			Limit: 100,
		},
	}

	sub, err := relay.Subscribe(ctx, filters)
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	t.Logf("Subscribed, waiting for events...")

	// Collect events
	eventCount := 0
	eventIDs := make([]string, 0)
	timeout := time.After(10 * time.Second)

	for {
		select {
		case <-ctx.Done():
			t.Logf("Context cancelled")
			goto done
		case <-timeout:
			t.Logf("Timeout waiting for events")
			goto done
		case event := <-sub.Events:
			if event == nil {
				continue
			}

			eventCount++
			eventIDs = append(eventIDs, event.ID)

			// Save to storage
			if err := testStorage.SaveEvent(nil, event); err != nil {
				t.Errorf("Failed to save event %s: %v", event.ID, err)
			}

			t.Logf("Ingested event %d: %s (kind=%d, author=%s)", eventCount, event.ID, event.Kind, event.PubKey[:8])

			// Stop after receiving enough events
			if eventCount >= 20 {
				goto done
			}
		}
	}

done:
	sub.Unsub()

	if eventCount == 0 {
		t.Fatal("Did not receive any events from relay")
	}

	t.Logf("Ingested %d events from %s", eventCount, relayURL)

	// Wait for batch flush
	time.Sleep(500 * time.Millisecond)

	// Verify events were saved
	queryCtx := context.Background()
	for i, eventID := range eventIDs {
		if i >= 5 {
			break // Just check first 5
		}

		filters := nostr.Filters{{IDs: []string{eventID}}}
		events, err := testStorage.QueryEvents(queryCtx, nil, filters)
		if err != nil {
			t.Errorf("Failed to query event %s: %v", eventID, err)
			continue
		}

		if len(events) == 0 {
			t.Errorf("Event %s not found in storage", eventID)
			continue
		}

		// Verify event data
		event := events[0]
		if event.ID != eventID {
			t.Errorf("Event ID mismatch: got %s, want %s", event.ID, eventID)
		}

		t.Logf("Verified event %s in storage", eventID)
	}

	// Test counting ingested events
	countFilters := nostr.Filters{{Kinds: []int{1}}}
	count, _, err := testStorage.CountEvents(nil, countFilters)
	if err != nil {
		t.Errorf("Failed to count events: %v", err)
	} else {
		t.Logf("Total kind-1 events in storage: %d", count)
	}
}

// TestFunctionalIngestMultipleKinds tests ingesting various event kinds
func TestFunctionalIngestMultipleKinds(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	if testing.Short() {
		t.Skip("Skipping functional test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	relayURL := "wss://relay.nostr.net"
	t.Logf("Connecting to %s...", relayURL)

	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer relay.Close()

	// Subscribe to multiple kinds
	now := nostr.Now()
	since := now - 7200 // Last 2 hours

	filters := []nostr.Filter{
		{
			Kinds: []int{1, 3, 7}, // Text notes, contacts, reactions
			Since: &since,
			Limit: 50,
		},
	}

	sub, err := relay.Subscribe(ctx, filters)
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	kindCounts := make(map[int]int)
	timeout := time.After(15 * time.Second)
	totalEvents := 0

	for {
		select {
		case <-ctx.Done():
			goto done
		case <-timeout:
			goto done
		case event := <-sub.Events:
			if event == nil {
				continue
			}

			totalEvents++
			kindCounts[event.Kind]++

			if err := testStorage.SaveEvent(nil, event); err != nil {
				t.Errorf("Failed to save event: %v", err)
			}

			if totalEvents >= 30 {
				goto done
			}
		}
	}

done:
	sub.Unsub()

	if totalEvents == 0 {
		t.Fatal("Did not receive any events")
	}

	t.Logf("Ingested %d total events:", totalEvents)
	for kind, count := range kindCounts {
		t.Logf("  Kind %d: %d events", kind, count)
	}

	// Wait for batch flush
	time.Sleep(500 * time.Millisecond)

	// Verify different kinds can be queried
	queryCtx := context.Background()
	for kind := range kindCounts {
		filters := nostr.Filters{{Kinds: []int{kind}}}
		events, err := testStorage.QueryEvents(queryCtx, nil, filters)
		if err != nil {
			t.Errorf("Failed to query kind %d: %v", kind, err)
			continue
		}

		if len(events) == 0 {
			t.Errorf("No events found for kind %d", kind)
		} else {
			t.Logf("Successfully queried %d events of kind %d", len(events), kind)
		}
	}
}

// TestFunctionalIngestWithTags tests ingesting and querying events with tags
func TestFunctionalIngestWithTags(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	if testing.Short() {
		t.Skip("Skipping functional test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	relayURL := "wss://relay.nostr.net"
	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer relay.Close()

	// Subscribe to events with tags (replies and mentions)
	now := nostr.Now()
	since := now - 3600

	filters := []nostr.Filter{
		{
			Kinds: []int{1}, // Text notes
			Since: &since,
			Limit: 50,
		},
	}

	sub, err := relay.Subscribe(ctx, filters)
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	eventsWithPTags := 0
	eventsWithETags := 0
	timeout := time.After(15 * time.Second)
	samplePTag := ""
	sampleETag := ""

	for {
		select {
		case <-ctx.Done():
			goto done
		case <-timeout:
			goto done
		case event := <-sub.Events:
			if event == nil {
				continue
			}

			// Check for p and e tags
			hasPTag := false
			hasETag := false

			for _, tag := range event.Tags {
				if len(tag) < 2 {
					continue
				}
				if tag[0] == "p" {
					hasPTag = true
					if samplePTag == "" {
						samplePTag = tag[1]
					}
				}
				if tag[0] == "e" {
					hasETag = true
					if sampleETag == "" {
						sampleETag = tag[1]
					}
				}
			}

			if hasPTag {
				eventsWithPTags++
			}
			if hasETag {
				eventsWithETags++
			}

			if err := testStorage.SaveEvent(nil, event); err != nil {
				t.Errorf("Failed to save event: %v", err)
			}

			if eventsWithPTags >= 5 && eventsWithETags >= 5 {
				goto done
			}
		}
	}

done:
	sub.Unsub()

	t.Logf("Found %d events with p tags, %d with e tags", eventsWithPTags, eventsWithETags)

	// Wait for batch flush
	time.Sleep(500 * time.Millisecond)

	// Test querying by tags
	queryCtx := context.Background()

	if samplePTag != "" {
		filters := nostr.Filters{{
			Tags: nostr.TagMap{"p": []string{samplePTag}},
		}}
		events, err := testStorage.QueryEvents(queryCtx, nil, filters)
		if err != nil {
			t.Errorf("Failed to query by p tag: %v", err)
		} else {
			t.Logf("Found %d events with p tag %s", len(events), samplePTag[:8])
		}
	}

	if sampleETag != "" {
		filters := nostr.Filters{{
			Tags: nostr.TagMap{"e": []string{sampleETag}},
		}}
		events, err := testStorage.QueryEvents(queryCtx, nil, filters)
		if err != nil {
			t.Errorf("Failed to query by e tag: %v", err)
		} else {
			t.Logf("Found %d events with e tag %s", len(events), sampleETag[:8])
		}
	}
}

// TestFunctionalStressIngest tests high-volume ingestion
func TestFunctionalStressIngest(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	relayURL := "wss://relay.nostr.net"
	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer relay.Close()

	// Subscribe to recent events (high volume)
	now := nostr.Now()
	since := now - 86400 // Last 24 hours

	filters := []nostr.Filter{
		{
			Kinds: []int{1, 3, 6, 7}, // Multiple kinds
			Since: &since,
			Limit: 500,
		},
	}

	sub, err := relay.Subscribe(ctx, filters)
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	startTime := time.Now()
	eventCount := 0
	errorCount := 0
	timeout := time.After(30 * time.Second)

	for {
		select {
		case <-ctx.Done():
			goto done
		case <-timeout:
			goto done
		case event := <-sub.Events:
			if event == nil {
				continue
			}

			eventCount++

			if err := testStorage.SaveEvent(nil, event); err != nil {
				errorCount++
				if errorCount < 5 { // Only log first few errors
					t.Logf("Error saving event: %v", err)
				}
			}

			if eventCount%100 == 0 {
				elapsed := time.Since(startTime)
				rate := float64(eventCount) / elapsed.Seconds()
				t.Logf("Ingested %d events in %s (%.2f events/sec)", eventCount, elapsed, rate)
			}

			if eventCount >= 500 {
				goto done
			}
		}
	}

done:
	sub.Unsub()

	elapsed := time.Since(startTime)
	rate := float64(eventCount) / elapsed.Seconds()

	t.Logf("Stress test complete:")
	t.Logf("  Total events: %d", eventCount)
	t.Logf("  Duration: %s", elapsed)
	t.Logf("  Rate: %.2f events/sec", rate)
	t.Logf("  Errors: %d", errorCount)

	if eventCount == 0 {
		t.Fatal("Did not ingest any events")
	}

	// Error rate should be low
	errorRate := float64(errorCount) / float64(eventCount)
	if errorRate > 0.01 { // More than 1% error rate
		t.Errorf("Error rate too high: %.2f%%", errorRate*100)
	}

	// Wait for final batch flush
	time.Sleep(1 * time.Second)

	// Verify storage stats
	stats, err := testStorage.Stats()
	if err != nil {
		t.Errorf("Failed to get stats: %v", err)
	} else {
		t.Logf("Storage stats: %+v", stats)
	}
}

// TestFunctionalQueryAfterIngest tests that ingested events are immediately queryable
func TestFunctionalQueryAfterIngest(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	if testing.Short() {
		t.Skip("Skipping functional test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	relayURL := "wss://relay.nostr.net"
	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		t.Fatalf("Failed to connect to relay: %v", err)
	}
	defer relay.Close()

	// Get a recent event
	now := nostr.Now()
	since := now - 1800 // Last 30 minutes

	filters := []nostr.Filter{
		{
			Kinds: []int{1},
			Since: &since,
			Limit: 1,
		},
	}

	sub, err := relay.Subscribe(ctx, filters)
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	timeout := time.After(10 * time.Second)
	var ingestedEvent *nostr.Event

	select {
	case <-timeout:
		t.Fatal("Timeout waiting for event")
	case event := <-sub.Events:
		if event == nil {
			t.Fatal("Received nil event")
		}
		ingestedEvent = event
		t.Logf("Received event %s", event.ID)
	}

	sub.Unsub()

	// Save the event
	if err := testStorage.SaveEvent(nil, ingestedEvent); err != nil {
		t.Fatalf("Failed to save event: %v", err)
	}

	// Wait for batch flush
	time.Sleep(200 * time.Millisecond)

	// Immediately query the event
	queryCtx := context.Background()
	queryFilters := nostr.Filters{{IDs: []string{ingestedEvent.ID}}}

	events, err := testStorage.QueryEvents(queryCtx, nil, queryFilters)
	if err != nil {
		t.Fatalf("Failed to query event: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("Event not found immediately after ingestion")
	}

	// Verify event matches
	found := events[0]
	if found.ID != ingestedEvent.ID {
		t.Errorf("Event ID mismatch: got %s, want %s", found.ID, ingestedEvent.ID)
	}
	if found.PubKey != ingestedEvent.PubKey {
		t.Errorf("PubKey mismatch: got %s, want %s", found.PubKey, ingestedEvent.PubKey)
	}
	if found.Kind != ingestedEvent.Kind {
		t.Errorf("Kind mismatch: got %d, want %d", found.Kind, ingestedEvent.Kind)
	}
	if found.Content != ingestedEvent.Content {
		t.Errorf("Content mismatch")
	}

	t.Logf("Successfully verified event immediately after ingestion")
}

package clickhouse

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// TestInsertQueryFlow tests the complete insert → query flow
func TestInsertQueryFlow(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	ctx := context.Background()
	testCases := []struct {
		name   string
		events []nostr.Event
		filter nostr.Filter
	}{
		{
			name: "query by ID",
			events: []nostr.Event{
				createTestEvent(t, 1, "test content 1"),
			},
			filter: nostr.Filter{},
		},
		{
			name: "query by author",
			events: []nostr.Event{
				createTestEvent(t, 1, "author test 1"),
				createTestEvent(t, 1, "author test 2"),
			},
			filter: nostr.Filter{},
		},
		{
			name: "query by kind",
			events: []nostr.Event{
				createTestEvent(t, 1, "kind 1 event"),
				createTestEvent(t, 7, "kind 7 event"),
			},
			filter: nostr.Filter{Kinds: []int{1}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Insert events
			for i := range tc.events {
				if err := testStorage.SaveEvent(nil, &tc.events[i]); err != nil {
					t.Fatalf("Failed to save event: %v", err)
				}
			}

			// Wait for batch flush
			time.Sleep(200 * time.Millisecond)

			// Setup filter
			if len(tc.filter.IDs) == 0 && len(tc.events) > 0 {
				tc.filter.IDs = []string{tc.events[0].ID}
			}
			if len(tc.filter.Authors) == 0 && len(tc.events) > 0 {
				tc.filter.Authors = []string{tc.events[0].PubKey}
			}

			// Query events
			filters := nostr.Filters{tc.filter}
			events, err := testStorage.QueryEvents(ctx, nil, filters)
			if err != nil {
				t.Fatalf("Query failed: %v", err)
			}

			// Verify we got results
			if len(events) == 0 {
				t.Error("Expected at least one event in results")
			}

			// Verify event matches
			found := false
			for _, event := range events {
				if event.ID == tc.events[0].ID {
					found = true
					if event.Content != tc.events[0].Content {
						t.Errorf("Content mismatch: got %s, want %s", event.Content, tc.events[0].Content)
					}
					break
				}
			}

			if !found {
				t.Error("Expected event not found in results")
			}
		})
	}
}

// TestInsertCountFlow tests the insert → count flow
func TestInsertCountFlow(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	// Create unique events with specific tags
	events := make([]nostr.Event, 10)
	testTag := fmt.Sprintf("test_%d", time.Now().Unix())

	for i := 0; i < 10; i++ {
		event := createTestEvent(t, 1, fmt.Sprintf("count test %d", i))
		event.Tags = nostr.Tags{{"t", testTag}}
		if err := event.Sign(nostr.GeneratePrivateKey()); err != nil {
			t.Fatalf("Failed to sign event: %v", err)
		}
		events[i] = event

		if err := testStorage.SaveEvent(nil, &events[i]); err != nil {
			t.Fatalf("Failed to save event: %v", err)
		}
	}

	// Wait for batch flush
	time.Sleep(200 * time.Millisecond)

	// Count by tag
	filters := nostr.Filters{
		{
			Tags:  nostr.TagMap{"t": []string{testTag}},
			Kinds: []int{1},
		},
	}

	count, approximate, err := testStorage.CountEvents(nil, filters)
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}

	if count != 10 {
		t.Errorf("Expected count of 10, got %d", count)
	}

	if approximate {
		t.Error("Expected exact count, got approximate")
	}
}

// TestFilterCombinations tests various filter combinations
func TestFilterCombinations(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	ctx := context.Background()

	// Create test events with various properties
	now := time.Now()
	since := nostr.Timestamp(now.Add(-1 * time.Hour).Unix())
	until := nostr.Timestamp(now.Add(1 * time.Hour).Unix())

	event1 := createTestEvent(t, 1, "test 1")
	event1.Tags = nostr.Tags{{"p", "pubkey123"}, {"t", "nostr"}}
	event1.Sign(nostr.GeneratePrivateKey())

	event2 := createTestEvent(t, 7, "test 2")
	event2.Tags = nostr.Tags{{"e", "event456"}}
	event2.Sign(nostr.GeneratePrivateKey())

	// Insert events
	testStorage.SaveEvent(nil, &event1)
	testStorage.SaveEvent(nil, &event2)
	time.Sleep(200 * time.Millisecond)

	testCases := []struct {
		name        string
		filter      nostr.Filter
		expectCount int
	}{
		{
			name: "filter by kind",
			filter: nostr.Filter{
				Kinds: []int{1},
			},
			expectCount: 1,
		},
		{
			name: "filter by time range",
			filter: nostr.Filter{
				Since: &since,
				Until: &until,
			},
			expectCount: 2,
		},
		{
			name: "filter by p tag",
			filter: nostr.Filter{
				Tags: nostr.TagMap{"p": []string{"pubkey123"}},
			},
			expectCount: 1,
		},
		{
			name: "filter by multiple conditions",
			filter: nostr.Filter{
				Kinds: []int{1},
				Tags:  nostr.TagMap{"t": []string{"nostr"}},
			},
			expectCount: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			filters := nostr.Filters{tc.filter}
			events, err := testStorage.QueryEvents(ctx, nil, filters)
			if err != nil {
				t.Fatalf("Query failed: %v", err)
			}

			if len(events) < tc.expectCount {
				t.Errorf("Expected at least %d events, got %d", tc.expectCount, len(events))
			}
		})
	}
}

// TestConcurrentInserts tests concurrent event insertion
func TestConcurrentInserts(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	const numGoroutines = 10
	const eventsPerGoroutine = 50

	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*eventsPerGoroutine)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < eventsPerGoroutine; j++ {
				event := createTestEvent(t, 1, fmt.Sprintf("concurrent test %d-%d", workerID, j))
				if err := testStorage.SaveEvent(nil, &event); err != nil {
					errors <- err
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Errorf("Concurrent insert error: %v", err)
		errorCount++
	}

	if errorCount > 0 {
		t.Fatalf("Got %d errors during concurrent inserts", errorCount)
	}

	// Wait for batch flush
	time.Sleep(500 * time.Millisecond)

	// Verify events were saved
	ctx := context.Background()
	filters := nostr.Filters{{Kinds: []int{1}}}
	events, err := testStorage.QueryEvents(ctx, nil, filters)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	expectedMin := numGoroutines * eventsPerGoroutine
	if len(events) < expectedMin {
		t.Errorf("Expected at least %d events, got %d", expectedMin, len(events))
	}
}

// TestDeduplication tests event deduplication
func TestDeduplication(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	ctx := context.Background()

	// Create same event twice
	event := createTestEvent(t, 1, "duplicate test")

	// Insert twice
	testStorage.SaveEvent(nil, &event)
	testStorage.SaveEvent(nil, &event)

	// Wait for batch flush
	time.Sleep(200 * time.Millisecond)

	// Query by ID
	filters := nostr.Filters{{IDs: []string{event.ID}}}
	events, err := testStorage.QueryEvents(ctx, nil, filters)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	// Should only get one event (deduplicated)
	if len(events) > 1 {
		t.Errorf("Expected 1 event after deduplication, got %d", len(events))
	}
}

// TestLargeQueryResults tests querying large result sets
func TestLargeQueryResults(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	ctx := context.Background()
	const numEvents = 1000

	// Insert many events
	testPrefix := fmt.Sprintf("large_query_%d", time.Now().Unix())
	for i := 0; i < numEvents; i++ {
		event := createTestEvent(t, 1, fmt.Sprintf("%s_%d", testPrefix, i))
		testStorage.SaveEvent(nil, &event)
	}

	// Wait for batch flush
	time.Sleep(1 * time.Second)

	// Query with high limit
	filters := nostr.Filters{{Kinds: []int{1}, Limit: 5000}}
	events, err := testStorage.QueryEvents(ctx, nil, filters)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(events) < numEvents {
		t.Logf("Got %d events, expected at least %d", len(events), numEvents)
	}
}

// TestComplexTagFilters tests complex tag filtering scenarios
func TestComplexTagFilters(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	ctx := context.Background()

	// Create events with various tag combinations
	event1 := createTestEvent(t, 1, "multi-tag test 1")
	event1.Tags = nostr.Tags{
		{"e", "event1"},
		{"p", "pubkey1"},
		{"t", "bitcoin"},
		{"t", "nostr"},
	}
	event1.Sign(nostr.GeneratePrivateKey())

	event2 := createTestEvent(t, 1, "multi-tag test 2")
	event2.Tags = nostr.Tags{
		{"e", "event2"},
		{"p", "pubkey2"},
		{"t", "nostr"},
	}
	event2.Sign(nostr.GeneratePrivateKey())

	// Insert events
	testStorage.SaveEvent(nil, &event1)
	testStorage.SaveEvent(nil, &event2)
	time.Sleep(200 * time.Millisecond)

	// Query by t tag
	filters := nostr.Filters{{
		Tags: nostr.TagMap{"t": []string{"nostr"}},
	}}

	events, err := testStorage.QueryEvents(ctx, nil, filters)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(events) < 2 {
		t.Errorf("Expected at least 2 events with #nostr tag, got %d", len(events))
	}
}

// TestQueryPerformance benchmarks query performance
func TestQueryPerformance(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	ctx := context.Background()

	// Insert test data
	for i := 0; i < 100; i++ {
		event := createTestEvent(t, 1, fmt.Sprintf("perf test %d", i))
		testStorage.SaveEvent(nil, &event)
	}
	time.Sleep(300 * time.Millisecond)

	// Measure query time
	start := time.Now()
	filters := nostr.Filters{{Kinds: []int{1}, Limit: 100}}
	_, err := testStorage.QueryEvents(ctx, nil, filters)
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	t.Logf("Query took %s", duration)

	// Query should complete in reasonable time (< 1 second for small dataset)
	if duration > 1*time.Second {
		t.Errorf("Query took too long: %s", duration)
	}
}

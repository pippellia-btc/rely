package clickhouse

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

var testStorage *Storage

// TestMain sets up test environment
func TestMain(m *testing.M) {
	// Check if ClickHouse is available
	dsn := os.Getenv("CLICKHOUSE_DSN")
	if dsn == "" {
		dsn = "clickhouse://localhost:9000/nostr"
	}

	// Create storage instance
	cfg := Config{
		DSN:           dsn,
		BatchSize:     100,
		FlushInterval: 100 * time.Millisecond,
		MaxOpenConns:  10,
		MaxIdleConns:  5,
	}

	var err error
	testStorage, err = NewStorage(cfg)
	if err != nil {
		// Skip tests if ClickHouse is not available
		os.Exit(0)
	}

	// Run migrations
	if err := runMigrations(testStorage); err != nil {
		testStorage.Close()
		panic(err)
	}

	// Run tests
	code := m.Run()

	// Cleanup
	testStorage.Close()

	os.Exit(code)
}

// runMigrations runs all SQL migrations
func runMigrations(s *Storage) error {
	ctx := context.Background()

	migrations := []string{
		// Create database (should already exist)
		`CREATE DATABASE IF NOT EXISTS nostr`,

		// Create events table
		`CREATE TABLE IF NOT EXISTS nostr.events (
			id String,
			pubkey String,
			created_at UInt32,
			kind UInt16,
			content String,
			sig String,
			tags Array(Array(String)),
			tag_e Array(String),
			tag_p Array(String),
			tag_a Array(String),
			tag_t Array(String),
			tag_d String,
			tag_g Array(String),
			tag_r Array(String),
			relay_received_at UInt32,
			version UInt32,
			deleted UInt8 DEFAULT 0
		) ENGINE = ReplacingMergeTree(version, deleted)
		ORDER BY (id, created_at)
		PRIMARY KEY (id)`,

		// Create by_author view
		`CREATE TABLE IF NOT EXISTS nostr.events_by_author (
			id String,
			pubkey String,
			created_at UInt32,
			kind UInt16,
			content String,
			sig String,
			tags Array(Array(String)),
			tag_e Array(String),
			tag_p Array(String),
			tag_a Array(String),
			tag_t Array(String),
			tag_d String,
			tag_g Array(String),
			tag_r Array(String),
			relay_received_at UInt32,
			version UInt32,
			deleted UInt8 DEFAULT 0
		) ENGINE = ReplacingMergeTree(version, deleted)
		ORDER BY (pubkey, created_at)
		PRIMARY KEY (pubkey)`,

		// Create by_kind view
		`CREATE TABLE IF NOT EXISTS nostr.events_by_kind (
			id String,
			pubkey String,
			created_at UInt32,
			kind UInt16,
			content String,
			sig String,
			tags Array(Array(String)),
			tag_e Array(String),
			tag_p Array(String),
			tag_a Array(String),
			tag_t Array(String),
			tag_d String,
			tag_g Array(String),
			tag_r Array(String),
			relay_received_at UInt32,
			version UInt32,
			deleted UInt8 DEFAULT 0
		) ENGINE = ReplacingMergeTree(version, deleted)
		ORDER BY (kind, created_at)
		PRIMARY KEY (kind)`,
	}

	for _, migration := range migrations {
		if _, err := s.db.ExecContext(ctx, migration); err != nil {
			return err
		}
	}

	return nil
}

// TestNewStorage tests storage creation
func TestNewStorage(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DSN = "clickhouse://localhost:9000/nostr"

	storage, err := NewStorage(cfg)
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	defer storage.Close()

	if storage.db == nil {
		t.Error("Expected non-nil database connection")
	}
}

// TestPing tests database connectivity
func TestPing(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	ctx := context.Background()
	if err := testStorage.Ping(ctx); err != nil {
		t.Errorf("Ping failed: %v", err)
	}
}

// TestExtractAllTags tests tag extraction
func TestExtractAllTags(t *testing.T) {
	tests := []struct {
		name     string
		tags     nostr.Tags
		expected ExtractedTags
	}{
		{
			name: "empty tags",
			tags: nostr.Tags{},
			expected: ExtractedTags{
				e:         []string{},
				p:         []string{},
				a:         []string{},
				t:         []string{},
				g:         []string{},
				r:         []string{},
				d:         "",
				tagsArray: [][]string{},
			},
		},
		{
			name: "single e tag",
			tags: nostr.Tags{{"e", "event123"}},
			expected: ExtractedTags{
				e:         []string{"event123"},
				p:         []string{},
				a:         []string{},
				t:         []string{},
				g:         []string{},
				r:         []string{},
				d:         "",
				tagsArray: [][]string{{"e", "event123"}},
			},
		},
		{
			name: "multiple tag types",
			tags: nostr.Tags{
				{"e", "event123"},
				{"p", "pubkey456"},
				{"t", "bitcoin"},
				{"d", "identifier"},
			},
			expected: ExtractedTags{
				e:         []string{"event123"},
				p:         []string{"pubkey456"},
				a:         []string{},
				t:         []string{"bitcoin"},
				g:         []string{},
				r:         []string{},
				d:         "identifier",
				tagsArray: [][]string{{"e", "event123"}, {"p", "pubkey456"}, {"t", "bitcoin"}, {"d", "identifier"}},
			},
		},
		{
			name: "multiple d tags - only first used",
			tags: nostr.Tags{
				{"d", "first"},
				{"d", "second"},
			},
			expected: ExtractedTags{
				e:         []string{},
				p:         []string{},
				a:         []string{},
				t:         []string{},
				g:         []string{},
				r:         []string{},
				d:         "first",
				tagsArray: [][]string{{"d", "first"}, {"d", "second"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractAllTags(tt.tags)

			// Check each field
			if len(result.e) != len(tt.expected.e) {
				t.Errorf("e tags: got %v, want %v", result.e, tt.expected.e)
			}
			if len(result.p) != len(tt.expected.p) {
				t.Errorf("p tags: got %v, want %v", result.p, tt.expected.p)
			}
			if result.d != tt.expected.d {
				t.Errorf("d tag: got %v, want %v", result.d, tt.expected.d)
			}
		})
	}
}

// TestSaveEvent tests event saving
func TestSaveEvent(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	event := createTestEvent(t, 1, "Test content")

	err := testStorage.SaveEvent(nil, &event)
	if err != nil {
		t.Errorf("SaveEvent failed: %v", err)
	}

	// Wait for batch to flush
	time.Sleep(200 * time.Millisecond)
}

// TestQueryEvents tests event querying
func TestQueryEvents(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	// Save test event
	event := createTestEvent(t, 1, "Query test content")
	err := testStorage.SaveEvent(nil, &event)
	if err != nil {
		t.Fatalf("SaveEvent failed: %v", err)
	}

	// Wait for batch to flush
	time.Sleep(200 * time.Millisecond)

	// Query by ID
	filters := nostr.Filters{
		{IDs: []string{event.ID}},
	}

	ctx := context.Background()
	events, err := testStorage.QueryEvents(ctx, nil, filters)
	if err != nil {
		t.Fatalf("QueryEvents failed: %v", err)
	}

	if len(events) == 0 {
		t.Error("Expected at least one event")
	}

	// Verify event data
	if len(events) > 0 {
		found := events[0]
		if found.ID != event.ID {
			t.Errorf("ID mismatch: got %s, want %s", found.ID, event.ID)
		}
		if found.Content != event.Content {
			t.Errorf("Content mismatch: got %s, want %s", found.Content, event.Content)
		}
	}
}

// TestCountEvents tests event counting
func TestCountEvents(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	// Save test events
	for i := 0; i < 5; i++ {
		event := createTestEvent(t, 1, "Count test")
		testStorage.SaveEvent(nil, &event)
	}

	// Wait for batch to flush
	time.Sleep(200 * time.Millisecond)

	// Count events
	filters := nostr.Filters{
		{Kinds: []int{1}},
	}

	count, approximate, err := testStorage.CountEvents(nil, filters)
	if err != nil {
		t.Fatalf("CountEvents failed: %v", err)
	}

	if count < 5 {
		t.Errorf("Expected at least 5 events, got %d", count)
	}

	if approximate {
		t.Error("Expected exact count, got approximate")
	}
}

// TestStats tests storage statistics
func TestStats(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	// Save some events
	for i := 0; i < 3; i++ {
		event := createTestEvent(t, 1, "Stats test")
		testStorage.SaveEvent(nil, &event)
	}

	// Wait for batch to flush
	time.Sleep(200 * time.Millisecond)

	stats, err := testStorage.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if stats.TotalEvents == 0 {
		t.Error("Expected non-zero total events")
	}

	t.Logf("Stats: %+v", stats)
}

// TestClose tests graceful shutdown
func TestClose(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DSN = "clickhouse://localhost:9000/nostr"

	storage, err := NewStorage(cfg)
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}

	// Close should not error
	if err := storage.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

// Helper function to create test events
func createTestEvent(t *testing.T, kind int, content string) nostr.Event {
	t.Helper()

	event := nostr.Event{
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Kind:      kind,
		Tags:      nostr.Tags{},
		Content:   content,
	}

	// Sign the event
	sk := nostr.GeneratePrivateKey()
	if err := event.Sign(sk); err != nil {
		t.Fatalf("Failed to sign event: %v", err)
	}

	return event
}

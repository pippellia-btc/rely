package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

// Storage implements ClickHouse-backed storage for Nostr events
type Storage struct {
	db *sql.DB

	// Batch insertion configuration
	batchSize     int
	flushInterval time.Duration
	batchChan     chan *nostr.Event
	stopBatch     chan struct{}
	batchDone     chan struct{}
}

// Config holds ClickHouse connection configuration
type Config struct {
	// Connection string, e.g. "clickhouse://localhost:9000/nostr"
	DSN string

	// Batch insertion settings
	BatchSize     int           // Number of events to batch before inserting (default: 1000)
	FlushInterval time.Duration // Max time to wait before flushing batch (default: 1s)

	// Connection pool settings
	MaxOpenConns int // Maximum number of open connections (default: 10)
	MaxIdleConns int // Maximum number of idle connections (default: 5)
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	return Config{
		DSN:           "clickhouse://localhost:9000/nostr",
		BatchSize:     1000,
		FlushInterval: 1 * time.Second,
		MaxOpenConns:  10,
		MaxIdleConns:  5,
	}
}

// NewStorage creates a new ClickHouse storage instance
func NewStorage(cfg Config) (*Storage, error) {
	// Open database connection
	db, err := sql.Open("clickhouse", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open clickhouse connection: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(time.Hour)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping clickhouse: %w", err)
	}

	storage := &Storage{
		db:            db,
		batchSize:     cfg.BatchSize,
		flushInterval: cfg.FlushInterval,
		batchChan:     make(chan *nostr.Event, cfg.BatchSize*2),
		stopBatch:     make(chan struct{}),
		batchDone:     make(chan struct{}),
	}

	// Start batch inserter
	go storage.batchInserter()

	log.Printf("ClickHouse storage initialized (batch_size=%d, flush_interval=%s)",
		cfg.BatchSize, cfg.FlushInterval)

	return storage, nil
}

// Close gracefully shuts down the storage
func (s *Storage) Close() error {
	// Stop batch inserter
	close(s.stopBatch)
	<-s.batchDone

	// Close database
	return s.db.Close()
}

// SaveEvent stores a single event (non-blocking, queues for batch insert)
func (s *Storage) SaveEvent(c rely.Client, event *nostr.Event) error {
	select {
	case s.batchChan <- event:
		return nil
	default:
		// Channel is full, log warning and try direct insert
		log.Printf("batch channel full, falling back to direct insert for event %s", event.ID)
		return s.insertEvent(context.Background(), event)
	}
}

// QueryEvents retrieves events matching the given filters
func (s *Storage) QueryEvents(ctx context.Context, c rely.Client, filters nostr.Filters) ([]nostr.Event, error) {
	var allEvents []nostr.Event

	// Query each filter separately
	for _, filter := range filters {
		events, err := s.queryFilter(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("failed to query filter: %w", err)
		}
		allEvents = append(allEvents, events...)
	}

	// Deduplicate by event ID (keep first occurrence)
	allEvents = deduplicateEvents(allEvents)

	return allEvents, nil
}

// CountEvents returns the count of events matching the given filters
func (s *Storage) CountEvents(c rely.Client, filters nostr.Filters) (int64, bool, error) {
	ctx := context.Background()

	var totalCount int64

	// Count each filter separately
	for _, filter := range filters {
		count, err := s.countFilter(ctx, filter)
		if err != nil {
			return 0, false, fmt.Errorf("failed to count filter: %w", err)
		}
		totalCount += count
	}

	// Return approximate=false since we're doing exact counting
	return totalCount, false, nil
}

// Ping checks if the database connection is alive
func (s *Storage) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Stats returns storage statistics
func (s *Storage) Stats() (StorageStats, error) {
	ctx := context.Background()

	var stats StorageStats

	// Get total event count
	err := s.db.QueryRowContext(ctx, `
		SELECT count() FROM nostr.events FINAL WHERE deleted = 0
	`).Scan(&stats.TotalEvents)
	if err != nil {
		return stats, fmt.Errorf("failed to get total events: %w", err)
	}

	// Get total storage size
	err = s.db.QueryRowContext(ctx, `
		SELECT sum(bytes) FROM system.parts
		WHERE database = 'nostr' AND active = 1
	`).Scan(&stats.TotalBytes)
	if err != nil {
		return stats, fmt.Errorf("failed to get total bytes: %w", err)
	}

	// Get oldest and newest event timestamps
	err = s.db.QueryRowContext(ctx, `
		SELECT min(created_at), max(created_at)
		FROM nostr.events FINAL
		WHERE deleted = 0
	`).Scan(&stats.OldestEvent, &stats.NewestEvent)
	if err != nil && err != sql.ErrNoRows {
		return stats, fmt.Errorf("failed to get time range: %w", err)
	}

	return stats, nil
}

// StorageStats holds storage statistics
type StorageStats struct {
	TotalEvents uint64 // Total number of events
	TotalBytes  uint64 // Total storage size in bytes
	OldestEvent uint32 // Timestamp of oldest event
	NewestEvent uint32 // Timestamp of newest event
}

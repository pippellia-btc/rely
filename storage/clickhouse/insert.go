package clickhouse

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// batchInserter continuously batches and inserts events
func (s *Storage) batchInserter() {
	defer close(s.batchDone)

	buffer := make([]*nostr.Event, 0, s.batchSize)
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(buffer) == 0 {
			return
		}

		start := time.Now()
		if err := s.batchInsert(context.Background(), buffer); err != nil {
			log.Printf("batch insert error: %v", err)
		} else {
			log.Printf("inserted batch of %d events in %s", len(buffer), time.Since(start))
		}

		buffer = buffer[:0]
	}

	for {
		select {
		case <-s.stopBatch:
			// Flush remaining events before stopping
			flush()
			return

		case <-ticker.C:
			// Periodic flush
			flush()

		case event, ok := <-s.batchChan:
			if !ok {
				flush()
				return
			}

			buffer = append(buffer, event)
			if len(buffer) >= s.batchSize {
				flush()
			}
		}
	}
}

// batchInsert inserts a batch of events in a single transaction
func (s *Storage) batchInsert(ctx context.Context, events []*nostr.Event) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO nostr.events (
			id, pubkey, created_at, kind, content, sig,
			tags, tag_e, tag_p, tag_a, tag_t, tag_d, tag_g, tag_r,
			relay_received_at, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	now := uint32(time.Now().Unix())

	for _, event := range events {
		// Extract tag arrays
		tagE := extractTagValues(event.Tags, "e")
		tagP := extractTagValues(event.Tags, "p")
		tagA := extractTagValues(event.Tags, "a")
		tagT := extractTagValues(event.Tags, "t")
		tagD := getFirstTagValue(event.Tags, "d")
		tagG := extractTagValues(event.Tags, "g")
		tagR := extractTagValues(event.Tags, "r")

		// Convert tags to [][]string for ClickHouse Array(Array(String))
		tagsArray := tagsToArrayArray(event.Tags)

		_, err := stmt.ExecContext(ctx,
			event.ID,
			event.PubKey,
			uint32(event.CreatedAt),
			uint16(event.Kind),
			event.Content,
			event.Sig,
			tagsArray,
			tagE,
			tagP,
			tagA,
			tagT,
			tagD,
			tagG,
			tagR,
			now,  // relay_received_at
			now,  // version (used for deduplication)
		)
		if err != nil {
			return fmt.Errorf("failed to insert event %s: %w", event.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// insertEvent inserts a single event directly (used as fallback)
func (s *Storage) insertEvent(ctx context.Context, event *nostr.Event) error {
	return s.batchInsert(ctx, []*nostr.Event{event})
}

// extractTagValues extracts all values for a given tag name
func extractTagValues(tags nostr.Tags, tagName string) []string {
	var values []string
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == tagName {
			values = append(values, tag[1])
		}
	}
	return values
}

// getFirstTagValue returns the first value for a given tag name
func getFirstTagValue(tags nostr.Tags, tagName string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == tagName {
			return tag[1]
		}
	}
	return ""
}

// tagsToArrayArray converts nostr.Tags to [][]string for ClickHouse
func tagsToArrayArray(tags nostr.Tags) [][]string {
	result := make([][]string, len(tags))
	for i, tag := range tags {
		result[i] = []string(tag)
	}
	return result
}

package clickhouse

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nbd-wtf/go-nostr"
)

// queryFilter queries events for a single filter
func (s *Storage) queryFilter(ctx context.Context, filter nostr.Filter) ([]nostr.Event, error) {
	// Build optimized query
	table, query, args := s.buildQuery(filter)

	// Execute query
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query failed on table %s: %w", table, err)
	}
	defer rows.Close()

	// Parse results
	var events []nostr.Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return events, nil
}

// buildQuery constructs an optimized query based on the filter
func (s *Storage) buildQuery(filter nostr.Filter) (string, string, []interface{}) {
	var table string
	var args []interface{}

	// Choose optimal table based on filter characteristics
	switch {
	case len(filter.IDs) > 0:
		table = "nostr.events"
	case len(filter.Authors) > 0:
		table = "nostr.events_by_author"
	case len(filter.Kinds) > 0:
		table = "nostr.events_by_kind"
	default:
		// Check for tag filters
		if pTags := filter.Tags["p"]; len(pTags) > 0 {
			table = "nostr.events_by_tag_p"
		} else if eTags := filter.Tags["e"]; len(eTags) > 0 {
			table = "nostr.events_by_tag_e"
		} else {
			table = "nostr.events"
		}
	}

	// Build SELECT clause
	query := fmt.Sprintf(`
		SELECT
			id, pubkey, created_at, kind, content, sig, tags
		FROM %s FINAL
	`, table)

	// Build WHERE conditions
	var conditions []string
	conditions = append(conditions, "deleted = 0")

	// ID filter
	if len(filter.IDs) > 0 {
		placeholders := make([]string, len(filter.IDs))
		for i, id := range filter.IDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		conditions = append(conditions, fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ",")))
	}

	// Authors filter
	if len(filter.Authors) > 0 {
		placeholders := make([]string, len(filter.Authors))
		for i, author := range filter.Authors {
			placeholders[i] = "?"
			args = append(args, author)
		}
		conditions = append(conditions, fmt.Sprintf("pubkey IN (%s)", strings.Join(placeholders, ",")))
	}

	// Kinds filter
	if len(filter.Kinds) > 0 {
		placeholders := make([]string, len(filter.Kinds))
		for i, kind := range filter.Kinds {
			placeholders[i] = "?"
			args = append(args, uint16(kind))
		}
		conditions = append(conditions, fmt.Sprintf("kind IN (%s)", strings.Join(placeholders, ",")))
	}

	// Time range filters
	if filter.Since != nil {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, uint32(*filter.Since))
	}

	if filter.Until != nil {
		conditions = append(conditions, "created_at <= ?")
		args = append(args, uint32(*filter.Until))
	}

	// Tag filters
	if eTags := filter.Tags["e"]; len(eTags) > 0 {
		if table == "nostr.events_by_tag_e" {
			// Special handling for tag_e table
			placeholders := make([]string, len(eTags))
			for i, tag := range eTags {
				placeholders[i] = "?"
				args = append(args, tag)
			}
			conditions = append(conditions, fmt.Sprintf("tag_e_value IN (%s)", strings.Join(placeholders, ",")))
		} else {
			// Use hasAny for other tables
			conditions = append(conditions, "hasAny(tag_e, ?)")
			args = append(args, eTags)
		}
	}

	if pTags := filter.Tags["p"]; len(pTags) > 0 {
		if table == "nostr.events_by_tag_p" {
			// Special handling for tag_p table
			placeholders := make([]string, len(pTags))
			for i, tag := range pTags {
				placeholders[i] = "?"
				args = append(args, tag)
			}
			conditions = append(conditions, fmt.Sprintf("tag_p_value IN (%s)", strings.Join(placeholders, ",")))
		} else {
			// Use hasAny for other tables
			conditions = append(conditions, "hasAny(tag_p, ?)")
			args = append(args, pTags)
		}
	}

	if aTags := filter.Tags["a"]; len(aTags) > 0 {
		conditions = append(conditions, "hasAny(tag_a, ?)")
		args = append(args, aTags)
	}

	if tTags := filter.Tags["t"]; len(tTags) > 0 {
		conditions = append(conditions, "hasAny(tag_t, ?)")
		args = append(args, tTags)
	}

	if dTags := filter.Tags["d"]; len(dTags) > 0 {
		placeholders := make([]string, len(dTags))
		for i, tag := range dTags {
			placeholders[i] = "?"
			args = append(args, tag)
		}
		conditions = append(conditions, fmt.Sprintf("tag_d IN (%s)", strings.Join(placeholders, ",")))
	}

	// Search filter (full-text search)
	if filter.Search != "" {
		conditions = append(conditions, "hasToken(content, ?)")
		args = append(args, filter.Search)
	}

	// Add WHERE clause
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	// ORDER BY and LIMIT
	query += " ORDER BY created_at DESC"

	limit := filter.Limit
	if limit == 0 || limit > 5000 {
		limit = 5000 // Default/max limit
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	return table, query, args
}

// scanEvent scans a row into a nostr.Event
func scanEvent(rows *sql.Rows) (nostr.Event, error) {
	var event nostr.Event
	var createdAt uint32
	var kind uint16
	var tagsJSON string

	err := rows.Scan(
		&event.ID,
		&event.PubKey,
		&createdAt,
		&kind,
		&event.Content,
		&event.Sig,
		&tagsJSON,
	)
	if err != nil {
		return event, err
	}

	event.CreatedAt = nostr.Timestamp(createdAt)
	event.Kind = int(kind)

	// Parse tags from JSON
	if tagsJSON != "" {
		if err := json.Unmarshal([]byte(tagsJSON), &event.Tags); err != nil {
			return event, fmt.Errorf("failed to parse tags: %w", err)
		}
	}

	return event, nil
}

// deduplicateEvents removes duplicate events by ID (keeps first occurrence)
func deduplicateEvents(events []nostr.Event) []nostr.Event {
	if len(events) <= 1 {
		return events
	}

	seen := make(map[string]bool, len(events))
	result := make([]nostr.Event, 0, len(events))

	for _, event := range events {
		if !seen[event.ID] {
			seen[event.ID] = true
			result = append(result, event)
		}
	}

	return result
}

package clickhouse

import (
	"context"
	"fmt"
	"strings"

	"github.com/nbd-wtf/go-nostr"
)

// countFilter counts events matching a single filter
func (s *Storage) countFilter(ctx context.Context, filter nostr.Filter) (int64, error) {
	// Build count query (similar to regular query but with COUNT(*))
	table, query, args := s.buildCountQuery(filter)

	// Execute query
	var count int64
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count query failed on table %s: %w", table, err)
	}

	return count, nil
}

// buildCountQuery constructs an optimized count query based on the filter
func (s *Storage) buildCountQuery(filter nostr.Filter) (string, string, []interface{}) {
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

	// Build SELECT clause for counting
	query := fmt.Sprintf(`
		SELECT count()
		FROM %s FINAL
	`, table)

	// Build WHERE conditions (same logic as regular query)
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
			placeholders := make([]string, len(eTags))
			for i, tag := range eTags {
				placeholders[i] = "?"
				args = append(args, tag)
			}
			conditions = append(conditions, fmt.Sprintf("tag_e_value IN (%s)", strings.Join(placeholders, ",")))
		} else {
			conditions = append(conditions, "hasAny(tag_e, ?)")
			args = append(args, eTags)
		}
	}

	if pTags := filter.Tags["p"]; len(pTags) > 0 {
		if table == "nostr.events_by_tag_p" {
			placeholders := make([]string, len(pTags))
			for i, tag := range pTags {
				placeholders[i] = "?"
				args = append(args, tag)
			}
			conditions = append(conditions, fmt.Sprintf("tag_p_value IN (%s)", strings.Join(placeholders, ",")))
		} else {
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

	// Search filter
	if filter.Search != "" {
		conditions = append(conditions, "hasToken(content, ?)")
		args = append(args, filter.Search)
	}

	// Add WHERE clause
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	return table, query, args
}

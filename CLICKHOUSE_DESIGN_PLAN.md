# ClickHouse Storage Design for Nostr Relay - Performance Optimized

## Executive Summary

This document outlines a **highly optimized ClickHouse storage architecture** for a Nostr archival/analytics relay built on the `rely` framework. The design prioritizes:

1. **Maximum query performance** for all Nostr filter patterns
2. **Aggressive deduplication** to handle massive data volumes efficiently
3. **Analytics-ready schema** for generating reports
4. **Write throughput** to handle 8000+ concurrent clients

---

## Table of Contents

1. [Schema Design](#schema-design)
2. [Table Structure](#table-structure)
3. [Indexing Strategy](#indexing-strategy)
4. [Deduplication Strategy](#deduplication-strategy)
5. [Partitioning Strategy](#partitioning-strategy)
6. [Query Optimization](#query-optimization)
7. [Implementation Guide](#implementation-guide)
8. [Analytics Tables](#analytics-tables)
9. [Performance Tuning](#performance-tuning)
10. [Monitoring & Maintenance](#monitoring--maintenance)

---

## 1. Schema Design

### Primary Events Table (ReplacingMergeTree)

```sql
CREATE DATABASE IF NOT EXISTS nostr;

CREATE TABLE nostr.events
(
    -- Core Event Fields
    id              FixedString(64),     -- Event ID (SHA256 hex)
    pubkey          FixedString(64),     -- Author public key
    created_at      UInt32,              -- Unix timestamp
    kind            UInt16,              -- Event kind (0-65535)
    content         String,              -- Event content (can be large)
    sig             FixedString(128),    -- Signature

    -- Tag Storage (optimized for queries)
    tags            Array(Array(String)),  -- Full tags array

    -- Extracted Tag Indexes (for fast filtering)
    tag_e           Array(FixedString(64)), -- Event references
    tag_p           Array(FixedString(64)), -- Pubkey mentions
    tag_a           Array(String),          -- Address references (NIP-33)
    tag_t           Array(String),          -- Hashtags
    tag_d           String,                 -- Replaceable event identifier
    tag_g           Array(String),          -- Geohash locations
    tag_r           Array(String),          -- URL references

    -- Metadata for deduplication & analytics
    relay_received_at UInt32,             -- When relay received it
    deleted           UInt8 DEFAULT 0,     -- Soft delete flag (kind 5 events)

    -- Version for ReplacingMergeTree
    version         UInt32                 -- For deduplication (use relay_received_at)
)
ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(toDateTime(created_at))
PRIMARY KEY (id)
ORDER BY (id, created_at, kind, pubkey)
SETTINGS
    index_granularity = 8192,
    index_granularity_bytes = 10485760,
    min_bytes_for_wide_part = 0,
    min_rows_for_wide_part = 0;
```

### Why This Design?

1. **ReplacingMergeTree**: Automatically deduplicates events with same `id`, keeping the one with highest `version`
2. **FixedString for hashes**: More efficient than String for fixed-length hex strings
3. **Array columns for tags**: Enables fast `has()` and `hasAny()` queries
4. **Partitioning by month**: Efficient time-range queries and data lifecycle management
5. **Composite ORDER BY**: Supports multiple query patterns

---

## 2. Table Structure

### 2.1 Events Table (Main Storage)

The primary table shown above stores all events with full deduplication.

### 2.2 Materialized Views for Performance

#### A. Events by Author (for author queries)

```sql
CREATE TABLE nostr.events_by_author
(
    pubkey          FixedString(64),
    created_at      UInt32,
    kind            UInt16,
    id              FixedString(64),
    content         String,
    tags            Array(Array(String)),
    sig             FixedString(128),
    tag_e           Array(FixedString(64)),
    tag_p           Array(FixedString(64)),
    tag_t           Array(String),
    relay_received_at UInt32,
    deleted         UInt8,
    version         UInt32
)
ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(toDateTime(created_at))
PRIMARY KEY (pubkey)
ORDER BY (pubkey, created_at, kind, id)
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW nostr.events_by_author_mv TO nostr.events_by_author
AS SELECT
    pubkey, created_at, kind, id, content, tags, sig,
    tag_e, tag_p, tag_t, relay_received_at, deleted, version
FROM nostr.events;
```

#### B. Events by Kind (for kind-specific queries)

```sql
CREATE TABLE nostr.events_by_kind
(
    kind            UInt16,
    created_at      UInt32,
    id              FixedString(64),
    pubkey          FixedString(64),
    content         String,
    tags            Array(Array(String)),
    sig             FixedString(128),
    tag_e           Array(FixedString(64)),
    tag_p           Array(FixedString(64)),
    tag_t           Array(String),
    tag_d           String,
    relay_received_at UInt32,
    deleted         UInt8,
    version         UInt32
)
ENGINE = ReplacingMergeTree(version)
PARTITION BY kind
PRIMARY KEY (kind)
ORDER BY (kind, created_at, pubkey, id)
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW nostr.events_by_kind_mv TO nostr.events_by_kind
AS SELECT
    kind, created_at, id, pubkey, content, tags, sig,
    tag_e, tag_p, tag_t, tag_d, relay_received_at, deleted, version
FROM nostr.events;
```

#### C. Events by Tag-P (mentions/replies)

```sql
CREATE TABLE nostr.events_by_tag_p
(
    tag_p_value     FixedString(64),
    created_at      UInt32,
    id              FixedString(64),
    pubkey          FixedString(64),
    kind            UInt16,
    content         String,
    tags            Array(Array(String)),
    relay_received_at UInt32,
    version         UInt32
)
ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(toDateTime(created_at))
PRIMARY KEY (tag_p_value)
ORDER BY (tag_p_value, created_at, kind, id)
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW nostr.events_by_tag_p_mv TO nostr.events_by_tag_p
AS SELECT
    arrayJoin(tag_p) AS tag_p_value,
    created_at, id, pubkey, kind, content, tags, relay_received_at, version
FROM nostr.events
WHERE length(tag_p) > 0;
```

#### D. Events by Tag-E (event references)

```sql
CREATE TABLE nostr.events_by_tag_e
(
    tag_e_value     FixedString(64),
    created_at      UInt32,
    id              FixedString(64),
    pubkey          FixedString(64),
    kind            UInt16,
    content         String,
    tags            Array(Array(String)),
    relay_received_at UInt32,
    version         UInt32
)
ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(toDateTime(created_at))
PRIMARY KEY (tag_e_value)
ORDER BY (tag_e_value, created_at, kind, id)
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW nostr.events_by_tag_e_mv TO nostr.events_by_tag_e
AS SELECT
    arrayJoin(tag_e) AS tag_e_value,
    created_at, id, pubkey, kind, content, tags, relay_received_at, version
FROM nostr.events
WHERE length(tag_e) > 0;
```

---

## 3. Indexing Strategy

### 3.1 Primary Indexes

Each table has a PRIMARY KEY optimized for its access pattern:

- **events**: `(id, created_at, kind, pubkey)` - ID lookups first
- **events_by_author**: `(pubkey, created_at, kind, id)` - Author queries
- **events_by_kind**: `(kind, created_at, pubkey, id)` - Kind filtering
- **events_by_tag_p**: `(tag_p_value, created_at, kind, id)` - Mention queries
- **events_by_tag_e**: `(tag_e_value, created_at, kind, id)` - Reply chains

### 3.2 Secondary Indexes (Data Skipping)

```sql
-- Add to events table
ALTER TABLE nostr.events
    ADD INDEX idx_kind kind TYPE minmax GRANULARITY 4,
    ADD INDEX idx_pubkey pubkey TYPE bloom_filter(0.01) GRANULARITY 4,
    ADD INDEX idx_created_at created_at TYPE minmax GRANULARITY 4,
    ADD INDEX idx_tag_p tag_p TYPE bloom_filter(0.01) GRANULARITY 4,
    ADD INDEX idx_tag_e tag_e TYPE bloom_filter(0.01) GRANULARITY 4,
    ADD INDEX idx_content content TYPE tokenbf_v1(30000, 3, 0) GRANULARITY 4;
```

**Index Types:**
- **minmax**: For range queries (time, kind)
- **bloom_filter**: For exact match on arrays and strings
- **tokenbf_v1**: For full-text search in content

---

## 4. Deduplication Strategy

### 4.1 Primary Deduplication (ReplacingMergeTree)

**How it works:**
- Same event ID inserted multiple times → only latest version kept after merge
- `version` field = `relay_received_at` (higher = newer)
- Automatic background merges handle deduplication

**Force manual deduplication:**
```sql
OPTIMIZE TABLE nostr.events FINAL;
```

### 4.2 Insert Deduplication (ReplicatedReplacingMergeTree for clusters)

For distributed setups:

```sql
CREATE TABLE nostr.events ON CLUSTER cluster_name
(
    -- same schema
)
ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/nostr/events', '{replica}', version)
PARTITION BY toYYYYMM(toDateTime(created_at))
ORDER BY (id, created_at, kind, pubkey);
```

### 4.3 Application-Level Deduplication

Before inserting, check if event exists:

```sql
SELECT 1 FROM nostr.events WHERE id = ? LIMIT 1;
```

**Trade-off:** Adds latency but prevents duplicate inserts entirely.

**Recommendation:** Use async batch inserts without pre-checking, let ClickHouse handle deduplication.

---

## 5. Partitioning Strategy

### 5.1 Time-Based Partitioning

```sql
PARTITION BY toYYYYMM(toDateTime(created_at))
```

**Benefits:**
- Fast time-range queries (prunes irrelevant partitions)
- Efficient data deletion (drop old partitions)
- Better compression (similar data together)

### 5.2 Partition Management

**Archive old data:**
```sql
-- Move partitions to cold storage after 6 months
ALTER TABLE nostr.events MOVE PARTITION '202301' TO VOLUME 'cold';

-- Delete very old partitions
ALTER TABLE nostr.events DROP PARTITION '202001';
```

### 5.3 Multi-Level Partitioning (Advanced)

For truly massive scale:

```sql
PARTITION BY (toYear(toDateTime(created_at)), intDiv(kind, 1000))
```

Partitions by year AND kind range (e.g., 0-999, 1000-1999).

---

## 6. Query Optimization

### 6.1 Query Pattern Mapping

| Nostr Filter | Optimal Table | Index Used |
|--------------|---------------|------------|
| `ids: [...]` | `events` | PRIMARY KEY (id) |
| `authors: [...]` | `events_by_author` | PRIMARY KEY (pubkey) |
| `kinds: [...]` | `events_by_kind` | PRIMARY KEY (kind) |
| `#e: [...]` | `events_by_tag_e` | PRIMARY KEY (tag_e_value) |
| `#p: [...]` | `events_by_tag_p` | PRIMARY KEY (tag_p_value) |
| `since/until` | All tables | Partition pruning |
| `authors + kinds` | `events_by_author` | Composite ORDER BY |
| `search: "..."` | `events` | tokenbf_v1 index |

### 6.2 Example Queries

#### Query 1: Get events by author

```sql
SELECT id, created_at, kind, content, tags, sig
FROM nostr.events_by_author FINAL
WHERE pubkey = ?
  AND created_at >= ?
  AND created_at <= ?
  AND deleted = 0
ORDER BY created_at DESC
LIMIT 100;
```

#### Query 2: Get events by multiple authors and kinds

```sql
SELECT id, pubkey, created_at, kind, content, tags, sig
FROM nostr.events_by_author FINAL
WHERE pubkey IN (?, ?, ?)
  AND kind IN (1, 6, 7)
  AND created_at >= ?
  AND deleted = 0
ORDER BY created_at DESC
LIMIT 50;
```

#### Query 3: Get replies to an event

```sql
SELECT id, pubkey, created_at, kind, content, tags
FROM nostr.events_by_tag_e FINAL
WHERE tag_e_value = ?
  AND created_at >= ?
  AND deleted = 0
ORDER BY created_at ASC;
```

#### Query 4: Get mentions of a user

```sql
SELECT id, pubkey, created_at, kind, content, tags
FROM nostr.events_by_tag_p FINAL
WHERE tag_p_value = ?
  AND created_at >= ?
  AND deleted = 0
ORDER BY created_at DESC
LIMIT 100;
```

#### Query 5: Multiple filters (OR logic)

```sql
-- Filter 1: Events by alice with kind 1
-- Filter 2: Events by bob with kind 0

SELECT * FROM (
    SELECT id, pubkey, created_at, kind, content, tags, sig
    FROM nostr.events_by_author FINAL
    WHERE pubkey = 'alice' AND kind = 1 AND created_at >= ? AND deleted = 0

    UNION ALL

    SELECT id, pubkey, created_at, kind, content, tags, sig
    FROM nostr.events_by_author FINAL
    WHERE pubkey = 'bob' AND kind = 0 AND created_at >= ? AND deleted = 0
)
ORDER BY created_at DESC
LIMIT 50;
```

### 6.3 Query Best Practices

1. **Always use FINAL** for ReplacingMergeTree when reading to get deduplicated results
2. **Always filter on deleted = 0** to respect deletions
3. **Use LIMIT** to prevent massive result sets
4. **Order by created_at DESC** for most recent first (Nostr convention)
5. **Use PREWHERE** for high-cardinality filters:

```sql
SELECT * FROM nostr.events FINAL
PREWHERE kind IN (1, 6, 7)
WHERE pubkey IN (?, ?, ?)
  AND created_at >= ?
LIMIT 100;
```

---

## 7. Implementation Guide

### 7.1 Go Implementation Structure

```go
package storage

import (
    "context"
    "database/sql"
    "fmt"

    _ "github.com/ClickHouse/clickhouse-go/v2"
    "github.com/nbd-wtf/go-nostr"
)

type ClickHouseStorage struct {
    db *sql.DB
}

func NewClickHouseStorage(dsn string) (*ClickHouseStorage, error) {
    db, err := sql.Open("clickhouse", dsn)
    if err != nil {
        return nil, err
    }

    if err := db.Ping(); err != nil {
        return nil, err
    }

    return &ClickHouseStorage{db: db}, nil
}
```

### 7.2 Event Storage (On.Event Hook)

```go
func (s *ClickHouseStorage) SaveEvent(ctx context.Context, event *nostr.Event) error {
    // Extract tag arrays
    tagE := extractTagValues(event.Tags, "e")
    tagP := extractTagValues(event.Tags, "p")
    tagA := extractTagValues(event.Tags, "a")
    tagT := extractTagValues(event.Tags, "t")
    tagD := getFirstTagValue(event.Tags, "d")

    query := `
        INSERT INTO nostr.events (
            id, pubkey, created_at, kind, content, sig,
            tags, tag_e, tag_p, tag_a, tag_t, tag_d,
            relay_received_at, version
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `

    now := uint32(time.Now().Unix())

    _, err := s.db.ExecContext(ctx, query,
        event.ID,
        event.PubKey,
        event.CreatedAt.Time().Unix(),
        event.Kind,
        event.Content,
        event.Sig,
        tagsToArrayArray(event.Tags),
        tagE,
        tagP,
        tagA,
        tagT,
        tagD,
        now,
        now, // version = relay_received_at
    )

    return err
}

func extractTagValues(tags nostr.Tags, tagName string) []string {
    var values []string
    for _, tag := range tags {
        if len(tag) >= 2 && tag[0] == tagName {
            values = append(values, tag[1])
        }
    }
    return values
}

func getFirstTagValue(tags nostr.Tags, tagName string) string {
    for _, tag := range tags {
        if len(tag) >= 2 && tag[0] == tagName {
            return tag[1]
        }
    }
    return ""
}

func tagsToArrayArray(tags nostr.Tags) [][]string {
    result := make([][]string, len(tags))
    for i, tag := range tags {
        result[i] = tag
    }
    return result
}
```

### 7.3 Batch Insertions (High Performance)

```go
type EventBatch struct {
    events chan *nostr.Event
    done   chan struct{}
}

func (s *ClickHouseStorage) StartBatchInserter(ctx context.Context, batchSize int, flushInterval time.Duration) *EventBatch {
    batch := &EventBatch{
        events: make(chan *nostr.Event, batchSize*2),
        done:   make(chan struct{}),
    }

    go func() {
        defer close(batch.done)

        buffer := make([]*nostr.Event, 0, batchSize)
        ticker := time.NewTicker(flushInterval)
        defer ticker.Stop()

        flush := func() {
            if len(buffer) == 0 {
                return
            }

            if err := s.batchInsert(ctx, buffer); err != nil {
                log.Printf("batch insert error: %v", err)
            }

            buffer = buffer[:0]
        }

        for {
            select {
            case <-ctx.Done():
                flush()
                return
            case <-ticker.C:
                flush()
            case event, ok := <-batch.events:
                if !ok {
                    flush()
                    return
                }
                buffer = append(buffer, event)
                if len(buffer) >= batchSize {
                    flush()
                }
            }
        }
    }()

    return batch
}

func (s *ClickHouseStorage) batchInsert(ctx context.Context, events []*nostr.Event) error {
    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil {
        return err
    }
    defer tx.Rollback()

    stmt, err := tx.PrepareContext(ctx, `
        INSERT INTO nostr.events (
            id, pubkey, created_at, kind, content, sig,
            tags, tag_e, tag_p, tag_a, tag_t, tag_d,
            relay_received_at, version
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    if err != nil {
        return err
    }
    defer stmt.Close()

    now := uint32(time.Now().Unix())

    for _, event := range events {
        tagE := extractTagValues(event.Tags, "e")
        tagP := extractTagValues(event.Tags, "p")
        tagA := extractTagValues(event.Tags, "a")
        tagT := extractTagValues(event.Tags, "t")
        tagD := getFirstTagValue(event.Tags, "d")

        _, err := stmt.ExecContext(ctx,
            event.ID,
            event.PubKey,
            event.CreatedAt.Time().Unix(),
            event.Kind,
            event.Content,
            event.Sig,
            tagsToArrayArray(event.Tags),
            tagE, tagP, tagA, tagT, tagD,
            now, now,
        )
        if err != nil {
            return err
        }
    }

    return tx.Commit()
}
```

### 7.4 Query Implementation (On.Req Hook)

```go
func (s *ClickHouseStorage) QueryEvents(ctx context.Context, filters nostr.Filters) ([]nostr.Event, error) {
    var allEvents []nostr.Event

    for _, filter := range filters {
        events, err := s.queryFilter(ctx, filter)
        if err != nil {
            return nil, err
        }
        allEvents = append(allEvents, events...)
    }

    // Deduplicate and sort
    allEvents = deduplicateAndSort(allEvents)

    return allEvents, nil
}

func (s *ClickHouseStorage) queryFilter(ctx context.Context, filter nostr.Filter) ([]nostr.Event, error) {
    // Choose optimal table based on filter
    table, query := s.buildQuery(filter)

    rows, err := s.db.QueryContext(ctx, query, buildQueryArgs(filter)...)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var events []nostr.Event
    for rows.Next() {
        var event nostr.Event
        var tagsJSON string
        var createdAt int64

        err := rows.Scan(
            &event.ID,
            &event.PubKey,
            &createdAt,
            &event.Kind,
            &event.Content,
            &event.Sig,
            &tagsJSON,
        )
        if err != nil {
            return nil, err
        }

        event.CreatedAt = nostr.Timestamp(createdAt)

        // Parse tags
        if err := json.Unmarshal([]byte(tagsJSON), &event.Tags); err != nil {
            return nil, err
        }

        events = append(events, event)
    }

    return events, rows.Err()
}

func (s *ClickHouseStorage) buildQuery(filter nostr.Filter) (string, string) {
    // Determine optimal table
    var table string
    var orderByFields string

    if len(filter.IDs) > 0 {
        table = "nostr.events"
        orderByFields = "created_at"
    } else if len(filter.Authors) > 0 {
        table = "nostr.events_by_author"
        orderByFields = "created_at"
    } else if len(filter.Kinds) > 0 {
        table = "nostr.events_by_kind"
        orderByFields = "created_at"
    } else if pTags, ok := filter.Tags["p"]; ok && len(pTags) > 0 {
        table = "nostr.events_by_tag_p"
        orderByFields = "created_at"
    } else if eTags, ok := filter.Tags["e"]; ok && len(eTags) > 0 {
        table = "nostr.events_by_tag_e"
        orderByFields = "created_at"
    } else {
        table = "nostr.events"
        orderByFields = "created_at"
    }

    query := fmt.Sprintf(`
        SELECT id, pubkey, created_at, kind, content, sig,
               arrayStringConcat(arrayMap(x -> arrayStringConcat(x, '|'), tags), ';') as tags_json
        FROM %s FINAL
        WHERE deleted = 0
    `, table)

    // Build WHERE conditions
    conditions := []string{"deleted = 0"}

    if len(filter.IDs) > 0 {
        conditions = append(conditions, "id IN (?)")
    }
    if len(filter.Authors) > 0 {
        conditions = append(conditions, "pubkey IN (?)")
    }
    if len(filter.Kinds) > 0 {
        conditions = append(conditions, "kind IN (?)")
    }
    if filter.Since != nil {
        conditions = append(conditions, fmt.Sprintf("created_at >= %d", filter.Since.Time().Unix()))
    }
    if filter.Until != nil {
        conditions = append(conditions, fmt.Sprintf("created_at <= %d", filter.Until.Time().Unix()))
    }

    // Tag filters
    for tagName, values := range filter.Tags {
        if len(values) > 0 {
            switch tagName {
            case "e":
                conditions = append(conditions, "hasAny(tag_e, ?)")
            case "p":
                conditions = append(conditions, "hasAny(tag_p, ?)")
            case "t":
                conditions = append(conditions, "hasAny(tag_t, ?)")
            }
        }
    }

    if len(conditions) > 0 {
        query += " AND " + strings.Join(conditions, " AND ")
    }

    query += fmt.Sprintf(" ORDER BY %s DESC LIMIT %d", orderByFields, getLimit(filter))

    return table, query
}
```

---

## 8. Analytics Tables

### 8.1 Daily Event Statistics

```sql
CREATE TABLE nostr.daily_stats
(
    date            Date,
    kind            UInt16,
    event_count     UInt64,
    unique_authors  UInt64,
    avg_content_len Float32
)
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (date, kind);

CREATE MATERIALIZED VIEW nostr.daily_stats_mv TO nostr.daily_stats
AS SELECT
    toDate(toDateTime(created_at)) as date,
    kind,
    count() as event_count,
    uniq(pubkey) as unique_authors,
    avg(length(content)) as avg_content_len
FROM nostr.events
GROUP BY date, kind;
```

### 8.2 Author Activity Metrics

```sql
CREATE TABLE nostr.author_stats
(
    pubkey          FixedString(64),
    date            Date,
    event_count     UInt32,
    kinds_used      Array(UInt16),
    avg_tags_count  Float32
)
ENGINE = ReplacingMergeTree(date)
PARTITION BY toYYYYMM(date)
ORDER BY (pubkey, date);

CREATE MATERIALIZED VIEW nostr.author_stats_mv TO nostr.author_stats
AS SELECT
    pubkey,
    toDate(toDateTime(created_at)) as date,
    count() as event_count,
    groupArray(DISTINCT kind) as kinds_used,
    avg(length(tags)) as avg_tags_count
FROM nostr.events
GROUP BY pubkey, date;
```

### 8.3 Network Graph (Tag References)

```sql
CREATE TABLE nostr.tag_graph
(
    from_pubkey     FixedString(64),
    to_pubkey       FixedString(64),
    reference_count UInt32,
    last_reference  UInt32
)
ENGINE = SummingMergeTree(reference_count)
ORDER BY (from_pubkey, to_pubkey);

CREATE MATERIALIZED VIEW nostr.tag_graph_mv TO nostr.tag_graph
AS SELECT
    pubkey as from_pubkey,
    arrayJoin(tag_p) as to_pubkey,
    1 as reference_count,
    max(created_at) as last_reference
FROM nostr.events
WHERE length(tag_p) > 0
GROUP BY from_pubkey, to_pubkey;
```

### 8.4 Analytics Query Examples

```sql
-- Top 100 most active authors this week
SELECT
    pubkey,
    sum(event_count) as total_events
FROM nostr.author_stats
WHERE date >= today() - 7
GROUP BY pubkey
ORDER BY total_events DESC
LIMIT 100;

-- Event distribution by kind
SELECT
    kind,
    sum(event_count) as total,
    sum(unique_authors) as authors
FROM nostr.daily_stats
WHERE date >= today() - 30
GROUP BY kind
ORDER BY total DESC;

-- Most mentioned users
SELECT
    to_pubkey,
    sum(reference_count) as mentions
FROM nostr.tag_graph
GROUP BY to_pubkey
ORDER BY mentions DESC
LIMIT 100;

-- Event growth over time
SELECT
    date,
    sum(event_count) as daily_total
FROM nostr.daily_stats
WHERE date >= today() - 90
GROUP BY date
ORDER BY date;
```

---

## 9. Performance Tuning

### 9.1 ClickHouse Server Configuration

```xml
<!-- /etc/clickhouse-server/config.xml -->
<clickhouse>
    <!-- Memory settings -->
    <max_memory_usage>60000000000</max_memory_usage> <!-- 60GB -->
    <max_bytes_before_external_group_by>30000000000</max_bytes_before_external_group_by>

    <!-- Thread settings -->
    <max_threads>16</max_threads>
    <max_insert_threads>4</max_insert_threads>

    <!-- Merge settings -->
    <background_pool_size>16</background_pool_size>
    <background_merges_mutations_concurrency_ratio>2</background_merges_mutations_concurrency_ratio>

    <!-- Cache settings -->
    <mark_cache_size>10737418240</mark_cache_size> <!-- 10GB -->
    <uncompressed_cache_size>21474836480</uncompressed_cache_size> <!-- 20GB -->

    <!-- Compression -->
    <compression>
        <case>
            <method>zstd</method>
            <level>3</level>
        </case>
    </compression>
</clickhouse>
```

### 9.2 Table-Level Optimizations

```sql
-- Enable adaptive index granularity
ALTER TABLE nostr.events
    MODIFY SETTING index_granularity_bytes = 10485760;

-- Aggressive compression for old data
ALTER TABLE nostr.events
    MODIFY SETTING min_compress_block_size = 65536,
    MODIFY SETTING max_compress_block_size = 1048576;

-- TTL for automatic archival
ALTER TABLE nostr.events
    MODIFY TTL
        toDateTime(created_at) + INTERVAL 12 MONTH TO VOLUME 'cold',
        toDateTime(created_at) + INTERVAL 24 MONTH DELETE;
```

### 9.3 Query Optimization Tips

```sql
-- Use PREWHERE for filtering
SELECT * FROM nostr.events FINAL
PREWHERE kind IN (1, 6, 7)
WHERE pubkey = ? AND created_at >= ?
LIMIT 100;

-- Parallel query execution
SET max_threads = 16;
SET max_parallel_replicas = 3;

-- Sampling for analytics
SELECT kind, count() * 10 as estimated_count
FROM nostr.events SAMPLE 0.1
WHERE toDate(toDateTime(created_at)) >= today() - 30
GROUP BY kind;
```

---

## 10. Monitoring & Maintenance

### 10.1 Health Checks

```sql
-- Check table sizes
SELECT
    database,
    table,
    formatReadableSize(sum(bytes)) as size,
    sum(rows) as rows,
    max(modification_time) as latest_modification
FROM system.parts
WHERE database = 'nostr' AND active = 1
GROUP BY database, table
ORDER BY sum(bytes) DESC;

-- Check merge activity
SELECT
    database,
    table,
    elapsed,
    progress,
    num_parts,
    result_part_name
FROM system.merges
WHERE database = 'nostr';

-- Check replication lag (for clusters)
SELECT
    database,
    table,
    absolute_delay,
    queue_size
FROM system.replicas
WHERE database = 'nostr';
```

### 10.2 Maintenance Tasks

```sql
-- Force merge small parts (run weekly)
OPTIMIZE TABLE nostr.events PARTITION '202311' FINAL;

-- Clean up old temporary data
SYSTEM DROP MARK CACHE;
SYSTEM DROP UNCOMPRESSED CACHE;

-- Vacuum deleted events (after processing kind 5 deletions)
-- This happens automatically but can be forced
OPTIMIZE TABLE nostr.events FINAL DEDUPLICATE;
```

### 10.3 Monitoring Queries

```go
// Monitor insert rate
func (s *ClickHouseStorage) GetInsertRate() (float64, error) {
    var rate float64
    err := s.db.QueryRow(`
        SELECT count() / 60.0
        FROM nostr.events
        WHERE relay_received_at >= toUnixTimestamp(now() - INTERVAL 1 MINUTE)
    `).Scan(&rate)
    return rate, err
}

// Monitor storage size
func (s *ClickHouseStorage) GetStorageSize() (uint64, error) {
    var bytes uint64
    err := s.db.QueryRow(`
        SELECT sum(bytes)
        FROM system.parts
        WHERE database = 'nostr' AND active = 1
    `).Scan(&bytes)
    return bytes, err
}

// Monitor query performance
func (s *ClickHouseStorage) GetSlowQueries() ([]SlowQuery, error) {
    rows, err := s.db.Query(`
        SELECT
            query,
            query_duration_ms,
            read_rows,
            read_bytes
        FROM system.query_log
        WHERE type = 'QueryFinish'
          AND event_time >= now() - INTERVAL 1 HOUR
          AND query_duration_ms > 1000
        ORDER BY query_duration_ms DESC
        LIMIT 10
    `)
    // ... parse results
}
```

---

## 11. Disaster Recovery

### 11.1 Backup Strategy

```bash
#!/bin/bash
# Backup script for ClickHouse

DATE=$(date +%Y%m%d)
BACKUP_DIR="/backup/clickhouse/$DATE"

# Freeze tables (creates hardlinks)
clickhouse-client --query "ALTER TABLE nostr.events FREEZE WITH NAME 'backup_$DATE'"
clickhouse-client --query "ALTER TABLE nostr.events_by_author FREEZE WITH NAME 'backup_$DATE'"
clickhouse-client --query "ALTER TABLE nostr.events_by_kind FREEZE WITH NAME 'backup_$DATE'"

# Copy frozen data to backup location
rsync -av /var/lib/clickhouse/shadow/backup_$DATE/ $BACKUP_DIR/

# Upload to S3
aws s3 sync $BACKUP_DIR s3://my-bucket/nostr-relay-backups/$DATE/

# Cleanup old freezes
clickhouse-client --query "SYSTEM UNFREEZE WITH NAME 'backup_$DATE'"
```

### 11.2 Restore Process

```bash
# Stop ClickHouse
systemctl stop clickhouse-server

# Restore data
rsync -av /backup/clickhouse/20231115/ /var/lib/clickhouse/data/nostr/

# Fix permissions
chown -R clickhouse:clickhouse /var/lib/clickhouse/data/nostr/

# Start ClickHouse
systemctl start clickhouse-server

# Verify
clickhouse-client --query "SELECT count() FROM nostr.events"
```

---

## 12. Expected Performance Benchmarks

Based on ClickHouse capabilities and this design:

| Operation | Expected Performance |
|-----------|---------------------|
| Single event insert | 0.5-2 ms |
| Batch insert (1000 events) | 50-200 ms |
| Query by ID | 1-5 ms |
| Query by author (with time range) | 10-50 ms |
| Query by kind (with filters) | 20-100 ms |
| Complex multi-filter query | 50-500 ms |
| Analytics aggregation (7 days) | 100-1000 ms |
| Full-text search | 200-2000 ms |
| Storage efficiency | 70-85% compression |
| Ingestion rate | 50,000-200,000 events/sec |

**Hardware assumptions:**
- 32 CPU cores
- 128GB RAM
- NVMe SSDs for hot storage
- HDD for cold storage

---

## Summary

This design provides:

1. **Optimal query performance** via specialized materialized views and indexes
2. **Automatic deduplication** using ReplacingMergeTree engine
3. **Massive scalability** with partitioning and compression
4. **Rich analytics** with pre-aggregated statistics tables
5. **Production-ready** monitoring and maintenance procedures

The key innovation is using **multiple materialized views** to pre-optimize data layout for different query patterns, while ReplacingMergeTree handles deduplication automatically in the background.

Next steps:
1. Set up ClickHouse server(s)
2. Create all tables and materialized views
3. Implement the Go storage layer
4. Integrate with rely hooks
5. Load test and tune
6. Deploy and monitor

# ClickHouse Storage for Rely

High-performance, scalable ClickHouse storage backend for Nostr relays built with [rely](https://github.com/pippellia-btc/rely).

## Features

- **Automatic deduplication** using ReplacingMergeTree engine
- **Optimized query performance** with materialized views for different access patterns
- **Batch insertion** for high throughput (50K-200K events/sec)
- **Time-based partitioning** for efficient queries and data lifecycle management
- **Analytics tables** for reporting and insights
- **Production-ready** with monitoring and statistics

## Quick Start

### 1. Setup ClickHouse

Install and start ClickHouse:

```bash
# Docker (recommended for testing)
docker run -d \
  --name clickhouse \
  -p 9000:9000 \
  -p 8123:8123 \
  clickhouse/clickhouse-server

# Or use your package manager
# Ubuntu/Debian:
sudo apt-get install clickhouse-server clickhouse-client
sudo systemctl start clickhouse-server
```

### 2. Initialize Schema

Run the migration scripts to create the database schema:

```bash
cd storage/clickhouse/migrations

# Run each migration in order
clickhouse-client < 001_create_database.sql
clickhouse-client < 002_create_events_table.sql
clickhouse-client < 003_create_materialized_views.sql
clickhouse-client < 004_create_analytics_tables.sql
clickhouse-client < 005_create_indexes.sql
```

Or use the provided initialization script:

```bash
./init_schema.sh
```

### 3. Run Example Relay

```bash
cd examples/clickhouse
go run main.go \
  --listen "0.0.0.0:3334" \
  --clickhouse "clickhouse://localhost:9000/nostr" \
  --batch-size 1000 \
  --flush-interval 1s
```

## Integration

### Basic Usage

```go
package main

import (
    "context"
    "github.com/pippellia-btc/rely"
    "github.com/pippellia-btc/rely/storage/clickhouse"
)

func main() {
    ctx := context.Background()

    // Create storage
    storage, err := clickhouse.NewStorage(clickhouse.Config{
        DSN:           "clickhouse://localhost:9000/nostr",
        BatchSize:     1000,
        FlushInterval: 1 * time.Second,
    })
    if err != nil {
        panic(err)
    }
    defer storage.Close()

    // Create relay
    relay := rely.NewRelay()

    // Connect storage hooks
    relay.On.Event = storage.SaveEvent
    relay.On.Req = storage.QueryEvents
    relay.On.Count = storage.CountEvents

    // Start relay
    relay.StartAndServe(ctx, "0.0.0.0:3334")
}
```

### Configuration

```go
cfg := clickhouse.Config{
    // Connection string
    DSN: "clickhouse://user:pass@host:9000/database",

    // Batch insertion settings
    BatchSize:     1000,           // Events per batch
    FlushInterval: 1 * time.Second, // Max wait time

    // Connection pool
    MaxOpenConns: 10,
    MaxIdleConns: 5,
}

storage, err := clickhouse.NewStorage(cfg)
```

## Schema Overview

### Main Tables

1. **events** - Primary storage with automatic deduplication
   - Partitioned by month
   - Indexed by event ID, time, kind, author
   - ReplacingMergeTree for automatic dedup

2. **events_by_author** - Optimized for author queries
   - Fast lookups by pubkey
   - Includes time and kind indexes

3. **events_by_kind** - Optimized for kind filtering
   - Partitioned by kind
   - Fast kind-based queries

4. **events_by_tag_p** - Optimized for mention queries
   - Finds events mentioning specific pubkeys
   - Uses arrayJoin for tag extraction

5. **events_by_tag_e** - Optimized for reply chains
   - Finds events referencing specific events
   - Efficient thread reconstruction

### Analytics Tables

1. **daily_stats** - Daily event statistics by kind
2. **author_stats** - Per-author activity metrics
3. **tag_graph** - Social network graph from tag references

## Performance

Expected performance on modern hardware (32 cores, 128GB RAM, NVMe SSD):

| Operation | Latency |
|-----------|---------|
| Single event insert | 0.5-2 ms |
| Batch insert (1000 events) | 50-200 ms |
| Query by ID | 1-5 ms |
| Query by author | 10-50 ms |
| Query by kind | 20-100 ms |
| Complex multi-filter | 50-500 ms |
| COUNT query | 10-100 ms |

**Throughput:**
- Ingestion: 50,000-200,000 events/sec
- Query: 1,000-10,000 queries/sec
- Storage efficiency: 70-85% compression

## Monitoring

### Storage Statistics

```go
stats, err := storage.Stats()
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Total events: %d\n", stats.TotalEvents)
fmt.Printf("Storage size: %.2f GB\n", float64(stats.TotalBytes)/(1<<30))
fmt.Printf("Time range: %s - %s\n",
    time.Unix(int64(stats.OldestEvent), 0),
    time.Unix(int64(stats.NewestEvent), 0),
)
```

### ClickHouse Queries

```sql
-- Table sizes
SELECT
    table,
    formatReadableSize(sum(bytes)) as size,
    sum(rows) as rows
FROM system.parts
WHERE database = 'nostr' AND active = 1
GROUP BY table;

-- Recent events
SELECT count()
FROM nostr.events
WHERE relay_received_at >= toUnixTimestamp(now() - INTERVAL 1 MINUTE);

-- Top authors
SELECT
    pubkey,
    count() as event_count
FROM nostr.events FINAL
WHERE created_at >= toUnixTimestamp(now() - INTERVAL 1 DAY)
GROUP BY pubkey
ORDER BY event_count DESC
LIMIT 10;
```

## Maintenance

### Optimize Tables (Force Merges)

```sql
-- Run weekly to optimize storage
OPTIMIZE TABLE nostr.events FINAL;
OPTIMIZE TABLE nostr.events_by_author FINAL;
OPTIMIZE TABLE nostr.events_by_kind FINAL;
```

### Backup

```bash
# Freeze tables (creates hardlinks)
clickhouse-client --query "ALTER TABLE nostr.events FREEZE WITH NAME 'backup_20231115'"

# Copy to backup location
rsync -av /var/lib/clickhouse/shadow/backup_20231115/ /backup/

# Upload to S3
aws s3 sync /backup/ s3://my-bucket/relay-backup/
```

### Drop Old Partitions

```sql
-- Drop events older than 2 years
ALTER TABLE nostr.events DROP PARTITION '202111';
```

## Advanced Features

### Full-Text Search

The schema includes a tokenbf_v1 index on content for full-text search:

```go
filter := nostr.Filter{
    Search: "bitcoin",
    Kinds: []int{1},
    Limit: 100,
}
```

### Time-Based Queries

Efficient time-range queries thanks to partitioning:

```go
since := nostr.Timestamp(time.Now().Add(-24 * time.Hour).Unix())
filter := nostr.Filter{
    Kinds: []int{1},
    Since: &since,
}
```

### Tag Filtering

Fast tag-based queries using specialized materialized views:

```go
filter := nostr.Filter{
    Tags: map[string][]string{
        "p": {"<pubkey1>", "<pubkey2>"},
    },
}
```

## Analytics Queries

### Event Growth

```sql
SELECT
    date,
    sum(event_count) as daily_total
FROM nostr.daily_stats
WHERE date >= today() - 90
GROUP BY date
ORDER BY date;
```

### Most Active Authors

```sql
SELECT
    pubkey,
    sum(event_count) as total
FROM nostr.author_stats
WHERE date >= today() - 7
GROUP BY pubkey
ORDER BY total DESC
LIMIT 100;
```

### Network Graph

```sql
SELECT
    to_pubkey,
    sum(reference_count) as mentions
FROM nostr.tag_graph
GROUP BY to_pubkey
ORDER BY mentions DESC
LIMIT 100;
```

## Troubleshooting

### Connection Issues

```bash
# Test connection
clickhouse-client --query "SELECT 1"

# Check if database exists
clickhouse-client --query "SHOW DATABASES"
```

### Slow Queries

```sql
-- Find slow queries
SELECT
    query,
    query_duration_ms,
    read_rows
FROM system.query_log
WHERE type = 'QueryFinish'
  AND query_duration_ms > 1000
ORDER BY query_duration_ms DESC
LIMIT 10;
```

### High Memory Usage

Adjust ClickHouse settings in `/etc/clickhouse-server/config.xml`:

```xml
<max_memory_usage>40000000000</max_memory_usage>
<max_threads>8</max_threads>
```

## Resources

- [Full Design Document](../../CLICKHOUSE_DESIGN_PLAN.md)
- [ClickHouse Documentation](https://clickhouse.com/docs)
- [Nostr NIPs](https://github.com/nostr-protocol/nips)
- [Rely Framework](https://github.com/pippellia-btc/rely)

## License

Same as rely - see [LICENSE](../../LICENSE)

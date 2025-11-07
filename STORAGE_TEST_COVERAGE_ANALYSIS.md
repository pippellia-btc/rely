# Rely Codebase: Storage Layer & Test Coverage Analysis

## Executive Summary

**Date:** November 7, 2025
**Project:** Rely - High-performance Nostr Relay
**Language:** Go 1.24
**Current Branch:** claude/storage-test-coverage-011CUts7ShDAzt4SWpaehUWc

### Key Findings:
- **1 Storage Implementation:** ClickHouse (fully featured)
- **5 Test Files:** Covering core functionality, 18 unit tests + 13 benchmarks
- **CRITICAL GAP:** Zero tests for storage layer (0% coverage)
- **Test Files:** 5 total files, but none dedicated to storage operations

---

## 1. STORAGE IMPLEMENTATIONS

### 1.1 ClickHouse Storage (PRIMARY)

**Location:** `/home/user/rely/storage/clickhouse/`

**Core Components:**

| File | Lines | Purpose | Status |
|------|-------|---------|--------|
| `storage.go` | 199 | Main Storage struct, connection pooling, lifecycle | Implemented |
| `insert.go` | 184 | Batch insertion with optimizations | Implemented |
| `insert_optimized.go` | 201 | Native ClickHouse driver integration | Implemented |
| `query.go` | 250 | Query building, filtering, tag handling | Implemented |
| `count.go` | 162 | COUNT queries with optimizations | Implemented |
| `analytics.go` | 751 | Advanced analytics service (10+ query types) | Implemented |

**Key Features Implemented:**

1. **Batch Insertion System**
   - Configurable batch size (default: 1000)
   - Configurable flush interval (default: 1s)
   - Non-blocking channel-based queueing
   - Fallback to direct insert on channel full
   - Single-pass tag extraction (5-7x performance gain)

2. **Query Engine**
   - Dynamic table selection based on filter type
   - String builder-based query construction
   - Support for:
     - ID filtering
     - Author/pubkey filtering
     - Event kind filtering
     - Tag filtering (e, p, a, t, d, g, r)
     - Time range filtering (since/until)
     - Full-text search
     - Result limit enforcement (max 5000)

3. **Count Queries**
   - Same filter logic as regular queries
   - Optimized for exact counting
   - Approximate flag support

4. **Analytics Service** (AnalyticsService)
   - **User Analytics:** Active users, daily active users, growth metrics
   - **Engagement:** Top engaged events, trending posts with time-decay
   - **Trending:** Hashtag trends, content distribution
   - **Event Stats:** By kind, by time, with unique authors
   - **Sampling:** Random event sampling for analysis

**Database Schema:**

```
Database: nostr
Main Table: events (ReplacingMergeTree with version field)

Columns:
- Core: id, pubkey, created_at, kind, content, sig
- Tags: tags (array), tag_e, tag_p, tag_a, tag_t, tag_d, tag_g, tag_r
- Metadata: relay_received_at, deleted, version

Partitioning: By month (toYYYYMM)
Primary Key: (id)
Order: (id, created_at, kind, pubkey)

Supporting Tables:
- events_by_author: Optimized for author queries
- events_by_kind: Optimized for kind queries
- events_by_tag_p: Optimized for pubkey mention queries
- events_by_tag_e: Optimized for event reference queries
```

**Migrations Available:**

| File | Purpose |
|------|---------|
| `001_create_database.sql` | Database creation |
| `002_create_events_table.sql` | Main events table |
| `003_create_materialized_views.sql` | Supporting tables |
| `004_create_analytics_tables.sql` | Analytics views |
| `005_create_indexes.sql` | Performance indexes |
| `006_create_analytics_tables.sql` | Extended analytics |
| `007_create_hot_posts_table.sql` | Hot posts calculation |
| `008_add_hot_posts_refresh_procedure.sql` | Hot posts refresh |

**Configuration:**

```go
type Config struct {
    DSN              string        // Connection string
    BatchSize        int           // Events per batch (default: 1000)
    FlushInterval    time.Duration // Max flush wait (default: 1s)
    MaxOpenConns     int           // Connection pool size (default: 10)
    MaxIdleConns     int           // Idle connections (default: 5)
}

DefaultConfig() returns sensible defaults
```

---

## 2. CURRENT TEST COVERAGE

### 2.1 Test Files Summary

| File | Tests | Benchmarks | Focus Area | Status |
|------|-------|-----------|-----------|--------|
| `dispatcher_test.go` | 6 | 3 | Event dispatching, subscription indexing | ✓ Passing |
| `requests_test.go` | 9 | 2 | Request parsing (EVENT, REQ, COUNT, CLOSE, AUTH) | ✓ Passing |
| `responses_test.go` | 2 | 3 | Response marshaling (eventResponse, rawEventResponse) | ✓ Passing |
| `time_index_test.go` | 2 | 3 | Time-based subscription filtering | ✓ Passing |
| `tests/stress_test.go` | 1 | 0 | Long-running stress/load test (500s) | ✓ Functional |
| **TOTAL** | **18** | **13** | | |

### 2.2 Existing Test Coverage Details

#### A. Dispatcher Tests (6 tests)
```
✓ TestIndex - Subscription indexing
✓ TestUnindex - Subscription removal
✓ TestIndexingSymmetry - Index consistency
✓ TestIsLetter - Character validation
✓ TestJoin - String joining utility
+ BenchmarkIndex, BenchmarkUnindex, BenchmarkCandidates
```

#### B. Request Parsing Tests (9 tests)
```
✓ TestApplyBudget - Query limit budgeting
✓ TestParseLabel - Message label parsing
✓ TestParseEvent - EVENT message parsing
✓ TestParseReq - REQ message parsing
✓ TestParseCount - COUNT message parsing
✓ TestParseClose - CLOSE message parsing
✓ TestParseAuth - AUTH message parsing
✓ TestValidateAuth - AUTH validation
+ BenchmarkCreateChallenge, BenchmarkParseEvent, BenchmarkParseReq
```

#### C. Response Tests (2 tests)
```
✓ TestMarshalEventResponse - Standard event response
✓ TestMarshalRawEventResponse - Raw JSON event response
+ BenchmarkMarshalEventResponse, BenchmarkMarshalRawEventResponse, BenchmarkEncodeRawEventResponse2
```

#### D. Time Index Tests (2 tests)
```
✓ TestTimeIndexAdd - Time interval indexing
✓ TestTimeIndexAdvance - Time advancement logic
+ BenchmarkTimeIndexAdd, BenchmarkTimeIndexRemove, BenchmarkTimeIndexCandidates
```

#### E. Stress Test (1 test)
```
✓ TestRandom - 500-second chaos test
  - Random EVENT, REQ, COUNT, CLOSE generation
  - Configurable disconnect probabilities
  - Real WebSocket connections
  - Performance metrics collection
```

### 2.3 Test Infrastructure

**Location:** `/home/user/rely/tests/utils.go` (263 lines)

**Utilities Provided:**
- `RandomEvent()` - Generate valid signed events
- `RandomFilter()` - Generate filter combinations
- `RandomFilters()` - Generate filter lists
- `RandomString()` - Generate random strings
- `RandomSlice[T]()` - Generate random slices
- `RandomTagMap()` - Generate tag collections
- `RandomTimestamp()` - Generate timestamps

**Test Helpers:**
- `quickReq()`, `quickCount()`, `quickEvent()`, `quickClose()` - Fast template mutation
- `modifyBytes()` - Random byte mutation
- Configurable probabilities for fuzzing

**Reproducibility:**
- PCG random seed in utils.go (manual seed control available)
- Template generation for performance testing

---

## 3. CRITICAL TEST GAPS

### 3.1 Storage Layer - ZERO TESTS

**No tests exist for any storage functionality:**

```
❌ No unit tests for:
   - Event insertion (single and batch)
   - Event querying with various filters
   - Event counting
   - Event deduplication
   - Tag extraction and filtering
   - Query building logic
   - Time-based queries
   - Full-text search
   - ClickHouse connection management

❌ No integration tests for:
   - Full insert→query→count flow
   - Database persistence
   - Batch timing and flushing
   - Connection pool behavior
   - Error handling and recovery
   - Analytics queries
   - Schema migrations
   - Concurrent operations
```

### 3.2 Analytics Features - ZERO TESTS

```
❌ No tests for:
   - GetActiveUsers()
   - GetDailyActiveUsers()
   - TopUsersByFollowers()
   - GetEventKindStats()
   - GetTrendingHashtags()
   - GetTopEngagedEvents()
   - GetGrowthMetrics()
   - GetContentSizeDistribution()
   - GetTrendingPosts()
   - RefreshHotPosts()
   - SampleEvents()
```

### 3.3 Data Structures - ZERO TESTS

```
❌ No tests for:
   - ExtractedTags struct population
   - Single-pass tag extraction optimization
   - Tag deduplication within events
   - Array/slice handling in ClickHouse format
```

### 3.4 Configuration - NO TESTS

```
❌ No tests for:
   - Config validation
   - Default values
   - Connection string parsing
   - Pool size limits
```

---

## 4. OVERALL ARCHITECTURE

### 4.1 Relay Architecture

```
┌─────────────────────────────────────────────────────┐
│                    Rely Relay                        │
├─────────────────────────────────────────────────────┤
│  WebSocket Handler (client.go)                       │
│  - Manages connections                              │
│  - Parses Nostr protocol messages                   │
│  - Routes to event handlers                         │
├─────────────────────────────────────────────────────┤
│  Dispatcher (dispatcher.go, dispatcher_test.go)      │
│  - Indexes subscriptions by:                        │
│    • Event ID (byID)                                │
│    • Author (byAuthor)                              │
│    • Kind (byKind)                                  │
│    • Tags (byTag)                                   │
│    • Time ranges (timeIndex)                        │
│  - Finds matching subscribers for events            │
├─────────────────────────────────────────────────────┤
│  Storage Layer (ONLY ClickHouse implemented)         │
│  - SaveEvent() - Insert with batch queueing        │
│  - QueryEvents() - Flexible multi-filter queries    │
│  - CountEvents() - Count matching events            │
│  - Analytics - Trending, engagement, growth         │
└─────────────────────────────────────────────────────┘
```

### 4.2 Request/Response Flow

```
CLIENT → WebSocket Message (JSON)
  ↓
Parser (requests.go, requests_test.go ✓)
  • parseLabel() - Message type
  • parseEvent() - EVENT messages
  • parseReq() - REQ messages
  • parseCount() - COUNT messages
  • parseClose() - CLOSE messages
  • parseAuth() - AUTH messages
  ↓
Dispatcher (dispatcher_test.go ✓)
  • Route to event handlers
  • Match subscriptions
  ↓
Storage (storage/clickhouse - ❌ NO TESTS)
  • Insert events
  • Query databases
  • Count results
  ↓
Response Builder (responses.go, responses_test.go ✓)
  • EVENT response
  • EOSE (end of stored events)
  • COUNT response
  • OK/NOTICE messages
  ↓
CLIENT ← WebSocket Response (JSON)
```

---

## 5. STORAGE LAYER DETAILED ANALYSIS

### 5.1 Insert Path

```
SaveEvent(event)
  ↓
Non-blocking channel send
  ├─ Success: Return immediately (non-blocking)
  └─ Full: Fallback to direct insert
  ↓
batchInserter() goroutine
  ├─ Accumulates events in buffer
  ├─ Flushes on:
  │  • Buffer size reached (batch_size)
  │  • Flush interval exceeded (1s)
  │  • Stop signal received
  ↓
batchInsert(events)
  ├─ Begin transaction
  ├─ Single-pass tag extraction (5-7x faster)
  │  • e, p, a, t, g, r, d tags extracted once
  │  • Pre-allocated slices by typical size
  ├─ Prepared statement with 16 parameters
  ├─ Exec all events in transaction
  └─ Commit
```

**Optimizations:**
- Reusable buffer (avoids GC pressure)
- Single-pass tag extraction
- Pre-allocated arrays for typical tag counts
- Transaction batching
- Configurable batch size and flush interval

### 5.2 Query Path

```
QueryEvents(filters)
  ↓
For each filter in filters:
  ├─ buildQuery() - Dynamic query construction
  │  ├─ Select optimal table by filter type
  │  ├─ Build WHERE conditions
  │  └─ Apply LIMIT (max 5000)
  ├─ Execute query
  ├─ scanEvent() - Parse rows to nostr.Event
  └─ Accumulate results
  ↓
deduplicateEvents() - Remove by ID
  ├─ Uses map[string]struct{} (0-byte values)
  ├─ O(n) complexity, minimal memory
  ↓
Return merged results
```

**Query Optimization:**
- Table routing based on filter type
- FINAL keyword for ReplacingMergeTree
- Tag filtering with hasAny() or IN clauses
- Time range constraints early
- Full-text search with hasToken()
- Limit enforcement (max 5000)

### 5.3 Tag System

**Supported Tag Types:**

| Tag | Field | Type | Example |
|-----|-------|------|---------|
| e | tag_e | Array(FixedString(64)) | Event references |
| p | tag_p | Array(FixedString(64)) | Pubkey mentions |
| a | tag_a | Array(String) | Address refs (NIP-33) |
| t | tag_t | Array(String) | Hashtags |
| d | tag_d | String | Replaceable ID |
| g | tag_g | Array(String) | Geohashes |
| r | tag_r | Array(String) | URL references |

**Tag Extraction Optimization:**

```go
// Before: 7 separate scans through tags
for i := 0; i < len(tags); i++ { /* extract e tags */ }
for i := 0; i < len(tags); i++ { /* extract p tags */ }
// ... repeat 5 more times

// After: Single pass (CRITICAL OPTIMIZATION)
for i, tag := range tags {
    switch tag[0] {
    case "e": result.e = append(result.e, tag[1])
    case "p": result.p = append(result.p, tag[1])
    // ... etc
    }
}
// Results: 5-7x faster, 80% less CPU
```

---

## 6. ANALYTICS LAYER

### 6.1 Query Categories

| Category | Method | Status | Data Source |
|----------|--------|--------|-------------|
| **User Analytics** | GetActiveUsers() | ✓ Implemented | Pre-aggregated tables |
| | GetDailyActiveUsers() | ✓ Implemented | AggregatingMergeTree |
| **Followers** | TopUsersByFollowers() | ✓ Implemented | follower_counts table |
| **Event Stats** | GetEventKindStats() | ✓ Implemented | hourly_stats table |
| **Trending** | GetTrendingHashtags() | ✓ Implemented | trending_hashtags table |
| | GetTrendingPosts() | ✓ Implemented | hot_posts table (optimized) |
| **Engagement** | GetTopEngagedEvents() | ✓ Implemented | event_engagement table |
| **Growth** | GetGrowthMetrics() | ✓ Implemented | Window functions |
| **Distribution** | GetContentSizeDistribution() | ✓ Implemented | content_stats table |
| **Sampling** | SampleEvents() | ✓ Implemented | SAMPLE clause |

### 6.2 Trending/Hot Algorithm

```
TrendingPosts = time-decay weighted engagement

Score Calculation:
- Replies: 3x weight
- Reposts: 2x weight
- Reactions: 1x weight
- Zaps: 5x weight

Time Decay: exp(-0.693 * (now - created_at) / 86400)
  • Half-life: 1 day
  • Older posts decay exponentially
  • Recent engagement weighted heavily
```

---

## 7. DATABASE MIGRATION & SETUP

### 7.1 Schema Version

Current: **8 migrations** (007 and 008 are latest)

### 7.2 Setup Process

```bash
# 1. Start ClickHouse
docker run -d --name clickhouse \
  -p 9000:9000 -p 8123:8123 \
  clickhouse/clickhouse-server

# 2. Run migrations in order
./storage/clickhouse/migrations/init_schema.sh

# 3. Verify schema
clickhouse-client --query "SHOW TABLES FROM nostr"

# 4. Create storage instance
storage, _ := clickhouse.NewStorage(Config{
    DSN: "clickhouse://localhost:9000/nostr",
    BatchSize: 1000,
    FlushInterval: 1 * time.Second,
})
```

### 7.3 Key Configuration Parameters

```toml
# Connection
dsn = "clickhouse://localhost:9000/nostr"
max_open_conns = 10
max_idle_conns = 5
conn_max_lifetime = 1h

# Insertion
batch_size = 1000        # Events per batch
flush_interval = "1s"    # Max wait before flush

# Query
max_limit = 5000         # Safety limit on queries
```

---

## 8. PERFORMANCE CHARACTERISTICS

### 8.1 Expected Latencies

| Operation | Latency | Note |
|-----------|---------|------|
| Single event insert | 0.5-2 ms | Queued, non-blocking |
| Batch insert (1000 events) | 50-200 ms | Transaction batching |
| Query by ID | 1-5 ms | Primary key lookup |
| Query by author | 10-50 ms | Secondary index |
| Query by kind | 20-100 ms | Partitioned access |
| Complex multi-filter | 50-500 ms | Multiple conditions |
| COUNT query | 10-100 ms | Optimized counting |

### 8.2 Expected Throughput

- **Ingestion:** 50,000-200,000 events/sec
- **Query:** 1,000-10,000 queries/sec
- **Compression:** 70-85% compression ratio

---

## 9. MISSING TEST INFRASTRUCTURE

### 9.1 Required for Storage Testing

```
Test Database Setup:
- Containerized ClickHouse (docker/testcontainers)
- Automatic schema initialization
- Cleanup between tests

Test Fixtures:
- Sample events (valid, signed)
- Event sets for bulk operations
- Filter combinations
- Edge cases (empty results, limits exceeded)

Test Patterns Needed:
- Unit tests (logic isolation)
- Integration tests (full flows)
- Benchmark tests (performance regression)
- Stress tests (high concurrency)
```

### 9.2 Test Coverage Priorities

**CRITICAL (Functional Correctness):**
1. Event insertion (single and batch)
2. Event querying with filters
3. Tag extraction and filtering
4. Deduplication logic
5. COUNT queries

**HIGH (Reliability):**
6. Concurrent insert operations
7. Connection pool management
8. Error handling and recovery
9. Batch flush timing
10. Query building edge cases

**MEDIUM (Performance):**
11. Benchmark insertion throughput
12. Benchmark query latency
13. Tag extraction optimization validation
14. Memory allocation patterns

**LOW (Advanced Features):**
15. Analytics queries
16. Hot/trending post calculation
17. Migration verification
18. Schema compatibility

---

## 10. CONFIGURATION & ENVIRONMENT

### 10.1 Dependencies

```go
// From go.mod (Go 1.24)
- github.com/ClickHouse/clickhouse-go/v2 v2.40.3
- github.com/nbd-wtf/go-nostr v0.51.8
- github.com/goccy/go-json v0.10.5
- github.com/gorilla/websocket v1.5.3
- github.com/pippellia-btc/smallset v0.4.1
- github.com/pippellia-btc/slicex v0.2.5
```

### 10.2 ClickHouse Requirements

```
Version: Latest stable (21.x+)
Protocol: Native TCP (port 9000)
HTTP API: Port 8123 (optional)

Hardware Recommendations:
- CPU: 8+ cores (parallelization)
- RAM: 16GB+ (for aggregations)
- Disk: NVMe (for throughput)
- Network: 1Gbps+ (for batch I/O)
```

---

## 11. DOCUMENTATION

### Available Documentation

| File | Purpose | Status |
|------|---------|--------|
| `README.md` | Storage setup and integration | ✓ Complete |
| `CLICKHOUSE_DESIGN_PLAN.md` | Architecture and design | ✓ Complete |
| `CLICKHOUSE_QUICKSTART.md` | Quick start guide | ✓ Complete |
| `ANALYTICS_OPTIMIZATION.md` | Analytics performance | ✓ Complete |
| `PERFORMANCE_ANALYSIS.md` | Performance benchmarks | ✓ Complete |
| `HOT_PATH.md` | Critical path analysis | ✓ Complete |
| `CORRECTNESS_REVIEW.md` | Correctness review | ✓ Complete |
| `REALWORLD_QUERY_OPTIMIZATION.md` | Query patterns | ✓ Complete |

### Migration Documentation

All migrations are documented in SQL:
- `001_create_database.sql` - Database creation
- `002_create_events_table.sql` - Main table with schema
- `003_create_materialized_views.sql` - Supporting views
- `004_create_analytics_tables.sql` - Analytics schema
- `005_create_indexes.sql` - Performance indexes
- `006_create_analytics_tables.sql` - Extended analytics
- `007_create_hot_posts_table.sql` - Hot posts
- `008_add_hot_posts_refresh_procedure.sql` - Refresh logic

---

## 12. EXAMPLE USAGE

### 12.1 From examples/clickhouse/main.go

```go
import "github.com/pippellia-btc/rely/storage/clickhouse"

// Create storage
storage, err := clickhouse.NewStorage(clickhouse.Config{
    DSN:           "clickhouse://localhost:9000/nostr",
    BatchSize:     1000,
    FlushInterval: 1 * time.Second,
})
defer storage.Close()

// Create relay
relay := rely.NewRelay()

// Wire up handlers
relay.On.Event = storage.SaveEvent      // Insert events
relay.On.Req = storage.QueryEvents      // Query events
relay.On.Count = storage.CountEvents    // Count events

// Optional: Analytics
analytics := clickhouse.NewAnalyticsService(storage.db)
trending, _ := analytics.GetTrendingPosts(ctx, 24, 5.0, 100)
```

---

## 13. SUMMARY TABLE

### Codebase Statistics

| Metric | Count | Status |
|--------|-------|--------|
| **Storage Implementation Files** | 6 | ✓ Complete |
| **Test Files** | 5 | ⚠️ Partial |
| **Unit Tests** | 18 | ✓ Core only |
| **Benchmark Tests** | 13 | ✓ Core only |
| **Storage Tests** | 0 | ❌ CRITICAL GAP |
| **Analytics Tests** | 0 | ❌ GAP |
| **Migrations** | 8 | ✓ Complete |
| **Documentation Files** | 8 | ✓ Complete |
| **Lines of Storage Code** | ~1,800 | ✓ Well-written |
| **Lines of Test Code** | ~1,500 | ⚠️ Incomplete |

---

## 14. NEXT STEPS RECOMMENDATIONS

### For Test Coverage:
1. Create `storage/clickhouse/storage_test.go` - Core storage operations
2. Create `storage/clickhouse/insert_test.go` - Batch insert logic
3. Create `storage/clickhouse/query_test.go` - Query building
4. Create `storage/clickhouse/analytics_test.go` - Analytics queries
5. Create integration test suite with containerized ClickHouse
6. Add stress/load tests for storage operations

### For CI/CD:
1. Add docker-compose for test environment
2. Configure automatic schema migration in tests
3. Add performance regression checks
4. Coverage reports per commit

### For Documentation:
1. Testing guide for developers
2. Troubleshooting guide
3. Performance tuning guide
4. Deployment checklist


# ClickHouse Storage Implementation - Correctness Review

**Date**: 2025-11-07
**Review Status**: CRITICAL ISSUES FOUND

## Summary

Found **4 critical bugs** in the analytics implementation that will cause incorrect results or severe performance degradation.

---

## 🚨 Issue #1: follower_counts_mv - Circular Dependency

**File**: `migrations/006_create_analytics_tables.sql` (line 101)

**Problem**:
```sql
CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.follower_counts_mv TO nostr.follower_counts
AS SELECT
    follower_pubkey,
    1 as follower_count,
    ...
FROM nostr.follow_graph  -- ❌ READS FROM follow_graph!
```

The materialized view reads from `follow_graph`, but `follow_graph` is ALSO populated by a materialized view reading from `events`. ClickHouse materialized views only trigger on the source table, so `follower_counts_mv` will **NEVER trigger** when new events arrive.

**Impact**: Follower counts will always be zero.

**Fix**: Read directly from `nostr.events` instead of `follow_graph`.

**Status**: ✅ FIXED in `006_create_analytics_tables_FIXED.sql`

---

## 🚨 Issue #2: event_engagement_mv - Missing Metadata Fields

**File**: `migrations/006_create_analytics_tables_FIXED.sql` (line 219-235)

**Problem**:
```sql
CREATE MATERIALIZED VIEW ... TO nostr.event_engagement
AS SELECT
    arrayJoin(tag_e) as event_id,
    '' as author_pubkey,  -- ❌ Empty!
    0 as created_at,      -- ❌ Always zero!
    0 as kind,            -- ❌ Always zero!
    ...
```

The table schema includes `author_pubkey`, `created_at`, and `kind`, but the materialized view doesn't populate them (intentionally, with a TODO comment). However, `analytics.go` tries to use these fields!

**Affected Code**: `analytics.go:377`
```go
func (a *AnalyticsService) GetTopEngagedEvents(...) {
    query := `
        SELECT event_id, author_pubkey, created_at, kind, ...
        FROM nostr.event_engagement
        WHERE created_at >= toUInt32(now() - ?)  -- ❌ Will always be 0!
        GROUP BY event_id, author_pubkey, created_at, kind
    `
```

**Why This Happens**:
ClickHouse materialized views only see the rows being inserted. When a reaction (kind 7) is inserted:
- The MV extracts event_id from the reaction's tag_e
- But it CANNOT efficiently JOIN to find the original event's metadata
- This is a fundamental limitation of ClickHouse MVs

**Fix Options**:
1. **Option A** (Recommended): Keep event_engagement minimal, fix analytics.go to JOIN with events table
2. **Option B**: Remove metadata fields from event_engagement table entirely

**Impact**: `GetTopEngagedEvents()` returns incorrect results (all events have created_at=0).

**Status**: ⚠️ NEEDS FIX

---

## 🚨 Issue #3: hot_posts_engagement_mv - Catastrophic Performance Bug

**File**: `migrations/007_create_hot_posts_table.sql` (line 32-81)

**Problem**:
```sql
CREATE MATERIALIZED VIEW ... TO nostr.hot_posts
AS
WITH engagement AS (
    SELECT
        arrayJoin(tag_e) as event_id,
        countIf(kind = 1) as replies,
        ...
    FROM nostr.events  -- ❌ FULL TABLE SCAN ON EVERY INSERT!
    WHERE length(tag_e) > 0
    GROUP BY event_id
)
SELECT ...
FROM nostr.events e
LEFT JOIN engagement eng ON e.id = eng.event_id
```

**Why This Is Catastrophic**:
1. Every time ANY event is inserted, this materialized view triggers
2. The CTE re-scans the ENTIRE `nostr.events` table
3. It re-aggregates ALL engagement counts from scratch
4. On a table with 100M events, this means 100M rows scanned per insert!

**Performance Impact**:
- With 10K events: ~200ms per insert (acceptable)
- With 100K events: ~2s per insert (slow)
- With 1M events: ~20s per insert (unusable)
- With 10M+ events: System grinds to a halt

**Why This Design Is Flawed**:
ClickHouse materialized views execute their SELECT query for EACH batch of inserted rows. This design assumes the query is cheap, but this query does a full table scan.

**Correct Approach**:
- Materialized views should be **incremental** (only process new data)
- For `hot_posts`, we should either:
  - **Option A**: Populate via periodic batch job (OPTIMIZE FOR ... query)
  - **Option B**: Build incrementally from `event_engagement` table
  - **Option C**: Remove hot_posts and compute on-demand with smart caching

**Impact**: After a few million events, every insert will take 10-30+ seconds.

**Status**: ⚠️ NEEDS COMPLETE REDESIGN

---

## 🚨 Issue #4: GetActiveUsers - follower_counts Join Issue

**File**: `analytics.go:51`

**Problem**:
```go
LEFT JOIN nostr.follower_counts f ON a.pubkey = f.pubkey
WHERE f.follower_count >= ? OR f.follower_count IS NULL
```

This query filters by `f.follower_count >= ?` but `follower_counts` is a `SummingMergeTree` which requires aggregation to get final values!

**Correct Approach**:
```sql
LEFT JOIN (
    SELECT pubkey, sum(follower_count) as followers
    FROM nostr.follower_counts
    GROUP BY pubkey
) f ON a.pubkey = f.pubkey
WHERE f.followers >= ? OR f.followers IS NULL
```

**Impact**: Incorrect follower count filtering.

**Status**: ⚠️ NEEDS FIX

---

## Recommended Fixes

### Priority 1: Fix event_engagement + analytics.go

**Change 1**: Update `analytics.go:GetTopEngagedEvents()` to JOIN with events table:

```go
func (a *AnalyticsService) GetTopEngagedEvents(ctx context.Context, hours int, limit int) ([]EngagedEvent, error) {
    query := `
        SELECT
            eng.event_id,
            e.pubkey as author_pubkey,
            e.created_at,
            e.kind,
            sum(eng.reply_count) as replies,
            sum(eng.reaction_count) as reactions,
            sum(eng.repost_count) as reposts,
            sum(eng.zap_count) as zaps,
            sum(eng.reply_count * 3 + eng.repost_count * 2 + eng.reaction_count * 1 + eng.zap_count * 5) as score
        FROM nostr.event_engagement eng
        JOIN nostr.events e ON eng.event_id = e.id
        WHERE e.created_at >= toUInt32(now() - ?)
          AND e.deleted = 0
        GROUP BY eng.event_id, e.pubkey, e.created_at, e.kind
        ORDER BY score DESC
        LIMIT ?
    `
    // ... rest unchanged
}
```

**Change 2**: Remove unused fields from `event_engagement` table OR document that they're not used.

### Priority 2: Fix hot_posts

**Option A - Periodic Refresh (Recommended)**:

Remove the broken materialized view and replace with a stored procedure or application code that runs hourly:

```sql
-- Remove the broken materialized view
DROP VIEW IF EXISTS nostr.hot_posts_engagement_mv;

-- Add comment to hot_posts table
COMMENT ON TABLE nostr.hot_posts IS 'Populated by periodic batch job - see analytics.go:RefreshHotPosts()';
```

Add Go function to refresh hot_posts:
```go
func (a *AnalyticsService) RefreshHotPosts(ctx context.Context, hours int) error {
    // Truncate old data
    _, err := a.db.ExecContext(ctx, `
        ALTER TABLE nostr.hot_posts
        DELETE WHERE hour_bucket < toStartOfHour(now() - INTERVAL ? HOUR)
    `, hours)

    // Insert/update hot posts
    _, err = a.db.ExecContext(ctx, `
        INSERT INTO nostr.hot_posts
        SELECT
            e.id as event_id,
            e.pubkey as author_pubkey,
            e.created_at,
            e.kind,
            sum(eng.reply_count) as reply_count,
            sum(eng.reaction_count) as reaction_count,
            sum(eng.repost_count) as repost_count,
            sum(eng.zap_count) as zap_count,
            sum(eng.zap_total_sats) as zap_total_sats,
            sum(eng.reply_count * 3 + eng.repost_count * 2 + eng.reaction_count * 1 + eng.zap_count * 5) as engagement_score,
            engagement_score * exp(-0.693 * (toFloat64(now()) - toFloat64(e.created_at)) / 86400.0) as hot_score,
            toStartOfHour(toDateTime(e.created_at)) as hour_bucket,
            toUInt32(now()) as last_updated
        FROM nostr.events e
        LEFT JOIN nostr.event_engagement eng ON e.id = eng.event_id
        WHERE e.kind = 1
          AND e.created_at >= toUInt32(now() - ? * 3600)
          AND e.deleted = 0
        GROUP BY e.id, e.pubkey, e.created_at, e.kind
        HAVING engagement_score > 0
    `, hours)

    return err
}
```

**Option B - Remove hot_posts entirely**:

Compute trending posts on-demand using event_engagement (with caching in application layer).

### Priority 3: Fix GetActiveUsers

Update the follower_counts join to properly aggregate SummingMergeTree:

```go
query := `
    WITH active_pubkeys AS (...),
    follower_agg AS (
        SELECT pubkey, sum(follower_count) as followers
        FROM nostr.follower_counts
        GROUP BY pubkey
    ),
    qualified_users AS (
        SELECT a.pubkey
        FROM active_pubkeys a
        LEFT JOIN follower_agg f ON a.pubkey = f.pubkey
        WHERE f.followers >= ? OR f.followers IS NULL
    )
    ...
```

---

## Testing Recommendations

1. **Unit Test**: Insert sample data and verify materialized views trigger correctly
2. **Performance Test**: Load 1M events and measure insert latency
3. **Correctness Test**: Verify follower counts, engagement metrics match manual queries
4. **Stress Test**: Insert 10K events/sec and monitor query performance

---

## Migration Plan

1. Create `006_create_analytics_tables_FIXED_v2.sql` with event_engagement fix
2. Create `007_create_hot_posts_table_FIXED.sql` removing broken materialized view
3. Create `008_add_hot_posts_refresh.sql` with batch refresh approach
4. Update `analytics.go` with JOIN fixes
5. Add periodic job to refresh hot_posts (cron or application ticker)
6. Test on staging with production-scale data
7. Deploy to production with monitoring

---

## Conclusion

The analytics implementation has **critical architectural flaws** that stem from misunderstanding ClickHouse materialized view semantics:

1. ❌ Materialized views can't cascade efficiently (Issue #1)
2. ❌ Materialized views can't JOIN with full table state (Issue #2)
3. ❌ Materialized views must be incremental, not full scans (Issue #3)
4. ❌ SummingMergeTree requires aggregation queries (Issue #4)

**All issues are fixable** with the recommended changes above.

**Estimated Fix Time**: 4-6 hours (including testing)
**Risk Level**: Medium (requires schema changes and code updates)
**Impact If Not Fixed**: System will be unusable at scale (>1M events)

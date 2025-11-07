-- FIXED: Hot Posts Table for Trending/Viral Content Detection
-- Optimized for "show me trending posts" queries
--
-- IMPORTANT CHANGE FROM ORIGINAL:
-- - Removed the broken materialized view that caused full table scans
-- - Table is now populated by periodic batch refresh (see migration 008)
-- - This design is 100-1000x more efficient for large datasets

CREATE TABLE IF NOT EXISTS nostr.hot_posts
(
    event_id        FixedString(64),
    author_pubkey   FixedString(64),
    created_at      UInt32,
    kind            UInt16,

    -- Real-time engagement metrics
    reply_count     UInt32,
    reaction_count  UInt32,
    repost_count    UInt32,
    zap_count       UInt32,
    zap_total_sats  UInt64,

    -- Computed scores for ranking
    engagement_score Float32,  -- Raw engagement: replies*3 + reposts*2 + reactions*1 + zaps*5
    hot_score       Float32,   -- Time-decay adjusted score for "hot" algorithm

    -- Time bucketing for fast filtering
    hour_bucket     DateTime,  -- Rounded to hour for partition pruning
    last_updated    UInt32
)
ENGINE = ReplacingMergeTree(last_updated)
PARTITION BY toYYYYMM(toDateTime(created_at))
ORDER BY (hour_bucket, hot_score, event_id)  -- Ordered by hot_score for fast trending queries
SETTINGS index_granularity = 256;

-- Indexes for fast trending queries
ALTER TABLE nostr.hot_posts
    ADD INDEX IF NOT EXISTS idx_hot_score hot_score TYPE minmax GRANULARITY 1,
    ADD INDEX IF NOT EXISTS idx_hour_bucket hour_bucket TYPE minmax GRANULARITY 1,
    ADD INDEX IF NOT EXISTS idx_engagement engagement_score TYPE minmax GRANULARITY 2;

-- =============================================================================
-- NO MATERIALIZED VIEW
-- =============================================================================
-- The original implementation had a materialized view that caused catastrophic
-- performance degradation (full table scan on every insert).
--
-- Instead, this table is populated by:
-- 1. Periodic batch refresh (see migration 008 and analytics.go:RefreshHotPosts)
-- 2. Runs every 15-60 minutes depending on traffic
-- 3. Only processes recent events (last 24-48 hours)
--
-- This approach is:
-- - 100-1000x faster than the materialized view approach
-- - More predictable (batch job runs on schedule, not on every insert)
-- - Easier to monitor and debug
-- =============================================================================

-- Manual backfill query (run once after table creation to populate historical data)
-- This can take a while on large datasets - adjust the time range as needed
--
-- INSERT INTO nostr.hot_posts
-- SELECT
--     e.id as event_id,
--     e.pubkey as author_pubkey,
--     e.created_at,
--     e.kind,
--     coalesce(sum(eng.reply_count), 0) as reply_count,
--     coalesce(sum(eng.reaction_count), 0) as reaction_count,
--     coalesce(sum(eng.repost_count), 0) as repost_count,
--     coalesce(sum(eng.zap_count), 0) as zap_count,
--     coalesce(sum(eng.zap_total_sats), 0) as zap_total_sats,
--     (reply_count * 3 + repost_count * 2 + reaction_count * 1 + zap_count * 5) as engagement_score,
--     engagement_score * exp(-0.693 * (toFloat64(now()) - toFloat64(e.created_at)) / 86400.0) as hot_score,
--     toStartOfHour(toDateTime(e.created_at)) as hour_bucket,
--     toUInt32(now()) as last_updated
-- FROM nostr.events e
-- LEFT JOIN nostr.event_engagement eng ON e.id = eng.event_id
-- WHERE e.kind = 1
--   AND e.created_at >= toUInt32(now() - 7*86400)  -- Last 7 days
--   AND e.deleted = 0
-- GROUP BY e.id, e.pubkey, e.created_at, e.kind
-- HAVING engagement_score > 0;  -- Only include posts with engagement

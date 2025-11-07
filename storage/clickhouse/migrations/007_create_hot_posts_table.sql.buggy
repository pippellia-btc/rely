-- Hot Posts Table for Trending/Viral Content Detection
-- Optimized for "show me trending posts" queries

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

-- Materialized view to populate engagement data in real-time
CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.hot_posts_engagement_mv TO nostr.hot_posts
AS
WITH engagement AS (
    SELECT
        arrayJoin(tag_e) as event_id,
        countIf(kind = 1) as replies,
        countIf(kind = 7) as reactions,
        countIf(kind = 6) as reposts,
        countIf(kind = 9735) as zaps,
        sumIf(
            toUInt64OrZero(JSONExtractString(content, 'amount')) / 1000,
            kind = 9735
        ) as zap_sats
    FROM nostr.events
    WHERE length(tag_e) > 0
      AND kind IN (1, 6, 7, 9735)
      AND deleted = 0
    GROUP BY event_id
)
SELECT
    e.id as event_id,
    e.pubkey as author_pubkey,
    e.created_at,
    e.kind,
    coalesce(eng.replies, 0) as reply_count,
    coalesce(eng.reactions, 0) as reaction_count,
    coalesce(eng.reposts, 0) as repost_count,
    coalesce(eng.zaps, 0) as zap_count,
    coalesce(eng.zap_sats, 0) as zap_total_sats,

    -- Engagement score (weighted)
    (coalesce(eng.replies, 0) * 3 +
     coalesce(eng.reposts, 0) * 2 +
     coalesce(eng.reactions, 0) * 1 +
     coalesce(eng.zaps, 0) * 5) as engagement_score,

    -- Hot score with exponential time decay
    -- Formula: engagement_score * e^(-0.693 * hours_old / 24)
    -- This gives posts a "half-life" of 24 hours
    (coalesce(eng.replies, 0) * 3 +
     coalesce(eng.reposts, 0) * 2 +
     coalesce(eng.reactions, 0) * 1 +
     coalesce(eng.zaps, 0) * 5) *
    exp(-0.693 * (toFloat64(now()) - toFloat64(e.created_at)) / 86400.0) as hot_score,

    toStartOfHour(toDateTime(e.created_at)) as hour_bucket,
    toUInt32(now()) as last_updated
FROM nostr.events e
LEFT JOIN engagement eng ON e.id = eng.event_id
WHERE e.kind = 1 AND e.deleted = 0;

-- Indexes for fast trending queries
ALTER TABLE nostr.hot_posts
    ADD INDEX IF NOT EXISTS idx_hot_score hot_score TYPE minmax GRANULARITY 1,
    ADD INDEX IF NOT EXISTS idx_hour_bucket hour_bucket TYPE minmax GRANULARITY 1,
    ADD INDEX IF NOT EXISTS idx_engagement engagement_score TYPE minmax GRANULARITY 2;

-- Backfill script (run after table creation to populate existing data)
-- Note: This can take a while on large datasets, run with SAMPLE for faster backfill
--
-- INSERT INTO nostr.hot_posts
-- SELECT
--     e.id as event_id,
--     e.pubkey as author_pubkey,
--     e.created_at,
--     e.kind,
--     countIf(r.kind = 1 AND has(r.tag_e, e.id)) as reply_count,
--     countIf(r.kind = 7 AND has(r.tag_e, e.id)) as reaction_count,
--     countIf(r.kind = 6 AND has(r.tag_e, e.id)) as repost_count,
--     countIf(r.kind = 9735 AND has(r.tag_e, e.id)) as zap_count,
--     0 as zap_total_sats,
--     (reply_count * 3 + repost_count * 2 + reaction_count + zap_count * 5) as engagement_score,
--     engagement_score * exp(-0.693 * (toFloat64(now()) - toFloat64(e.created_at)) / 86400.0) as hot_score,
--     toStartOfHour(toDateTime(e.created_at)) as hour_bucket,
--     toUInt32(now()) as last_updated
-- FROM nostr.events e
-- LEFT JOIN nostr.events r ON has(r.tag_e, e.id) AND r.kind IN (1, 6, 7, 9735)
-- WHERE e.kind = 1
--   AND e.created_at >= toUInt32(now() - 30*86400)  -- Last 30 days
--   AND e.deleted = 0
-- GROUP BY e.id, e.pubkey, e.created_at, e.kind;

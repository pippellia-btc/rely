-- Advanced Analytics Schema for Nostr Archival Relay
-- Optimized for OLAP queries, reports, and time-series analysis

-- =============================================================================
-- USER PROFILE ANALYTICS
-- =============================================================================

-- Extract user metadata from kind 0 events
CREATE TABLE IF NOT EXISTS nostr.user_profiles
(
    pubkey          FixedString(64),
    name            String,
    display_name    String,
    about           String,
    picture         String,
    banner          String,
    website         String,
    nip05           String,          -- NIP-05 identifier
    lud16           String,          -- Lightning address
    lud06           String,          -- LNURL

    -- Derived fields
    has_nip05       UInt8,           -- Boolean: has NIP-05 verification
    has_lightning   UInt8,           -- Boolean: has lightning address
    profile_size    UInt32,          -- Size of profile JSON

    -- Metadata
    created_at      UInt32,
    updated_at      UInt32,
    version         UInt32           -- For ReplacingMergeTree
)
ENGINE = ReplacingMergeTree(version)
ORDER BY pubkey
SETTINGS index_granularity = 8192;

-- Materialized view to extract profile data from kind 0 events
CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.user_profiles_mv TO nostr.user_profiles
AS SELECT
    pubkey,
    JSONExtractString(content, 'name') as name,
    JSONExtractString(content, 'display_name') as display_name,
    JSONExtractString(content, 'about') as about,
    JSONExtractString(content, 'picture') as picture,
    JSONExtractString(content, 'banner') as banner,
    JSONExtractString(content, 'website') as website,
    JSONExtractString(content, 'nip05') as nip05,
    JSONExtractString(content, 'lud16') as lud16,
    JSONExtractString(content, 'lud06') as lud06,

    -- Derived fields
    if(length(JSONExtractString(content, 'nip05')) > 0, 1, 0) as has_nip05,
    if(length(JSONExtractString(content, 'lud16')) > 0 OR length(JSONExtractString(content, 'lud06')) > 0, 1, 0) as has_lightning,
    length(content) as profile_size,

    created_at,
    created_at as updated_at,
    relay_received_at as version
FROM nostr.events
WHERE kind = 0 AND deleted = 0;

-- =============================================================================
-- FOLLOWER/FOLLOWING ANALYTICS
-- =============================================================================

-- Extract follow relationships from kind 3 events
CREATE TABLE IF NOT EXISTS nostr.follow_graph
(
    follower_pubkey  FixedString(64),  -- Who is following
    following_pubkey FixedString(64),  -- Who they follow
    created_at       UInt32,
    version          UInt32             -- For deduplication
)
ENGINE = ReplacingMergeTree(version)
ORDER BY (follower_pubkey, following_pubkey)
SETTINGS index_granularity = 8192;

-- Materialized view to extract follows from kind 3 contact lists
CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.follow_graph_mv TO nostr.follow_graph
AS SELECT
    pubkey as follower_pubkey,
    arrayJoin(tag_p) as following_pubkey,
    created_at,
    relay_received_at as version
FROM nostr.events
WHERE kind = 3 AND length(tag_p) > 0 AND deleted = 0;

-- Aggregated follower counts (pre-computed for fast lookups)
CREATE TABLE IF NOT EXISTS nostr.follower_counts
(
    pubkey           FixedString(64),
    follower_count   UInt32,
    following_count  UInt32,
    last_updated     UInt32
)
ENGINE = SummingMergeTree()
ORDER BY pubkey
SETTINGS index_granularity = 8192;

-- Materialized view to maintain follower counts
CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.follower_counts_mv TO nostr.follower_counts
AS SELECT
    following_pubkey as pubkey,
    count() as follower_count,
    0 as following_count,
    max(created_at) as last_updated
FROM nostr.follow_graph
GROUP BY following_pubkey

UNION ALL

SELECT
    follower_pubkey as pubkey,
    0 as follower_count,
    count() as following_count,
    max(created_at) as last_updated
FROM nostr.follow_graph
GROUP BY follower_pubkey;

-- =============================================================================
-- TIME-SERIES ANALYTICS (Hourly/Daily/Weekly/Monthly)
-- =============================================================================

-- Hourly event statistics
CREATE TABLE IF NOT EXISTS nostr.hourly_stats
(
    hour            DateTime,        -- Rounded to hour
    kind            UInt16,
    event_count     UInt64,
    unique_authors  AggregateFunction(uniq, FixedString(64)),
    total_size      UInt64,          -- Total bytes of content
    avg_tags        Float32
)
ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, kind)
SETTINGS index_granularity = 256;

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.hourly_stats_mv TO nostr.hourly_stats
AS SELECT
    toStartOfHour(toDateTime(created_at)) as hour,
    kind,
    count() as event_count,
    uniqState(pubkey) as unique_authors,
    sum(length(content)) as total_size,
    avg(length(tags)) as avg_tags
FROM nostr.events
WHERE deleted = 0
GROUP BY hour, kind;

-- Daily user activity (active users per day)
CREATE TABLE IF NOT EXISTS nostr.daily_active_users
(
    date            Date,
    active_users    AggregateFunction(uniq, FixedString(64)),
    total_events    UInt64,

    -- Segmentation
    users_with_nip05    AggregateFunction(uniq, FixedString(64)),
    users_with_followers UInt32,

    -- Event type breakdown
    text_notes      UInt64,  -- kind 1
    reactions       UInt64,  -- kind 7
    reposts         UInt64,  -- kind 6
    zaps            UInt64   -- kind 9735
)
ENGINE = AggregatingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY date
SETTINGS index_granularity = 32;

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.daily_active_users_mv TO nostr.daily_active_users
AS SELECT
    toDate(toDateTime(created_at)) as date,
    uniqState(pubkey) as active_users,
    count() as total_events,

    -- This will be enhanced with JOIN to user_profiles
    uniqState(pubkey) as users_with_nip05,
    0 as users_with_followers,

    countIf(kind = 1) as text_notes,
    countIf(kind = 7) as reactions,
    countIf(kind = 6) as reposts,
    countIf(kind = 9735) as zaps
FROM nostr.events
WHERE deleted = 0
GROUP BY date;

-- =============================================================================
-- ENGAGEMENT METRICS
-- =============================================================================

-- Per-event engagement tracking
CREATE TABLE IF NOT EXISTS nostr.event_engagement
(
    event_id        FixedString(64),
    author_pubkey   FixedString(64),
    created_at      UInt32,
    kind            UInt16,

    -- Engagement metrics
    reply_count     UInt32,
    reaction_count  UInt32,
    repost_count    UInt32,
    zap_count       UInt32,
    zap_total_sats  UInt64,

    last_updated    UInt32
)
ENGINE = SummingMergeTree()
ORDER BY (author_pubkey, created_at, event_id)
SETTINGS index_granularity = 8192;

-- Track replies to events
CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.event_replies_mv TO nostr.event_engagement
AS SELECT
    arrayJoin(tag_e) as event_id,
    '' as author_pubkey,  -- Will be filled by periodic job
    0 as created_at,
    kind,
    countIf(kind = 1) as reply_count,
    countIf(kind = 7) as reaction_count,
    countIf(kind = 6) as repost_count,
    countIf(kind = 9735) as zap_count,
    0 as zap_total_sats,
    max(toUInt32(now())) as last_updated
FROM nostr.events
WHERE length(tag_e) > 0 AND deleted = 0 AND kind IN (1, 6, 7, 9735)
GROUP BY event_id, kind;

-- =============================================================================
-- CONTENT ANALYTICS
-- =============================================================================

-- Trending hashtags
CREATE TABLE IF NOT EXISTS nostr.trending_hashtags
(
    date            Date,
    hour            UInt8,           -- 0-23
    hashtag         String,
    usage_count     UInt32,
    unique_authors  UInt32
)
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (date, hour, hashtag)
SETTINGS index_granularity = 256;

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.trending_hashtags_mv TO nostr.trending_hashtags
AS SELECT
    toDate(toDateTime(created_at)) as date,
    toHour(toDateTime(created_at)) as hour,
    arrayJoin(tag_t) as hashtag,
    count() as usage_count,
    uniq(pubkey) as unique_authors
FROM nostr.events
WHERE length(tag_t) > 0 AND deleted = 0 AND kind = 1
GROUP BY date, hour, hashtag;

-- Content size distribution
CREATE TABLE IF NOT EXISTS nostr.content_stats
(
    date            Date,
    kind            UInt16,

    -- Size buckets
    tiny_count      UInt64,  -- 0-140 chars
    short_count     UInt64,  -- 141-500
    medium_count    UInt64,  -- 501-2000
    long_count      UInt64,  -- 2001-10000
    huge_count      UInt64,  -- 10000+

    avg_size        Float32,
    max_size        UInt32
)
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(date)
ORDER BY (date, kind);

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.content_stats_mv TO nostr.content_stats
AS SELECT
    toDate(toDateTime(created_at)) as date,
    kind,
    countIf(length(content) <= 140) as tiny_count,
    countIf(length(content) > 140 AND length(content) <= 500) as short_count,
    countIf(length(content) > 500 AND length(content) <= 2000) as medium_count,
    countIf(length(content) > 2000 AND length(content) <= 10000) as long_count,
    countIf(length(content) > 10000) as huge_count,
    avg(length(content)) as avg_size,
    max(length(content)) as max_size
FROM nostr.events
WHERE deleted = 0
GROUP BY date, kind;

-- =============================================================================
-- RELAY HEALTH METRICS
-- =============================================================================

-- Event processing statistics
CREATE TABLE IF NOT EXISTS nostr.relay_metrics
(
    timestamp       DateTime,
    metric_type     String,          -- 'events_received', 'events_stored', 'queries_served'
    value           Float64,
    metadata        String           -- JSON with additional info
)
ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (timestamp, metric_type)
TTL timestamp + INTERVAL 90 DAY  -- Keep metrics for 90 days
SETTINGS index_granularity = 256;

-- =============================================================================
-- SPECIALIZED INDEXES FOR ANALYTICS
-- =============================================================================

-- Add bloom filter indexes for common analytical filters
ALTER TABLE nostr.events
    ADD INDEX IF NOT EXISTS idx_content_length length(content) TYPE minmax GRANULARITY 8,
    ADD INDEX IF NOT EXISTS idx_has_tags length(tags) TYPE minmax GRANULARITY 8;

-- Add indexes to user_profiles for filtering
ALTER TABLE nostr.user_profiles
    ADD INDEX IF NOT EXISTS idx_has_nip05 has_nip05 TYPE set(2) GRANULARITY 1,
    ADD INDEX IF NOT EXISTS idx_has_lightning has_lightning TYPE set(2) GRANULARITY 1,
    ADD INDEX IF NOT EXISTS idx_nip05 nip05 TYPE bloom_filter(0.01) GRANULARITY 4;

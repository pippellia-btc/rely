-- Daily statistics table
CREATE TABLE IF NOT EXISTS nostr.daily_stats
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

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.daily_stats_mv TO nostr.daily_stats
AS SELECT
    toDate(toDateTime(created_at)) as date,
    kind,
    count() as event_count,
    uniq(pubkey) as unique_authors,
    avg(length(content)) as avg_content_len
FROM nostr.events
GROUP BY date, kind;

-- Author activity metrics
CREATE TABLE IF NOT EXISTS nostr.author_stats
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

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.author_stats_mv TO nostr.author_stats
AS SELECT
    pubkey,
    toDate(toDateTime(created_at)) as date,
    count() as event_count,
    groupArray(DISTINCT kind) as kinds_used,
    avg(length(tags)) as avg_tags_count
FROM nostr.events
GROUP BY pubkey, date;

-- Network graph (tag references)
CREATE TABLE IF NOT EXISTS nostr.tag_graph
(
    from_pubkey     FixedString(64),
    to_pubkey       FixedString(64),
    reference_count UInt32,
    last_reference  UInt32
)
ENGINE = SummingMergeTree(reference_count)
ORDER BY (from_pubkey, to_pubkey);

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.tag_graph_mv TO nostr.tag_graph
AS SELECT
    pubkey as from_pubkey,
    arrayJoin(tag_p) as to_pubkey,
    1 as reference_count,
    max(created_at) as last_reference
FROM nostr.events
WHERE length(tag_p) > 0
GROUP BY from_pubkey, to_pubkey;

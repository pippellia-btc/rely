-- Materialized view for author-based queries
CREATE TABLE IF NOT EXISTS nostr.events_by_author
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
    tag_d           String,
    relay_received_at UInt32,
    deleted         UInt8,
    version         UInt32
)
ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(toDateTime(created_at))
PRIMARY KEY (pubkey)
ORDER BY (pubkey, created_at, kind, id)
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.events_by_author_mv TO nostr.events_by_author
AS SELECT
    pubkey, created_at, kind, id, content, tags, sig,
    tag_e, tag_p, tag_t, tag_d, relay_received_at, deleted, version
FROM nostr.events;

-- Materialized view for kind-based queries
CREATE TABLE IF NOT EXISTS nostr.events_by_kind
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

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.events_by_kind_mv TO nostr.events_by_kind
AS SELECT
    kind, created_at, id, pubkey, content, tags, sig,
    tag_e, tag_p, tag_t, tag_d, relay_received_at, deleted, version
FROM nostr.events;

-- Materialized view for tag-p queries (mentions)
CREATE TABLE IF NOT EXISTS nostr.events_by_tag_p
(
    tag_p_value     FixedString(64),
    created_at      UInt32,
    id              FixedString(64),
    pubkey          FixedString(64),
    kind            UInt16,
    content         String,
    tags            Array(Array(String)),
    sig             FixedString(128),
    relay_received_at UInt32,
    deleted         UInt8,
    version         UInt32
)
ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(toDateTime(created_at))
PRIMARY KEY (tag_p_value)
ORDER BY (tag_p_value, created_at, kind, id)
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.events_by_tag_p_mv TO nostr.events_by_tag_p
AS SELECT
    arrayJoin(tag_p) AS tag_p_value,
    created_at, id, pubkey, kind, content, tags, sig,
    relay_received_at, deleted, version
FROM nostr.events
WHERE length(tag_p) > 0;

-- Materialized view for tag-e queries (event references)
CREATE TABLE IF NOT EXISTS nostr.events_by_tag_e
(
    tag_e_value     FixedString(64),
    created_at      UInt32,
    id              FixedString(64),
    pubkey          FixedString(64),
    kind            UInt16,
    content         String,
    tags            Array(Array(String)),
    sig             FixedString(128),
    relay_received_at UInt32,
    deleted         UInt8,
    version         UInt32
)
ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(toDateTime(created_at))
PRIMARY KEY (tag_e_value)
ORDER BY (tag_e_value, created_at, kind, id)
SETTINGS index_granularity = 8192;

CREATE MATERIALIZED VIEW IF NOT EXISTS nostr.events_by_tag_e_mv TO nostr.events_by_tag_e
AS SELECT
    arrayJoin(tag_e) AS tag_e_value,
    created_at, id, pubkey, kind, content, tags, sig,
    relay_received_at, deleted, version
FROM nostr.events
WHERE length(tag_e) > 0;

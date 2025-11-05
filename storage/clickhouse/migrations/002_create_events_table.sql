-- Main events table with automatic deduplication
CREATE TABLE IF NOT EXISTS nostr.events
(
    -- Core Event Fields
    id              FixedString(64),        -- Event ID (SHA256 hex)
    pubkey          FixedString(64),        -- Author public key
    created_at      UInt32,                 -- Unix timestamp
    kind            UInt16,                 -- Event kind (0-65535)
    content         String,                 -- Event content
    sig             FixedString(128),       -- Signature

    -- Tag Storage
    tags            Array(Array(String)),   -- Full tags array

    -- Extracted Tag Indexes (for fast filtering)
    tag_e           Array(FixedString(64)), -- Event references
    tag_p           Array(FixedString(64)), -- Pubkey mentions
    tag_a           Array(String),          -- Address references (NIP-33)
    tag_t           Array(String),          -- Hashtags
    tag_d           String,                 -- Replaceable event identifier
    tag_g           Array(String),          -- Geohash locations
    tag_r           Array(String),          -- URL references

    -- Metadata
    relay_received_at UInt32,               -- When relay received it
    deleted           UInt8 DEFAULT 0,      -- Soft delete flag

    -- Version for deduplication
    version         UInt32                  -- For ReplacingMergeTree
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

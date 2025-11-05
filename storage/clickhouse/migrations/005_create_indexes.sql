-- Add secondary indexes for better query performance
ALTER TABLE nostr.events
    ADD INDEX IF NOT EXISTS idx_kind kind TYPE minmax GRANULARITY 4,
    ADD INDEX IF NOT EXISTS idx_pubkey pubkey TYPE bloom_filter(0.01) GRANULARITY 4,
    ADD INDEX IF NOT EXISTS idx_created_at created_at TYPE minmax GRANULARITY 4,
    ADD INDEX IF NOT EXISTS idx_tag_p tag_p TYPE bloom_filter(0.01) GRANULARITY 4,
    ADD INDEX IF NOT EXISTS idx_tag_e tag_e TYPE bloom_filter(0.01) GRANULARITY 4,
    ADD INDEX IF NOT EXISTS idx_content content TYPE tokenbf_v1(30000, 3, 0) GRANULARITY 4;

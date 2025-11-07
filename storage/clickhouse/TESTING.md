# ClickHouse Storage Testing Guide

This document describes the comprehensive test suite for the ClickHouse storage backend.

## Overview

The test suite includes:
- **Unit Tests**: Test individual functions and components
- **Integration Tests**: Test complete workflows (insert → query → count)
- **Functional Tests**: Test real-world scenarios with actual Nostr relay ingestion
- **Analytics Tests**: Test all analytics and reporting functions

## Test Files

- `storage_test.go` - Basic storage operations tests
- `storage_integration_test.go` - Integration and workflow tests
- `storage_functional_test.go` - Functional tests with relay.nostr.net
- `analytics_test.go` - Analytics service tests

## Prerequisites

### 1. ClickHouse Server

You need a running ClickHouse server. Use the provided docker-compose file:

```bash
# Start ClickHouse test server
docker-compose -f docker-compose.test.yml up -d

# Wait for ClickHouse to be ready
sleep 10

# Verify ClickHouse is running
docker-compose -f docker-compose.test.yml ps
```

### 2. Environment Variables

Set the ClickHouse DSN (optional, defaults to localhost):

```bash
export CLICKHOUSE_DSN="clickhouse://localhost:9000/nostr"
```

## Running Tests

### Run All Tests

```bash
# Run all tests
go test -v ./storage/clickhouse/...

# Run tests with timeout
go test -v -timeout 5m ./storage/clickhouse/...
```

### Run Specific Test Suites

```bash
# Run only unit tests (fast)
go test -v -short ./storage/clickhouse/

# Run only storage tests
go test -v -run TestStorage ./storage/clickhouse/

# Run only integration tests
go test -v -run TestIntegration ./storage/clickhouse/

# Run only functional tests (requires internet)
go test -v -run TestFunctional ./storage/clickhouse/

# Run only analytics tests
go test -v -run TestAnalytics ./storage/clickhouse/
```

### Run Individual Tests

```bash
# Run a specific test
go test -v -run TestSaveEvent ./storage/clickhouse/

# Run functional ingestion test
go test -v -run TestFunctionalIngestFromRelay ./storage/clickhouse/
```

### Skip Slow Tests

```bash
# Skip functional tests (which connect to external relays)
go test -v -short ./storage/clickhouse/
```

## Test Coverage

Generate test coverage report:

```bash
# Generate coverage
go test -coverprofile=coverage.out ./storage/clickhouse/

# View coverage in browser
go tool cover -html=coverage.out

# View coverage summary
go tool cover -func=coverage.out
```

## Test Categories

### Unit Tests (storage_test.go)

- `TestNewStorage` - Storage initialization
- `TestPing` - Database connectivity
- `TestExtractAllTags` - Tag extraction logic
- `TestSaveEvent` - Single event save
- `TestQueryEvents` - Event querying
- `TestCountEvents` - Event counting
- `TestStats` - Storage statistics
- `TestClose` - Graceful shutdown

### Integration Tests (storage_integration_test.go)

- `TestInsertQueryFlow` - Complete insert → query flow
- `TestInsertCountFlow` - Complete insert → count flow
- `TestFilterCombinations` - Complex filter scenarios
- `TestConcurrentInserts` - Concurrent event insertion (500 events)
- `TestDeduplication` - Event deduplication
- `TestLargeQueryResults` - Large result sets (1000+ events)
- `TestComplexTagFilters` - Multi-tag filtering
- `TestQueryPerformance` - Query performance benchmarking

### Functional Tests (storage_functional_test.go)

⚠️ **Requires internet connection to relay.nostr.net**

- `TestFunctionalIngestFromRelay` - Ingest real events from Nostr relay
- `TestFunctionalIngestMultipleKinds` - Ingest various event kinds
- `TestFunctionalIngestWithTags` - Ingest and query tagged events
- `TestFunctionalStressIngest` - High-volume ingestion (500+ events)
- `TestFunctionalQueryAfterIngest` - Immediate queryability

### Analytics Tests (analytics_test.go)

- `TestNewAnalyticsService` - Analytics service initialization
- `TestGetActiveUsers` - Active user statistics
- `TestTopUsersByFollowers` - Follower rankings
- `TestGetEventKindStats` - Event kind statistics
- `TestGetTrendingHashtags` - Trending hashtag analysis
- `TestGetTopEngagedEvents` - Engagement metrics
- `TestGetGrowthMetrics` - Network growth tracking
- `TestGetContentSizeDistribution` - Content size analysis
- `TestGetTrendingPosts` - Trending posts algorithm
- `TestRefreshHotPosts` - Hot posts refresh
- `TestSampleEvents` - Event sampling
- `TestAnalyticsWithRealData` - End-to-end analytics with realistic data

## Continuous Integration

Example GitHub Actions workflow:

```yaml
name: Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest

    services:
      clickhouse:
        image: clickhouse/clickhouse-server:24.1
        ports:
          - 9000:9000
          - 8123:8123
        options: >-
          --health-cmd "clickhouse-client --query 'SELECT 1'"
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5

    steps:
      - uses: actions/checkout@v3

      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.24'

      - name: Run tests
        run: go test -v -timeout 10m ./storage/clickhouse/...
        env:
          CLICKHOUSE_DSN: clickhouse://localhost:9000/nostr
```

## Troubleshooting

### ClickHouse Connection Issues

```bash
# Check if ClickHouse is running
docker ps | grep clickhouse

# Check ClickHouse logs
docker-compose -f docker-compose.test.yml logs clickhouse-test

# Restart ClickHouse
docker-compose -f docker-compose.test.yml restart
```

### Tests Timing Out

Increase test timeout:

```bash
go test -v -timeout 10m ./storage/clickhouse/...
```

### Functional Tests Failing

Functional tests require internet access to relay.nostr.net. If the relay is down or slow:

```bash
# Skip functional tests
go test -v -short ./storage/clickhouse/
```

### Database Migration Issues

If migrations fail, reset the database:

```bash
# Stop ClickHouse
docker-compose -f docker-compose.test.yml down -v

# Start fresh
docker-compose -f docker-compose.test.yml up -d
```

## Performance Benchmarks

Expected performance (on local ClickHouse):

- **Single insert**: < 100ms
- **Batch insert (100 events)**: < 200ms
- **Query by ID**: < 50ms
- **Query by filter**: < 200ms
- **Count query**: < 100ms
- **Concurrent inserts** (500 events): < 1s

## Test Data Cleanup

Tests create data in the ClickHouse database. To clean up:

```bash
# Connect to ClickHouse
docker exec -it clickhouse-test clickhouse-client

# Drop test tables
DROP DATABASE IF EXISTS nostr;

# Exit
exit
```

Or simply restart with clean volumes:

```bash
docker-compose -f docker-compose.test.yml down -v
docker-compose -f docker-compose.test.yml up -d
```

## Coverage Goals

Target test coverage: **80%+**

Current coverage by file:
- `storage.go` - Core storage operations
- `insert.go` - Batch insertion and tag extraction
- `query.go` - Query building and event scanning
- `count.go` - Count queries
- `analytics.go` - Analytics and reporting

## Contributing

When adding new features:

1. Add corresponding unit tests
2. Add integration tests if the feature involves workflows
3. Add functional tests if the feature affects real-world usage
4. Update this documentation
5. Ensure all tests pass before submitting PR

## Resources

- [ClickHouse Documentation](https://clickhouse.com/docs)
- [go-nostr Documentation](https://github.com/nbd-wtf/go-nostr)
- [Nostr Protocol Spec](https://github.com/nostr-protocol/nips)

package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

var testAnalytics *AnalyticsService

// setupAnalytics creates analytics service for testing
func setupAnalytics(t *testing.T) *AnalyticsService {
	t.Helper()

	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	if testAnalytics == nil {
		testAnalytics = NewAnalyticsService(testStorage.db)

		// Create analytics tables
		if err := createAnalyticsTables(testStorage.db); err != nil {
			t.Fatalf("Failed to create analytics tables: %v", err)
		}
	}

	return testAnalytics
}

// createAnalyticsTables creates necessary analytics tables
func createAnalyticsTables(db *sql.DB) error {
	ctx := context.Background()

	tables := []string{
		// User profiles table
		`CREATE TABLE IF NOT EXISTS nostr.user_profiles (
			pubkey String,
			name String,
			nip05 String,
			has_nip05 UInt8,
			has_lightning UInt8,
			last_updated UInt32
		) ENGINE = ReplacingMergeTree(last_updated)
		ORDER BY pubkey
		PRIMARY KEY pubkey`,

		// Follower counts table
		`CREATE TABLE IF NOT EXISTS nostr.follower_counts (
			pubkey String,
			follower_count UInt64,
			following_count UInt64,
			updated_at UInt32
		) ENGINE = SummingMergeTree(follower_count, following_count)
		ORDER BY (pubkey, updated_at)
		PRIMARY KEY pubkey`,

		// Daily active users table
		`CREATE TABLE IF NOT EXISTS nostr.daily_active_users (
			date Date,
			active_users AggregateFunction(uniq, String),
			total_events UInt64,
			text_notes UInt64,
			reactions UInt64,
			reposts UInt64,
			zaps UInt64
		) ENGINE = AggregatingMergeTree()
		ORDER BY date
		PRIMARY KEY date`,

		// Hourly stats table
		`CREATE TABLE IF NOT EXISTS nostr.hourly_stats (
			hour DateTime,
			kind UInt16,
			event_count UInt64,
			unique_authors AggregateFunction(uniq, String),
			total_size UInt64
		) ENGINE = AggregatingMergeTree()
		ORDER BY (kind, hour)
		PRIMARY KEY (kind, hour)`,

		// Trending hashtags table
		`CREATE TABLE IF NOT EXISTS nostr.trending_hashtags (
			date Date,
			hour UInt8,
			hashtag String,
			usage_count UInt64,
			unique_authors UInt64
		) ENGINE = SummingMergeTree()
		ORDER BY (date, hour, hashtag)
		PRIMARY KEY (date, hashtag)`,

		// Event engagement table
		`CREATE TABLE IF NOT EXISTS nostr.event_engagement (
			event_id String,
			reply_count UInt32,
			reaction_count UInt32,
			repost_count UInt32,
			zap_count UInt32,
			zap_total_sats UInt64,
			last_updated UInt32
		) ENGINE = SummingMergeTree()
		ORDER BY (event_id, last_updated)
		PRIMARY KEY event_id`,

		// Hot posts table
		`CREATE TABLE IF NOT EXISTS nostr.hot_posts (
			event_id String,
			author_pubkey String,
			created_at UInt32,
			kind UInt16,
			reply_count UInt32,
			reaction_count UInt32,
			repost_count UInt32,
			zap_count UInt32,
			zap_total_sats UInt64,
			engagement_score Float64,
			hot_score Float64,
			hour_bucket DateTime,
			last_updated UInt32
		) ENGINE = ReplacingMergeTree(last_updated)
		ORDER BY (hour_bucket, hot_score)
		PRIMARY KEY event_id`,

		// Content stats table
		`CREATE TABLE IF NOT EXISTS nostr.content_stats (
			date Date,
			kind UInt16,
			tiny_count UInt64,
			short_count UInt64,
			medium_count UInt64,
			long_count UInt64,
			huge_count UInt64,
			avg_size Float64
		) ENGINE = SummingMergeTree()
		ORDER BY (date, kind)
		PRIMARY KEY (date, kind)`,
	}

	for _, table := range tables {
		if _, err := db.ExecContext(ctx, table); err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}

	return nil
}

// TestNewAnalyticsService tests analytics service creation
func TestNewAnalyticsService(t *testing.T) {
	if testStorage == nil {
		t.Skip("Test storage not available")
	}

	analytics := NewAnalyticsService(testStorage.db)
	if analytics == nil {
		t.Error("Expected non-nil analytics service")
	}
	if analytics.db == nil {
		t.Error("Expected non-nil database connection")
	}
}

// TestGetActiveUsers tests active users analytics
func TestGetActiveUsers(t *testing.T) {
	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Insert test events
	for i := 0; i < 10; i++ {
		event := createTestEvent(t, 1, fmt.Sprintf("active user test %d", i))
		testStorage.SaveEvent(nil, &event)
	}
	time.Sleep(300 * time.Millisecond)

	// Query active users
	startDate := time.Now().Add(-24 * time.Hour)
	endDate := time.Now().Add(1 * time.Hour)

	report, err := analytics.GetActiveUsers(ctx, startDate, endDate, 0)
	if err != nil {
		t.Fatalf("GetActiveUsers failed: %v", err)
	}

	if report == nil {
		t.Fatal("Expected non-nil report")
	}

	t.Logf("Active users report: %+v", report)

	if report.TotalActiveUsers == 0 {
		t.Error("Expected at least one active user")
	}
}

// TestTopUsersByFollowers tests follower analytics
func TestTopUsersByFollowers(t *testing.T) {
	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Insert test follower data
	pubkey1 := nostr.GeneratePrivateKey()
	pubkey2 := nostr.GeneratePrivateKey()

	_, err := testStorage.db.ExecContext(ctx, `
		INSERT INTO nostr.follower_counts (pubkey, follower_count, following_count, updated_at)
		VALUES (?, 100, 50, ?), (?, 200, 75, ?)
	`, pubkey1, uint32(time.Now().Unix()), pubkey2, uint32(time.Now().Unix()))

	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Query top users
	users, err := analytics.TopUsersByFollowers(ctx, 10)
	if err != nil {
		t.Fatalf("TopUsersByFollowers failed: %v", err)
	}

	if len(users) == 0 {
		t.Error("Expected at least one user")
	}

	t.Logf("Top users: %+v", users)
}

// TestGetEventKindStats tests event kind statistics
func TestGetEventKindStats(t *testing.T) {
	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Insert test events of different kinds
	for kind := 1; kind <= 3; kind++ {
		for i := 0; i < 5; i++ {
			event := createTestEvent(t, kind, fmt.Sprintf("kind %d event %d", kind, i))
			testStorage.SaveEvent(nil, &event)
		}
	}
	time.Sleep(300 * time.Millisecond)

	// Query event kind stats
	startDate := time.Now().Add(-24 * time.Hour)
	endDate := time.Now().Add(1 * time.Hour)
	kinds := []int{1, 2, 3}

	stats, err := analytics.GetEventKindStats(ctx, kinds, startDate, endDate)
	if err != nil {
		t.Fatalf("GetEventKindStats failed: %v", err)
	}

	t.Logf("Event kind stats: %+v", stats)
}

// TestGetTrendingHashtags tests trending hashtag analytics
func TestGetTrendingHashtags(t *testing.T) {
	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Insert test hashtag data
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	hashtags := []string{"bitcoin", "nostr", "lightning"}
	for _, tag := range hashtags {
		_, err := testStorage.db.ExecContext(ctx, `
			INSERT INTO nostr.trending_hashtags (date, hour, hashtag, usage_count, unique_authors)
			VALUES (?, ?, ?, ?, ?)
		`, today, now.Hour(), tag, 10, 5)

		if err != nil {
			t.Fatalf("Failed to insert hashtag data: %v", err)
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Query trending hashtags
	trending, err := analytics.GetTrendingHashtags(ctx, 24, 10)
	if err != nil {
		t.Fatalf("GetTrendingHashtags failed: %v", err)
	}

	if len(trending) == 0 {
		t.Error("Expected at least one trending hashtag")
	}

	t.Logf("Trending hashtags: %+v", trending)
}

// TestGetTopEngagedEvents tests engagement analytics
func TestGetTopEngagedEvents(t *testing.T) {
	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Create and insert test events
	event1 := createTestEvent(t, 1, "Engagement test 1")
	event2 := createTestEvent(t, 1, "Engagement test 2")

	testStorage.SaveEvent(nil, &event1)
	testStorage.SaveEvent(nil, &event2)
	time.Sleep(300 * time.Millisecond)

	// Insert engagement data
	_, err := testStorage.db.ExecContext(ctx, `
		INSERT INTO nostr.event_engagement
		(event_id, reply_count, reaction_count, repost_count, zap_count, zap_total_sats, last_updated)
		VALUES (?, 10, 20, 5, 3, 1000, ?), (?, 5, 15, 2, 1, 500, ?)
	`, event1.ID, uint32(time.Now().Unix()), event2.ID, uint32(time.Now().Unix()))

	if err != nil {
		t.Fatalf("Failed to insert engagement data: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Query top engaged events
	events, err := analytics.GetTopEngagedEvents(ctx, 24, 10)
	if err != nil {
		t.Fatalf("GetTopEngagedEvents failed: %v", err)
	}

	if len(events) == 0 {
		t.Error("Expected at least one engaged event")
	}

	t.Logf("Top engaged events: %+v", events)
}

// TestGetGrowthMetrics tests network growth metrics
func TestGetGrowthMetrics(t *testing.T) {
	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Insert events from multiple users over time
	for i := 0; i < 20; i++ {
		event := createTestEvent(t, 1, fmt.Sprintf("growth test %d", i))
		testStorage.SaveEvent(nil, &event)
	}
	time.Sleep(300 * time.Millisecond)

	// Query growth metrics
	metrics, err := analytics.GetGrowthMetrics(ctx, 7)
	if err != nil {
		t.Fatalf("GetGrowthMetrics failed: %v", err)
	}

	t.Logf("Growth metrics: %+v", metrics)
}

// TestGetContentSizeDistribution tests content size distribution
func TestGetContentSizeDistribution(t *testing.T) {
	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Insert test content stats
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	_, err := testStorage.db.ExecContext(ctx, `
		INSERT INTO nostr.content_stats
		(date, kind, tiny_count, short_count, medium_count, long_count, huge_count, avg_size)
		VALUES (?, 1, 100, 50, 20, 5, 1, 250.5)
	`, today)

	if err != nil {
		t.Fatalf("Failed to insert content stats: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Query content size distribution
	dist, err := analytics.GetContentSizeDistribution(ctx, 7)
	if err != nil {
		t.Fatalf("GetContentSizeDistribution failed: %v", err)
	}

	if len(dist) == 0 {
		t.Error("Expected at least one content distribution entry")
	}

	t.Logf("Content size distribution: %+v", dist)
}

// TestGetTrendingPosts tests trending posts analytics
func TestGetTrendingPosts(t *testing.T) {
	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Create test events
	event := createTestEvent(t, 1, "Trending post test")
	testStorage.SaveEvent(nil, &event)
	time.Sleep(300 * time.Millisecond)

	// Insert hot posts data
	now := time.Now()
	hourBucket := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, time.UTC)

	_, err := testStorage.db.ExecContext(ctx, `
		INSERT INTO nostr.hot_posts
		(event_id, author_pubkey, created_at, kind, reply_count, reaction_count, repost_count,
		 zap_count, zap_total_sats, engagement_score, hot_score, hour_bucket, last_updated)
		VALUES (?, ?, ?, 1, 10, 20, 5, 3, 1000, 100.0, 95.5, ?, ?)
	`, event.ID, event.PubKey, uint32(event.CreatedAt), hourBucket, uint32(time.Now().Unix()))

	if err != nil {
		t.Fatalf("Failed to insert hot posts data: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Query trending posts
	posts, err := analytics.GetTrendingPosts(ctx, 24, 0, 10)
	if err != nil {
		t.Fatalf("GetTrendingPosts failed: %v", err)
	}

	if len(posts) == 0 {
		t.Error("Expected at least one trending post")
	}

	t.Logf("Trending posts: %+v", posts)
}

// TestRefreshHotPosts tests hot posts refresh
func TestRefreshHotPosts(t *testing.T) {
	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Create test events
	for i := 0; i < 5; i++ {
		event := createTestEvent(t, 1, fmt.Sprintf("hot post %d", i))
		testStorage.SaveEvent(nil, &event)
	}
	time.Sleep(300 * time.Millisecond)

	// Refresh hot posts
	if err := analytics.RefreshHotPosts(ctx, 24); err != nil {
		t.Fatalf("RefreshHotPosts failed: %v", err)
	}

	t.Log("Successfully refreshed hot posts")
}

// TestSampleEvents tests event sampling
func TestSampleEvents(t *testing.T) {
	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Insert test events
	for i := 0; i < 100; i++ {
		event := createTestEvent(t, 1, fmt.Sprintf("sample test %d", i))
		testStorage.SaveEvent(nil, &event)
	}
	time.Sleep(300 * time.Millisecond)

	// Sample events
	sampleRate := 0.1 // 10%
	samples, err := analytics.SampleEvents(ctx, sampleRate, "")
	if err != nil {
		t.Fatalf("SampleEvents failed: %v", err)
	}

	t.Logf("Sampled %d events with rate %.2f", len(samples), sampleRate)

	// Sample should return some events (probabilistic, so might be 0 with small datasets)
	if len(samples) > 100 {
		t.Errorf("Sample returned too many events: %d", len(samples))
	}
}

// TestAnalyticsWithRealData tests analytics with realistic data
func TestAnalyticsWithRealData(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping analytics real data test in short mode")
	}

	analytics := setupAnalytics(t)
	ctx := context.Background()

	// Create realistic test data
	now := time.Now()

	// Create users with different engagement levels
	users := make([]string, 10)
	for i := 0; i < 10; i++ {
		users[i] = nostr.GeneratePrivateKey()
	}

	// Create events over the past week
	for day := 0; day < 7; day++ {
		eventTime := now.Add(-time.Duration(day) * 24 * time.Hour)

		for userIdx := 0; userIdx < len(users); userIdx++ {
			// Each user posts 1-5 events per day
			numEvents := 1 + (userIdx % 5)

			for e := 0; e < numEvents; e++ {
				event := nostr.Event{
					CreatedAt: nostr.Timestamp(eventTime.Unix()),
					Kind:      1,
					Tags:      nostr.Tags{{"t", "test"}, {"t", "analytics"}},
					Content:   fmt.Sprintf("Test event from user %d on day %d, event %d", userIdx, day, e),
				}

				sk := users[userIdx]
				if err := event.Sign(sk); err != nil {
					t.Fatalf("Failed to sign event: %v", err)
				}

				testStorage.SaveEvent(nil, &event)
			}
		}
	}

	// Wait for all events to be inserted
	time.Sleep(1 * time.Second)

	// Run various analytics queries
	t.Run("ActiveUsers", func(t *testing.T) {
		startDate := now.Add(-7 * 24 * time.Hour)
		endDate := now.Add(1 * time.Hour)

		report, err := analytics.GetActiveUsers(ctx, startDate, endDate, 0)
		if err != nil {
			t.Fatalf("GetActiveUsers failed: %v", err)
		}

		t.Logf("Weekly active users: %d", report.TotalActiveUsers)
		t.Logf("Total events: %d", report.EventCount)
		t.Logf("Avg events per user: %.2f", report.AvgEventsPerUser)

		if report.TotalActiveUsers < 5 {
			t.Errorf("Expected at least 5 active users, got %d", report.TotalActiveUsers)
		}
	})

	t.Run("EventKindStats", func(t *testing.T) {
		startDate := now.Add(-7 * 24 * time.Hour)
		endDate := now.Add(1 * time.Hour)

		stats, err := analytics.GetEventKindStats(ctx, []int{1}, startDate, endDate)
		if err != nil {
			t.Fatalf("GetEventKindStats failed: %v", err)
		}

		t.Logf("Event kind stats entries: %d", len(stats))
	})

	t.Run("GrowthMetrics", func(t *testing.T) {
		metrics, err := analytics.GetGrowthMetrics(ctx, 7)
		if err != nil {
			t.Fatalf("GetGrowthMetrics failed: %v", err)
		}

		t.Logf("Growth metrics entries: %d", len(metrics))

		for _, metric := range metrics {
			t.Logf("Period %s: %d new users, %d active users, %d events",
				metric.Period, metric.NewUsers, metric.TotalActiveUsers, metric.EventsCreated)
		}
	})
}

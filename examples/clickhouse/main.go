package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pippellia-btc/rely"
	"github.com/pippellia-btc/rely/storage/clickhouse"
)

func main() {
	// Parse command-line flags
	var (
		listenAddr = flag.String("listen", "0.0.0.0:3334", "Address to listen on")
		clickhouseDSN = flag.String("clickhouse", "clickhouse://localhost:9000/nostr", "ClickHouse DSN")
		batchSize = flag.Int("batch-size", 1000, "Batch size for event insertion")
		flushInterval = flag.Duration("flush-interval", 1*time.Second, "Max time before flushing batch")
		domain = flag.String("domain", "localhost", "Relay domain (for NIP-42)")
	)
	flag.Parse()

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down gracefully...")
		cancel()
	}()

	// Initialize ClickHouse storage
	log.Println("Connecting to ClickHouse...")
	cfg := clickhouse.Config{
		DSN:           *clickhouseDSN,
		BatchSize:     *batchSize,
		FlushInterval: *flushInterval,
		MaxOpenConns:  10,
		MaxIdleConns:  5,
	}

	storage, err := clickhouse.NewStorage(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize ClickHouse storage: %v", err)
	}
	defer storage.Close()

	// Verify connection and print stats
	if err := storage.Ping(ctx); err != nil {
		log.Fatalf("Failed to ping ClickHouse: %v", err)
	}

	stats, err := storage.Stats()
	if err != nil {
		log.Printf("Warning: failed to get storage stats: %v", err)
	} else {
		log.Printf("Storage stats: %d events, %.2f GB, time range: %s - %s",
			stats.TotalEvents,
			float64(stats.TotalBytes)/(1024*1024*1024),
			time.Unix(int64(stats.OldestEvent), 0).Format(time.RFC3339),
			time.Unix(int64(stats.NewestEvent), 0).Format(time.RFC3339),
		)
	}

	// Create relay with ClickHouse storage hooks
	log.Println("Starting Nostr relay...")
	relay := rely.NewRelay(
		rely.WithDomain(*domain),
		rely.WithQueueCapacity(2048),
		rely.WithMaxProcessors(8),
		rely.WithClientResponseLimit(500),
	)

	// Hook up storage methods
	relay.On.Event = storage.SaveEvent
	relay.On.Req = storage.QueryEvents
	relay.On.Count = storage.CountEvents

	// Optional: Add connection logging
	relay.On.Connect = func(c rely.Client) {
		log.Printf("Client connected: %s", c.IP())
	}

	relay.On.Disconnect = func(c rely.Client) {
		log.Printf("Client disconnected: %s (duration: %s)", c.IP(), time.Since(c.ConnectedAt()))
	}

	// Optional: Add authentication handler
	relay.On.Auth = func(c rely.Client) {
		log.Printf("Client authenticated: %s (pubkey: %s)", c.IP(), c.Pubkey())
	}

	// Start periodic stats reporting
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stats, err := storage.Stats()
				if err != nil {
					log.Printf("Failed to get stats: %v", err)
					continue
				}

				log.Printf("Relay stats: clients=%d, subscriptions=%d, queue=%.1f%%, storage=%d events (%.2f GB)",
					relay.Clients(),
					relay.Subscriptions(),
					relay.QueueLoad()*100,
					stats.TotalEvents,
					float64(stats.TotalBytes)/(1024*1024*1024),
				)
			}
		}
	}()

	// Start relay server
	log.Printf("Listening on %s", *listenAddr)
	if err := relay.StartAndServe(ctx, *listenAddr); err != nil {
		log.Fatalf("Relay error: %v", err)
	}

	log.Println("Relay stopped")
}

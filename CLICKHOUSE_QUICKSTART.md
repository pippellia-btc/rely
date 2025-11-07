# ClickHouse Storage - Quick Start Guide

Get your high-performance Nostr relay running with ClickHouse storage in minutes!

## Prerequisites

- Go 1.24+
- ClickHouse server (23.x or higher)
- Basic understanding of Nostr protocol

## Step 1: Install ClickHouse

### Using Docker (Recommended for Development)

```bash
docker run -d \
  --name clickhouse-nostr \
  -p 9000:9000 \
  -p 8123:8123 \
  -v clickhouse-data:/var/lib/clickhouse \
  clickhouse/clickhouse-server
```

### Using Package Manager

**Ubuntu/Debian:**
```bash
sudo apt-get install -y apt-transport-https ca-certificates curl gnupg
curl -fsSL 'https://packages.clickhouse.com/rpm/lts/repodata/repomd.xml.key' | sudo gpg --dearmor -o /usr/share/keyrings/clickhouse-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/clickhouse-keyring.gpg] https://packages.clickhouse.com/deb stable main" | sudo tee /etc/apt/sources.list.d/clickhouse.list
sudo apt-get update
sudo apt-get install -y clickhouse-server clickhouse-client
sudo systemctl start clickhouse-server
```

**macOS:**
```bash
brew install clickhouse
brew services start clickhouse
```

## Step 2: Initialize Database Schema

```bash
cd storage/clickhouse/migrations
./init_schema.sh
```

Or manually:
```bash
clickhouse-client < 001_create_database.sql
clickhouse-client < 002_create_events_table.sql
clickhouse-client < 003_create_materialized_views.sql
clickhouse-client < 004_create_analytics_tables.sql
clickhouse-client < 005_create_indexes.sql
```

## Step 3: Run Your Relay

```bash
cd examples/clickhouse
go run main.go
```

Your relay is now running on `http://localhost:3334`!

## Step 4: Test It

### Using nak (Nostr Army Knife)

```bash
# Install nak
go install github.com/fiatjaf/nak@latest

# Connect and publish an event
echo "Hello from my ClickHouse relay!" | nak event -c "Hello from my relay" | nak publish ws://localhost:3334

# Query events
nak req -k 1 --limit 10 ws://localhost:3334
```

### Using websocat

```bash
# Install websocat
cargo install websocat

# Connect to relay
websocat ws://localhost:3334

# Send a REQ (paste and press Enter)
["REQ","sub1",{"kinds":[1],"limit":10}]

# You should see events returned as:
# ["EVENT","sub1",{...event data...}]
```

## Configuration Options

### Command Line Flags

```bash
go run main.go \
  --listen "0.0.0.0:3334" \
  --clickhouse "clickhouse://localhost:9000/nostr" \
  --batch-size 1000 \
  --flush-interval 1s \
  --domain "relay.yourdomain.com"
```

### Environment Variables

```bash
export CLICKHOUSE_DSN="clickhouse://user:password@host:9000/nostr"
export BATCH_SIZE=2000
export FLUSH_INTERVAL=2s
```

## Performance Tuning

### For High-Traffic Relays

```bash
go run main.go \
  --batch-size 5000 \
  --flush-interval 500ms \
  --listen "0.0.0.0:3334"
```

### ClickHouse Server Settings

Edit `/etc/clickhouse-server/config.xml`:

```xml
<clickhouse>
    <max_memory_usage>60000000000</max_memory_usage>
    <max_threads>16</max_threads>
    <background_pool_size>16</background_pool_size>
    <mark_cache_size>10737418240</mark_cache_size>
</clickhouse>
```

## Monitoring

### View Storage Stats

```bash
clickhouse-client --query "
SELECT
    table,
    formatReadableSize(sum(bytes)) as size,
    sum(rows) as rows
FROM system.parts
WHERE database = 'nostr' AND active = 1
GROUP BY table
"
```

### View Recent Activity

```bash
clickhouse-client --query "
SELECT count()
FROM nostr.events
WHERE relay_received_at >= toUnixTimestamp(now() - INTERVAL 1 MINUTE)
"
```

### Top Authors

```bash
clickhouse-client --query "
SELECT
    pubkey,
    count() as event_count
FROM nostr.events FINAL
WHERE created_at >= toUnixTimestamp(now() - INTERVAL 1 DAY)
GROUP BY pubkey
ORDER BY event_count DESC
LIMIT 10
"
```

## Common Issues

### Connection Refused

Make sure ClickHouse is running:
```bash
# Check if running
ps aux | grep clickhouse

# Test connection
clickhouse-client --query "SELECT 1"

# Check logs
tail -f /var/log/clickhouse-server/clickhouse-server.log
```

### Permission Denied

Grant permissions to the database user:
```sql
GRANT ALL ON nostr.* TO default;
```

### Slow Performance

```sql
-- Force merge to optimize storage
OPTIMIZE TABLE nostr.events FINAL;

-- Check for slow queries
SELECT query, query_duration_ms
FROM system.query_log
WHERE type = 'QueryFinish'
  AND query_duration_ms > 1000
ORDER BY query_duration_ms DESC
LIMIT 10;
```

## Next Steps

- Read the [Full Design Document](CLICKHOUSE_DESIGN_PLAN.md) for architecture details
- Check the [Storage README](storage/clickhouse/README.md) for advanced features
- Explore [analytics queries](storage/clickhouse/README.md#analytics-queries) for insights
- Set up [backups](storage/clickhouse/README.md#backup) for production use

## Production Deployment

### Using Systemd

Create `/etc/systemd/system/nostr-relay.service`:

```ini
[Unit]
Description=Nostr Relay with ClickHouse
After=network.target clickhouse-server.service

[Service]
Type=simple
User=nostr
WorkingDirectory=/opt/nostr-relay
ExecStart=/opt/nostr-relay/relay \
  --listen "0.0.0.0:3334" \
  --clickhouse "clickhouse://localhost:9000/nostr" \
  --batch-size 2000 \
  --domain "relay.yourdomain.com"
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Enable and start:
```bash
sudo systemctl enable nostr-relay
sudo systemctl start nostr-relay
sudo systemctl status nostr-relay
```

### Behind Nginx

```nginx
upstream nostr_relay {
    server 127.0.0.1:3334;
}

server {
    listen 443 ssl http2;
    server_name relay.yourdomain.com;

    ssl_certificate /etc/letsencrypt/live/relay.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/relay.yourdomain.com/privkey.pem;

    location / {
        proxy_pass http://nostr_relay;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "Upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # WebSocket timeout
        proxy_read_timeout 86400;
    }
}
```

## Support

- GitHub Issues: https://github.com/pippellia-btc/rely/issues
- ClickHouse Docs: https://clickhouse.com/docs
- Nostr NIPs: https://github.com/nostr-protocol/nips

## License

Same as rely - see LICENSE file

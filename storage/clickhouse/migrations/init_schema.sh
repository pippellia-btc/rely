#!/bin/bash
# Initialize ClickHouse schema for Nostr relay

set -e

# Default ClickHouse connection settings
CLICKHOUSE_HOST="${CLICKHOUSE_HOST:-localhost}"
CLICKHOUSE_PORT="${CLICKHOUSE_PORT:-9000}"
CLICKHOUSE_USER="${CLICKHOUSE_USER:-default}"
CLICKHOUSE_PASSWORD="${CLICKHOUSE_PASSWORD:-}"

# Build clickhouse-client command
if [ -n "$CLICKHOUSE_PASSWORD" ]; then
    CH_CLIENT="clickhouse-client --host=$CLICKHOUSE_HOST --port=$CLICKHOUSE_PORT --user=$CLICKHOUSE_USER --password=$CLICKHOUSE_PASSWORD"
else
    CH_CLIENT="clickhouse-client --host=$CLICKHOUSE_HOST --port=$CLICKHOUSE_PORT --user=$CLICKHOUSE_USER"
fi

echo "Initializing ClickHouse schema for Nostr relay..."
echo "Host: $CLICKHOUSE_HOST:$CLICKHOUSE_PORT"
echo ""

# Test connection
echo "Testing connection..."
$CH_CLIENT --query "SELECT 1" > /dev/null 2>&1 || {
    echo "Error: Cannot connect to ClickHouse"
    echo "Please check your connection settings"
    exit 1
}
echo "✓ Connection successful"

# Run migrations in order
for migration in *.sql; do
    echo ""
    echo "Running migration: $migration"
    $CH_CLIENT --multiquery < "$migration" || {
        echo "Error: Migration failed"
        exit 1
    }
    echo "✓ $migration completed"
done

echo ""
echo "========================================="
echo "Schema initialization completed!"
echo "========================================="
echo ""

# Show created tables
echo "Created tables:"
$CH_CLIENT --query "SHOW TABLES FROM nostr"

echo ""
echo "Storage statistics:"
$CH_CLIENT --query "
    SELECT
        table,
        formatReadableSize(sum(bytes)) as size,
        sum(rows) as rows
    FROM system.parts
    WHERE database = 'nostr' AND active = 1
    GROUP BY table
    ORDER BY table
"

echo ""
echo "Ready to start your relay!"
echo ""
echo "Example usage:"
echo "  cd ../../examples/clickhouse"
echo "  go run main.go --clickhouse \"clickhouse://$CLICKHOUSE_HOST:$CLICKHOUSE_PORT/nostr\""

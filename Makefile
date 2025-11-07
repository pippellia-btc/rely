.PHONY: test test-storage test-unit test-integration test-functional test-analytics test-short test-coverage docker-test-up docker-test-down docker-test-clean help

# Default target
help:
	@echo "Available targets:"
	@echo "  make test              - Run all tests"
	@echo "  make test-storage      - Run all storage tests"
	@echo "  make test-unit         - Run unit tests only"
	@echo "  make test-integration  - Run integration tests"
	@echo "  make test-functional   - Run functional tests (requires internet)"
	@echo "  make test-analytics    - Run analytics tests"
	@echo "  make test-short        - Run tests in short mode (skip slow tests)"
	@echo "  make test-coverage     - Generate coverage report"
	@echo "  make docker-test-up    - Start ClickHouse test environment"
	@echo "  make docker-test-down  - Stop ClickHouse test environment"
	@echo "  make docker-test-clean - Clean ClickHouse test environment"

# Run all tests
test:
	go test -v -timeout 10m ./...

# Run all storage tests
test-storage:
	go test -v -timeout 10m ./storage/clickhouse/...

# Run unit tests only (fast)
test-unit:
	go test -v -short -timeout 2m ./storage/clickhouse/ -run "^Test[^F].*"

# Run integration tests
test-integration:
	go test -v -timeout 5m ./storage/clickhouse/ -run "TestInsert.*|TestQuery.*|TestCount.*|TestConcurrent.*|TestDeduplic.*|TestLarge.*|TestComplex.*"

# Run functional tests (requires internet connection)
test-functional:
	go test -v -timeout 10m ./storage/clickhouse/ -run "TestFunctional.*"

# Run analytics tests
test-analytics:
	go test -v -timeout 5m ./storage/clickhouse/ -run "TestAnalytics.*|TestGet.*|TestRefresh.*|TestSample.*"

# Run tests in short mode (skip slow tests)
test-short:
	go test -v -short -timeout 2m ./storage/clickhouse/...

# Generate coverage report
test-coverage:
	go test -coverprofile=coverage.out -covermode=atomic ./storage/clickhouse/...
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out
	@echo "Coverage report generated: coverage.html"

# Start ClickHouse test environment
docker-test-up:
	docker-compose -f docker-compose.test.yml up -d
	@echo "Waiting for ClickHouse to be ready..."
	@sleep 10
	@docker-compose -f docker-compose.test.yml ps

# Stop ClickHouse test environment
docker-test-down:
	docker-compose -f docker-compose.test.yml down

# Clean ClickHouse test environment (removes volumes)
docker-test-clean:
	docker-compose -f docker-compose.test.yml down -v

# Run tests with Docker environment
docker-test: docker-test-up
	@echo "Running tests..."
	CLICKHOUSE_DSN="clickhouse://localhost:9000/nostr" go test -v -timeout 10m ./storage/clickhouse/...
	@$(MAKE) docker-test-down

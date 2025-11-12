# Examples and Testing

The `examples/` directory and the test suites are practical references for building your own relay. This document explains what each example demonstrates and how to run the available tests.

## Examples directory

Each subfolder is a self-contained Go module you can run with `go run .`. Highlights:

| Example | Focus | Notes |
| --- | --- | --- |
| `basic` | Smallest working relay | Mirrors the Quickstart; great for verifying environment setup. |
| `auth` | Custom NIP-42 handling | Shows how to accept/reject authenticated users. |
| `anti-crawlers` | Bot mitigation | Illustrates connection/IP heuristics via `Reject.Connection`. |
| `blacklist` | Content moderation | Rejects certain kinds/pubkeys. |
| `ip-rate` | Rate limiting by IP | Demonstrates combining hooks with external state. |
| `count` | NIP-45 COUNT implementation | Implements `On.Count`. |
| `dvm` / `wot` / `sparing` | Advanced business logic | Useful patterns for DVM processing, trust graphs, etc. |
| `nip11` | Custom info document | Shows `WithInfo` in action. |
| `logger` | Custom logging | Example `WithLogger` usage. |

To run an example:

```bash
cd examples/ip-rate
go run .
```

Most examples only require Go; a few expect backing services (e.g., Redis). Check the inline comments at the top of each `main.go` for prerequisites.

## Unit tests

Standard tests live alongside the package code (`*_test.go`). Run them from the repo root:

```bash
go test ./...
```

These tests cover utilities, dispatcher logic, and safety checks, and they are safe to run without external dependencies.

## Stress test

`tests/stress_test.go` simulates thousands of noisy clients, abrupt disconnects, and large event floods to smoke-test concurrency. Because it opens many goroutines and sockets, run it intentionally:

```bash
go test ./tests -run Stress -v
```

Recommendations:
- Run on a machine that can open thousands of file descriptors (configure `ulimit -n` if needed).
- Observe CPU/memory usage while the test runs; it should surface data races, deadlocks, and hook bottlenecks.
- Pair stress results with production-like logging to confirm `When.GreedyClient` and other safeguards behave as expected.

## Working with hooks

Many examples double as integration tests for hooks. When experimenting:

1. Copy the closest example into your project or run it locally.
2. Tweak hook implementations (auth, rate limiting, persistence) to match your needs.
3. Use `go test ./...` to catch regressions, then stress test before deploying.

If you build a new pattern that could help others (e.g., a persistence adapter, custom auth flow), consider contributing another example under `examples/`.

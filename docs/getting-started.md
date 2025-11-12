# Getting Started with rely

This guide walks through setting up a minimal relay backed by rely, explaining the moving pieces you need to configure before handling real client traffic.

## 1. Prerequisites

- Go 1.22 or newer
- A stable domain/hostname used for NIP-42 authentication (`WithDomain`)
- Basic familiarity with [NIP-01](https://github.com/nostr-protocol/nips/blob/master/01.md) and WebSocket hosting
- An environment for persistence/rate limiting (e.g., a database, Redis) if your relay needs them

## 2. Install and scaffold

```bash
mkdir myrelay && cd myrelay
go mod init example.com/myrelay
go get github.com/pippellia-btc/rely
```

Create `main.go`:

```go
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/pippellia-btc/rely"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	relay := rely.NewRelay(
		rely.WithDomain("relay.example.com"),
	)

	if err := relay.StartAndServe(ctx, "127.0.0.1:3334"); err != nil {
		log.Fatal(err)
	}
}
```

Run with `go run .` and connect using any Nostr client that supports custom relays.

## 3. Configure core options

| Concern | Option(s) | Notes |
| --- | --- | --- |
| Auth/NIP-42 | `WithDomain` | Required; clients sign the `relay` tag with this host. |
| Logging/observability | `WithLogger` | Provide your preconfigured `*slog.Logger`. |
| Client backpressure | `WithClientResponseLimit`, `WithQueueCapacity` | Increase when serving high fan-out clients. |
| Worker throughput | `WithMaxProcessors` | Defaults to CPU count; raise on I/O-bound workloads. |
| Websocket tuning | `WithReadBufferSize`, `WithWriteBufferSize`, `WithMaxMessageSize`, `WithPingPeriod`, `WithPongWait`, `WithWriteWait` | Match your infra (reverse proxies, MTU, latency). |
| Relay metadata | `WithInfo` | Customize NIP-11 document. |

All options are documented in [docs/configuration-and-performance.md](configuration-and-performance.md) and [`options.go`](../options.go). Most panics during startup come from invalid option values (negative queue, ping/pong mismatch, tiny message size), so double-check overrides before deploying.

## 4. Implement hooks

Rely gives you full control over behavior through `relay.Reject`, `relay.On`, and `relay.When`. At a minimum you should:

```go
relay.On.Event = func(c rely.Client, e *nostr.Event) error {
	// Persist EVENTs, validate business rules, etc.
	return store(e)
}

relay.On.Req = func(ctx context.Context, c rely.Client, filters nostr.Filters) ([]nostr.Event, error) {
	return query(filters), nil
}
```

Add rate limits, authentication logic, and monitoring via additional hooks. The execution order and best practices are covered in [docs/hooks-and-lifecycle.md](hooks-and-lifecycle.md).

## 5. Run and verify

1. `go run .`
2. Connect with a client and publish an EVENT.
3. Inspect logs for connect/auth/req activity.
4. Exercise CLOSE → REQ → EVENT flows to ensure your hooks respond as expected.

For more advanced validation (COUNT requests, stress testing, sample integrations), see [docs/examples-and-testing.md](examples-and-testing.md).

## 6. Troubleshooting

| Symptom | Likely Cause | Fix |
| --- | --- | --- |
| Immediate NIP-42 failures | Missing `WithDomain` or mismatch with client `relay` tag | Set the exact domain the client uses. |
| Panic: “max processors must be greater than 1” | `WithMaxProcessors(0)` | Provide a positive value. |
| Panic: “pong wait must be greater than ping period” | Ping/pong overrides inverted | Ensure `pongWait > pingPeriod`. |
| Clients disconnect after bursts | Response channel full | Increase `WithClientResponseLimit` and monitor `When.GreedyClient`. |
| Relay unresponsive under load | Processor queue saturated | Increase `WithQueueCapacity` and `WithMaxProcessors`, or lighten hook work. |

Once the basics run locally, containerize or embed rely inside a larger HTTP server as needed. The `examples/` directory demonstrates common production patterns (custom auth, IP rate limiting, DVM relays) if you need inspiration.

# Configuration and Performance Guide

This guide explains every functional option exposed by rely and how those settings affect throughput, memory usage, and client experience. Use it as a reference when tuning a production relay.

## Functional options

| Option | Default | Effect | When to change |
| --- | --- | --- | --- |
| `WithDomain(string)` | `""` | Sets the host used in the NIP-42 `relay` tag. | **Required** for auth; use the public hostname clients connect to. |
| `WithInfo(nip11.RelayInformationDocument)` | Software/version + supported NIPs | Overrides the NIP-11 JSON served via `Accept: application/nostr+json`. | Advertise operator info, rate limits, or additional NIPs. |
| `WithLogger(*slog.Logger)` | `slog.New(slog.NewTextHandler(os.Stdout))` | Replaces the structured logger rely uses. | Integrate with your logging/observability stack. |
| `WithQueueCapacity(int)` | `1000` | Size of the processor queue buffering incoming EVENT/REQ/COUNT work. | Increase to absorb bursts or when `On.Req` is slower than message arrival. |
| `WithMaxProcessors(int)` | `runtime.NumCPU()` | Worker goroutines draining the processor queue. | Raise for I/O-bound workloads; lower to cap CPU usage. Must be > 0. |
| `WithClientResponseLimit(int)` | `1000` | Maximum buffered responses per client connection. | Increase for high-fanout relays; decrease to enforce stricter backpressure. |
| `WithReadBufferSize(int)` / `WithWriteBufferSize(int)` | `1024` bytes | WebSocket read/write buffer sizes. | Increase if handling large EVENTs or avoid fragmentation; reduce on constrained hosts. |
| `WithMaxMessageSize(int64)` | `500_000` bytes | Maximum size of a single incoming message. | Raise for large events; lower to reject big payloads earlier. Must be ≥ 512. |
| `WithWriteWait(time.Duration)` | `10s` | Deadline for flushing outbound messages. | Increase for long-haul networks; decrease to fail faster on stalled clients. |
| `WithPongWait(time.Duration)` | `60s` | Maximum time to wait for a `PONG` after sending `PING`. | Tweak for unreliable clients; must be > `pingPeriod`. |
| `WithPingPeriod(time.Duration)` | `45s` | Frequency of keep-alive `PING`s. | Reduce for aggressive liveness detection; must be < `pongWait` and > 1s. |

All options are validated at relay creation; invalid combinations panic to surface misconfiguration early.

## Backpressure model

Each client owns a fixed-size response queue (capacity = `WithClientResponseLimit`). When a REQ arrives, rely sums the remaining capacity (`limitRemaining = responseLimit - len(queue)`) and scales every filter's `limit` so the total responses cannot exceed `limitRemaining`. Practical implications:

- Clients that fail to read responses get naturally throttled.
- Hook implementations should not bypass this mechanism (e.g., by writing directly to the websocket) unless you know the queue is not full.
- When the queue fills up, `When.GreedyClient` fires. The default handler disconnects clients after dropping 50 responses; replace it if you need a softer policy.

## Processor workers

- Incoming EVENT/REQ/COUNT messages enter a single queue sized by `WithQueueCapacity`.
- Up to `WithMaxProcessors` goroutines drain that queue and run `Reject`/`On` hooks.
- If the queue fills up:
  - New messages block until space frees up.
  - Monitor via metrics/logs; sustained fullness means either the worker count is too low or hooks are slow.
- Tune by measuring p95 hook latency and CPU utilization. For I/O-bound hooks, you can exceed CPU count; for CPU-bound logic, keep it near core count.

## Dispatcher internals

The dispatcher builds inverted indexes of subscriptions across clients so it can determine which connections should receive a broadcasted event in `O(log n)` time. Key behaviors:

- Dispatcher state is eventually consistent; each client remains the source of truth for its subscriptions.
- CLOSE messages immediately remove the subscription from the client and asynchronously update the dispatcher snapshot.
- If you need a real-time view of subscriptions, call `Client.Subscriptions()` instead of relying on dispatcher internals.

## Websocket timing

The trio `WithPingPeriod`, `WithPongWait`, and `WithWriteWait` keeps idle connections healthy:

- `pingPeriod` must be > 1s.
- `pongWait` must be greater than `pingPeriod`.
- `writeWait` should exceed the longest expected write duration (consider Nagle/MTU/slow clients). If writes exceed this deadline, the connection closes to prevent stalling the relay.

Match these values to any upstream reverse proxies—if a proxy kills idle connections at 30s, set `pingPeriod` < 30s so the relay keeps the link active.

## Memory sizing

- Processor queue memory ≈ `queueCapacity * pointerSize`.
- Per-client response queue memory ≈ `responseLimit * averageResponseSize`.
- Large `WithMaxMessageSize` values increase temporary buffer allocations; set it as low as your application allows.

Use Go's pprof tooling or `GODEBUG=gctrace=1` in staging to validate allocations before and after tuning.

## Observability tips

- Attach a custom `*slog.Logger` via `WithLogger` to include request IDs, tenant info, or structured fields your platform expects.
- Wrap hooks with middleware to record execution time and success/failure counts.
- Emit metrics when `When.GreedyClient` fires so you can identify abusive clients or insufficient response limits.
- Enable the stress test (`go test ./tests -run Stress -v`) after tuning to ensure your settings hold under randomized load. Instructions live in [docs/examples-and-testing.md](examples-and-testing.md).

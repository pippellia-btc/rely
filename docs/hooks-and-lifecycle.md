# Hooks and Lifecycle

Rely exposes a predictable set of extension points so you can inject business logic, persistence, and enforcement into every phase of a client's session. This document summarizes each hook group, the order in which they fire, and practical guidance for using them safely.

## Lifecycle overview
1. **Connection** arrives → `Reject.Connection` filters get the HTTP request and current relay stats.
2. WebSocket upgrades → `On.Connect` fires.
3. Client sends messages:
   - `EVENT` → `Reject.Event` → `On.Event`
   - `REQ` → `Reject.Req` → `On.Req`
   - `COUNT` → `Reject.Count` → `On.Count`
4. Dispatcher broadcasts matching events, applying per-client backpressure.
5. If a client falls behind, `When.GreedyClient` runs.
6. When the connection closes, `On.Disconnect` runs.

All hook functions must be goroutine-safe and return quickly; offload heavy work to separate goroutines or background workers.

## Reject hooks

| Hook | Signature | Purpose | Tips |
| --- | --- | --- | --- |
| `Reject.Connection` | `func(Stats, *http.Request) error` | Block unwanted IPs, enforce capacity, require headers. | Runs before WebSocket upgrade—no client yet. |
| `Reject.Event` | `func(Client, *nostr.Event) error` | Validate IDs/signatures, reject specific kinds. | Default hooks already verify ID/signature. |
| `Reject.Req` | `func(Client, nostr.Filters) error` | Enforce rate limits, reject unsupported filters, auth gates. | Run lightweight checks—heavy work happens in `On.Req`. |
| `Reject.Count` | `func(Client, nostr.Filters) error` | Same as above but restricted to COUNT (NIP-45). | Leave unset to allow counts through. |

All functions in a slice run sequentially; the first non-nil error stops processing, and the client gets a `NOTICE`.

## On hooks

| Hook | Signature | When it fires | Common uses |
| --- | --- | --- | --- |
| `Connect` | `func(Client)` | After the client registers but before it can send messages. | Track active sessions, send welcome notices, kick off async auth. |
| `Disconnect` | `func(Client)` | Immediately after the client is unregistered. | Cleanup resources, cancel timers, collect metrics. |
| `Auth` | `func(Client)` | After successful NIP-42 authentication. | Load per-user limits, attach metadata to the client. |
| `Event` | `func(Client, *nostr.Event) error` | For every EVENT that passes Reject hooks. | Persist events, implement moderation, broadcast to other services. |
| `Req` | `func(context.Context, Client, nostr.Filters) ([]nostr.Event, error)` | For every REQ. | Query storage, enforce custom pagination, fan out to external services. |
| `Count` | `func(Client, nostr.Filters) (int64, bool, error)` | When a COUNT request (NIP-45) is accepted. | Return approximate counts, query analytics stores. |

Notes:
- `On.Req` gets a context that is canceled when the client sends `CLOSE`. Honor the cancellation to avoid wasted work.
- Errors returned from `On.Event` or `On.Req` are converted into `NOTICE`s for the client.
- Keep `Connect`/`Disconnect` extremely fast; they run on the hot path.

## When hooks

| Hook | Trigger | Typical action |
| --- | --- | --- |
| `GreedyClient` | A client's response buffer is full (it is receiving faster than it reads). | Log the incident, downgrade rate limits, or disconnect repeat offenders. |

Avoid sending new responses inside `GreedyClient`; doing so can retrigger the hook and create recursion. If you must notify a client, guard with counters or timeouts.

## Patterns and best practices

- **Layering logic:** Chain multiple functions inside each hook slice (e.g., sanitize → auth → rate-limit) to keep each concern small and testable.
- **Concurrency:** Hooks run concurrently for different clients. Ensure your storage/cache code is thread-safe.
- **Tracing/metrics:** Wrap hooks with middleware that records execution time to identify slow paths.
- **Testing:** Write unit tests for hook helpers (e.g., signature checks) and leverage the stress test in `tests/stress_test.go` to observe hooks under load.
- **Extensibility:** If you need a new lifecycle hook, open an issue—rely is designed to add more touch points where it makes sense.

For concrete examples, inspect the programs in `examples/` (e.g., `auth`, `ip-rate`, `dvm`) and the defaults defined in [`hooks.go`](../hooks.go).

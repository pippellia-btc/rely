package rely

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Hooks provides a complete set of extension points allowing custom logic
// to be injected into the relay's lifecycle and operational flow.
//
// These functions are categorized into three groups:
//   - Reject: Preemptively blocks incoming data or connections before processing.
//   - On: Handles standard lifecycle events (Connect, Disconnect, Auth) and
//     successful data flows (Event, Req, Count).
//   - When: Triggers on special, non-standard, or warning conditions.
//
// All functions supplied must be thread-safe and must not be modified at runtime.
type Hooks struct {
	Reject RejectHooks
	On     OnHooks
	When   WhenHooks
}

func DefaultHooks() Hooks {
	return Hooks{
		Reject: DefaultRejectHooks(),
		On:     DefaultOnHooks(),
		When:   DefaultWhenHooks(),
	}
}

// RejectHooks defines optional functions that can preemptively reject
// certain actions before they are processed by the relay.
//
// Each function in a hook slice is evaluated in order. If any function
// returns a non-nil error, the corresponding input (connection, event,
// request, or count) is immediately rejected.
//
// These hooks are useful for enforcing access policies, validating input,
// or applying rate limits before the relay performs further processing.
type RejectHooks struct {
	// Connection is invoked before establishing a new client connection.
	// Returning a non-nil error rejects the connection.
	Connection slice[func(stats Stats, r *http.Request) error]

	// Event is invoked before processing an EVENT message.
	// Returning a non-nil error rejects the event.
	Event slice[func(c Client, event *nostr.Event) error]

	// Req is invoked before processing a REQ message.
	// Returning a non-nil error rejects the request.
	Req slice[func(c Client, id string, filters nostr.Filters) error]

	// Count is invoked before processing a NIP-45 COUNT request.
	// Returning a non-nil error rejects the request.
	Count slice[func(c Client, id string, filters nostr.Filters) error]

	// Negentropy is invoked before opening a NIP-77 reconciliation session.
	// Use it to enforce read access and query policies, including authentication.
	// Req hooks are not automatically applied to reconciliation requests.
	Negentropy slice[func(c Client, id string, filter nostr.Filter) error]
}

func DefaultRejectHooks() RejectHooks {
	return RejectHooks{
		Connection: []func(Stats, *http.Request) error{RegistrationFailWithin(3 * time.Second)},
		Event:      []func(Client, *nostr.Event) error{InvalidID, InvalidSignature},
	}
}

// OnHooks defines functions invoked after specific relay events occur.
// These hooks customize how the relay reacts to client actions such as
// EVENT, REQ, and COUNT messages. Each function is called only after the
// corresponding input has passed all RejectHooks (if any).
//
// OnHooks are typically used to implement custom processing, persistence,
// logging, authorization, or other side effects in response to relay activity.
type OnHooks struct {
	// Connect runs immediately after a client has been connected and registered.
	// It is guaranteed to run before the Disconnect hook of the same client.
	// This callback must be very fast to avoid blocking the hot path.
	// For longer operations, use goroutines.
	//
	// Example:
	//   relay.On.Connect = func(c Client) {
	//       go longOperation(c)
	//   }
	Connect func(c Client)

	// Disconnect runs immediately after a client has been unregistered and disconnected.
	// It is guaranteed to run after the Connect hook of the same client.
	// This callback must be very fast to avoid blocking the hot path.
	// For longer operations, use goroutines.
	//
	// Example:
	//   relay.On.Disconnect = func(c Client) {
	//       go longOperation(c)
	//   }
	Disconnect func(c Client)

	// Auth is called immediately after a client successfully authenticates.
	// It can be used to load resources tied to the client’s public key or adjust rate limits.
	Auth func(c Client)

	// Event defines how the relay processes an EVENT, allowing to define custom messages and broadcast behavior.
	// This hook is for example used to save an event in the database.
	// Return [Success] to acknowledge the event as accepted and broadcast it, or [Fail] to acknowledge a failure.
	Event func(c Client, event *nostr.Event) EventResult

	// Req defines how the relay processes a REQ with the provided id, containing one or more filters,
	// for example by querying the database for matching events.
	// The provided context is canceled if the client sends the corresponding CLOSE message.
	Req func(ctx context.Context, c Client, id string, filters nostr.Filters) ([]nostr.Event, error)

	// Count defines how the relay processes a NIP-45 COUNT request with the provided id,
	// containing one ore more filters.
	// This hook is optional (= nil). If unset, COUNT requests are rejected with [ErrUnsupportedNIP45].
	Count func(c Client, id string, filters nostr.Filters) (count int64, approx bool, err error)

	// Negentropy returns a snapshot of unique event IDs and timestamps matching
	// one NIP-77 filter and visible to this client. It must return the complete
	// selected set or an error, never silently truncate to a database query cap.
	// The context is canceled on close, replacement, disconnect, or timeout.
	// Implementations should bound database work using the configured MaxItems
	// and honor cancellation. The returned slice must not be mutated afterward.
	// If nil, NIP-77 is disabled. Event transfer still uses Req and Event hooks.
	Negentropy func(ctx context.Context, c Client, id string, filter nostr.Filter) ([]NegentropyItem, error)
}

// EventResult is returned from [OnHooks.Event] to instruct the relay on
// how to respond to the client and whether to broadcast the event.
// The zero value indicates the event was accepted and should be broadcast.
// Use the convenience functions [Success] and [Fail] to create results.
type EventResult struct {
	failed      bool
	noBroadcast bool
	reason      string
}

// Success instructs the relay to send OK(true) to the client and broadcast the event.
func Success() EventResult {
	return EventResult{}
}

// Fail instructs the relay to send OK(false, reason) to the client with the given reason.
func Fail(reason string) EventResult {
	return EventResult{failed: true, noBroadcast: true, reason: reason}
}

// NoBroadcast instructs the relay to skip broadcasting the event.
func (r EventResult) NoBroadcast() EventResult {
	r.noBroadcast = true
	return r
}

// Broadcast instructs the relay to broadcast the event, even if processing failed.
func (r EventResult) Broadcast() EventResult {
	r.noBroadcast = false
	return r
}

// WithReply adds a message to the OK response sent to the client.
func (r EventResult) WithReply(msg string) EventResult {
	r.reason = msg
	return r
}

func DefaultOnHooks() OnHooks {
	return OnHooks{
		Connect:    func(Client) {},
		Disconnect: func(Client) {},
		Auth:       func(c Client) {},
		Event:      logEvent,
		Req:        logFilters,
	}
}

func logEvent(c Client, e *nostr.Event) EventResult {
	slog.Info("received event", "id", e.ID, "ip", c.IP().Group())
	return Success()
}

func logFilters(ctx context.Context, c Client, id string, f nostr.Filters) ([]nostr.Event, error) {
	slog.Info("received req", "id", id, "filters", len(f), "ip", c.IP().Group())
	return nil, nil
}

// WhenHooks defines functions invoked when special, non-standard, or exceptional
// conditions occur during the relay’s operation.
//
// These hooks are useful for detecting and responding to client misbehavior or
// non-critical performance issues that fall outside the normal operational flow.
type WhenHooks struct {
	// GreedyClient is invoked when a client’s response buffer becomes full,
	// typically because it sends new REQs before reading responses from earlier ones.
	// This hook is commonly used for logging misbehavior or disconnecting the client.
	GreedyClient func(Client)
}

func DefaultWhenHooks() WhenHooks {
	return WhenHooks{
		GreedyClient: DisconnectOnDrops(50),
	}
}

// InvalidID returns an error if the event's ID is invalid
func InvalidID(c Client, e *nostr.Event) error {
	if !e.CheckID() {
		return ErrInvalidEventID
	}
	return nil
}

// InvalidSignature returns an error if the event's signature is invalid.
func InvalidSignature(c Client, e *nostr.Event) error {
	match, err := e.CheckSignature()
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidEventSig, err.Error())
	}

	if !match {
		return ErrInvalidEventSig
	}
	return nil
}

// RegistrationFailWithin returns a Reject.Connection function that errs
// if a client registration has failed within the given duration.
func RegistrationFailWithin(d time.Duration) func(Stats, *http.Request) error {
	return func(s Stats, r *http.Request) error {
		if time.Since(s.LastRegistrationFail()) < d {
			return ErrOverloaded
		}
		return nil
	}
}

// DisconnectOnDrops returns a When.GreedyClient function that disconnects the client
// if it dropped more than the maximum responses.
func DisconnectOnDrops(maxDropped int) func(c Client) {
	return func(c Client) {
		if c.DroppedResponses() > maxDropped {
			c.Disconnect()
		}
	}
}

// Slice is an internal type used to simplify registration of hooks.
type slice[T any] []T

// Append adds hooks to the end of the slice, in the provided order.
func (s *slice[T]) Append(hooks ...T) {
	*s = append(*s, hooks...)
}

// Prepend adds hooks to the start of the slice, in the provided order.
func (s *slice[T]) Prepend(hooks ...T) {
	*s = append(hooks, *s...)
}

// Clear resets the slice, removing all registered hooks.
func (s *slice[T]) Clear() {
	*s = nil
}

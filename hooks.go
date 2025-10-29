package rely

import (
	"context"
	"fmt"
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
	Connection []func(Stats, *http.Request) error

	// Event is invoked before processing an EVENT message.
	// Returning a non-nil error rejects the event.
	Event []func(Client, *nostr.Event) error

	// Req is invoked before processing a REQ message.
	// Returning a non-nil error rejects the request.
	Req []func(Client, nostr.Filters) error

	// Count is invoked before processing a NIP-45 COUNT request.
	// Returning a non-nil error rejects the request.
	Count []func(Client, nostr.Filters) error
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
	Connect func(Client)

	// Disconnect runs immediately after a client has been unregistered and disconnected.
	// It is guaranteed to run after the Connect hook of the same client.
	// This callback must be very fast to avoid blocking the hot path.
	// For longer operations, use goroutines.
	//
	// Example:
	//   relay.On.Disconnect = func(c Client) {
	//       go longOperation(c)
	//   }
	Disconnect func(Client)

	// Auth is called immediately after a client successfully authenticates.
	// It can be used to load resources tied to the client’s public key or adjust rate limits.
	Auth func(Client)

	// Event defines how the relay processes an EVENT, for example by storing it in a database.
	Event func(Client, *nostr.Event) error

	// Req defines how the relay processes a REQ containing one or more filters,
	// for example by querying the database for matching events.
	// The provided context is canceled if the client sends the corresponding CLOSE message.
	Req func(context.Context, Client, nostr.Filters) ([]nostr.Event, error)

	// Count defines how the relay processes NIP-45 COUNT requests.
	// This hook is optional (= nil). If unset, COUNT requests are rejected with [ErrUnsupportedNIP45].
	Count func(Client, nostr.Filters) (count int64, approx bool, err error)
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
		GreedyClient: DisconnectOnDrops(200),
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
		return fmt.Errorf("%w: %s", ErrInvalidEventSignature, err.Error())
	}

	if !match {
		return ErrInvalidEventSignature
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

// DisconnectOnDrops returns a When.GreedyClient function that sends a notice and
// disconnects the client if it dropped more than the maximum responses.
func DisconnectOnDrops(maxDropped int) func(c Client) {
	return func(c Client) {
		if c.DroppedResponses() > maxDropped {
			c.SendNotice("too many dropped responses, disconnecting")
			c.Disconnect()
		}
	}
}

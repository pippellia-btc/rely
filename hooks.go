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

// RejectHooks define functions that can preemptively reject certain actions
// before they are processed by the relay. Each function is evaluated in order,
// and if any function returns an error, the corresponding input (connection, event,
// request, or count) is rejected immediately.
//
// These hooks allow users to enforce policies, validations, or rate limits
// before the relay performs any further processing.
type RejectHooks struct {
	Connection []func(Stats, *http.Request) error
	Event      []func(Client, *nostr.Event) error
	Req        []func(Client, nostr.Filters) error
	Count      []func(Client, nostr.Filters) error
}

func DefaultRejectHooks() RejectHooks {
	return RejectHooks{
		Connection: []func(Stats, *http.Request) error{RegistrationFailWithin(3 * time.Second)},
		Event:      []func(Client, *nostr.Event) error{InvalidID, InvalidSignature},
	}
}

// OnHooks define functions that are called after certain events occur in the relay.
// They allow users to hook into and customize how the relay reacts to client actions,
// events, requests, and counts. These functions are invoked after the input has
// passed the corresponding RejectHooks (if any).
//
// OnHooks enable users to implement custom processing, logging, persistence,
// authorization, or other side effects in response to relay activity.
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
	Auth       func(Client)
	Event      func(Client, *nostr.Event) error
	Req        func(context.Context, Client, nostr.Filters) ([]nostr.Event, error)
	Count      func(context.Context, Client, nostr.Filters) (count int64, approx bool, err error)
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

// WhenHooks defines functions that are called when a special, non-standard,
// or exceptional condition occurs during the relay's operation.
// These hooks are useful for observing and reacting to client misbehavior or
// non-critical performance issues that fall outside the regular operational flow.
type WhenHooks struct {
	// GreedyClient is called when the client's response buffer is full,
	// which happens if the client sends new REQs before reading all responses from previous ones.
	// This hook is typically used for logging client misbehavior and/or disconnecting.
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

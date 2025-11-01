package rely

import (
	"context"
	"slices"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Subscription represent the nostr subscription created by a [Client] with a REQ.
// All methods are safe for concurrent use.
type Subscription interface {
	// UID is the unique subscription identifier that combines the [Client.UID]
	// with the user-provided subscription ID <Client.UID>:<subscription.ID>
	UID() string

	// ID is a unique identifier within the scope of its client.
	ID() string

	// Filters returns the filters of the subscription.
	Filters() nostr.Filters

	// Matches returns whether any of the subscription's filters match the provided event.
	Matches(*nostr.Event) bool

	// CreatedAt returns the time when the subscription was created.
	CreatedAt() time.Time

	// Age returns how long ago the subscription was created.
	// Short for time.Since(subscription.CreatedAt())
	Age() time.Duration

	// Close the subscription, and send the client a CLOSED message with the provided reason
	Close(reason string)
}

type subscription struct {
	uid     string
	id      string
	filters nostr.Filters

	createdAt time.Time
	cancel    context.CancelFunc // calling it cancels the context of the associated REQ
	client    *client
}

func (s subscription) UID() string                 { return s.uid }
func (s subscription) ID() string                  { return s.id }
func (s subscription) Filters() nostr.Filters      { return slices.Clone(s.filters) }
func (s subscription) CreatedAt() time.Time        { return s.createdAt }
func (s subscription) Age() time.Duration          { return time.Since(s.createdAt) }
func (s subscription) Matches(e *nostr.Event) bool { return s.filters.Match(e) }
func (s subscription) Close(reason string)         { s.client.CloseSubWithReason(s.id, reason) }

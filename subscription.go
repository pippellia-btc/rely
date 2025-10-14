package rely

import (
	"context"

	"github.com/nbd-wtf/go-nostr"
)

type Subscription struct {
	ID      string
	typ     string // either "REQ" or "COUNT"
	Filters nostr.Filters

	cancel context.CancelFunc // calling it cancels the context of the associated REQ/COUNT
	client *client
}

// UID is the unique subscription identifier that combines relay, client, and user-provided
// subscription ID <relay.uid>:<clientNumber>:<subscription.ID>
func (s Subscription) UID() string { return join(s.client.UID(), s.ID) }

func (s Subscription) Matches(e *nostr.Event) bool { return s.Filters.Match(e) }

// sID is the internal representation of a unique subscription identifier, which
// is identical to [Subscription.UID]. Used only to make the code more readable
type sID string

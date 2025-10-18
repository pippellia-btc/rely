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

// UID is the unique subscription identifier that combines the [Client.UID]
// with the user-provided subscription ID <Client.UID>:<subscription.ID>
func (s Subscription) UID() string { return join(s.client.uid, s.ID) }

func (s Subscription) Matches(e *nostr.Event) bool { return s.Filters.Match(e) }

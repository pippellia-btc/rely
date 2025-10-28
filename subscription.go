package rely

import (
	"context"

	"github.com/nbd-wtf/go-nostr"
)

type subscription struct {
	uid     string
	id      string
	filters nostr.Filters

	cancel context.CancelFunc // calling it cancels the context of the associated REQ/COUNT
	client *client
}

// UID is the unique subscription identifier that combines the [Client.UID]
// with the user-provided subscription ID <Client.UID>:<subscription.ID>
func (s subscription) UID() string { return s.uid }

func (s subscription) Matches(e *nostr.Event) bool { return s.filters.Match(e) }

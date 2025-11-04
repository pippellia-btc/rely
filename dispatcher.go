package rely

import (
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/pippellia-btc/smallset"
)

// sID is the internal representation of a unique subscription identifier, which
// is identical to [subscription.uid]. Used only to make the code more readable
type sID string

// Join multiple strings into one, separated by ":". Useful to produce canonical UIDs.
func join(strs ...string) string { return strings.Join(strs, ":") }

// Dispatcher is responsible for indexing subscriptions, essential for efficient broadcasting of events.
//
// The dispatcher is *not* the authority when it comes to the subscriptions state.
// Each [client] is the ultimate authority of its own subscriptions.
// The dispatcher manages a global and eventually-consistent snapshot of all clients' subscriptions.
type dispatcher struct {
	subscriptions map[sID]subscription
	byID          map[nostr.ID]*smallset.Ordered[sID]
	byAuthor      map[nostr.PubKey]*smallset.Ordered[sID]
	byTag         map[string]*smallset.Ordered[sID]
	byKind        map[nostr.Kind]*smallset.Ordered[sID]
	byTime        *timeIndex

	updates   chan update
	broadcast chan *nostr.Event

	// pointer to parent relay, which must only be used for:
	//	- reading settings/hooks
	//	- sending to channels
	// 	- incrementing atomic counters
	relay *Relay
}

type operation int

const (
	index operation = iota
	unindex
)

// update represent either an indexing or unindexing of a subscription.
// Both operations must be placed in the same channel to serialize them.
// For example, imagine a subscription being replaced with another (same ID, different filters).
// The dispatcher must unindex the old, and index the new, in this order.
type update struct {
	operation operation // either [index] or [unindex]
	sub       subscription
}

func newDispatcher(relay *Relay) *dispatcher {
	return &dispatcher{
		subscriptions: make(map[sID]subscription, 3000),
		byID:          make(map[nostr.ID]*smallset.Ordered[sID], 3000),
		byAuthor:      make(map[nostr.PubKey]*smallset.Ordered[sID], 3000),
		byTag:         make(map[string]*smallset.Ordered[sID], 3000),
		byKind:        make(map[nostr.Kind]*smallset.Ordered[sID], 3000),
		byTime:        newTimeIndex(600),
		updates:       make(chan update, 256),
		broadcast:     make(chan *nostr.Event, 256),
		relay:         relay,
	}
}

// Run syncronizes all access to the subscriptions map and the inverted indexes.
func (d *dispatcher) Run() {
	defer d.relay.wg.Done()

	for {
		select {
		case <-d.relay.done:
			d.Clear()
			return

		case update := <-d.updates:
			switch update.operation {
			case index:
				d.Index(update.sub)
			case unindex:
				d.Unindex(update.sub)
			}

		case event := <-d.broadcast:
			err := d.Broadcast(event)
			if err != nil {
				d.relay.log.Error("failed to broadcast event", "eventID", event.ID, "error", err)
			}
		}
	}
}

// Broadcast the provided event to all matching subscriptions.
// As a performance optimisation, we marshal the event only once, and not
// once per matching subscription.
func (d *dispatcher) Broadcast(e *nostr.Event) error {
	candidates := d.Candidates(e)
	if len(candidates) == 0 {
		return nil
	}

	var err error
	var response rawEventResponse

	response.Event, err = e.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal event %w", err)
	}

	for _, id := range candidates {
		sub := d.subscriptions[id]
		if sub.Matches(e) {
			response.ID = sub.id
			sub.client.send(response)
		}
	}
	return nil
}

// Candidates returns a slice of candidate subscription ids that are likely to match the provided event.
func (d *dispatcher) Candidates(e *nostr.Event) []sID {
	candidates := make([]*smallset.Ordered[sID], 0, 10)
	if subs, ok := d.byID[e.ID]; ok {
		candidates = append(candidates, subs)
	}
	if subs, ok := d.byAuthor[e.PubKey]; ok {
		candidates = append(candidates, subs)
	}
	if subs, ok := d.byKind[e.Kind]; ok {
		candidates = append(candidates, subs)
	}
	if subs, ok := d.byTime.Candidates(e.CreatedAt); ok {
		candidates = append(candidates, subs)
	}

	for _, t := range e.Tags {
		if len(t) >= 2 && isLetter(t[0]) {
			kv := join(t[0], t[1])
			if subs, ok := d.byTag[kv]; ok {
				candidates = append(candidates, subs)
			}
		}
	}
	return smallset.Merge(candidates...).Items()
}

// Clear explicitly sets all large index maps to nil to break references,
// so the garbage collector can immediately reclaim the memory.
func (d *dispatcher) Clear() {
	d.subscriptions = nil
	d.byID = nil
	d.byAuthor = nil
	d.byKind = nil
	d.byTag = nil
	d.byTime = nil
	d.relay.stats.subscriptions.Store(0)
	d.relay.stats.filters.Store(0)
}

// Add the subscription to the dispatcher indexes, one filter at the time.
// Filter indexing is assumed to be safe, given that all filters have passed the Reject.Req.
func (d *dispatcher) Index(s subscription) {
	sid := sID(s.uid)
	d.subscriptions[sid] = s
	d.relay.stats.subscriptions.Add(1)
	d.relay.stats.filters.Add(int64(len(s.filters)))

	for _, f := range s.filters {
		switch {
		case len(f.IDs) > 0:
			for _, id := range f.IDs {
				subs, ok := d.byID[id]
				if !ok {
					d.byID[id] = smallset.NewFrom(sid)
				} else {
					subs.Add(sid)
				}
			}

		case len(f.Authors) > 0:
			for _, pk := range f.Authors {
				subs, ok := d.byAuthor[pk]
				if !ok {
					d.byAuthor[pk] = smallset.NewFrom(sid)
				} else {
					subs.Add(sid)
				}
			}

		case len(f.Tags) > 0:
			for key, vals := range f.Tags {
				if !isLetter(key) {
					continue
				}

				for _, v := range vals {
					kv := join(key, v)
					subs, ok := d.byTag[kv]
					if !ok {
						d.byTag[kv] = smallset.NewFrom(sid)
					} else {
						subs.Add(sid)
					}
				}
			}

		case len(f.Kinds) > 0:
			for _, k := range f.Kinds {
				subs, ok := d.byKind[k]
				if !ok {
					d.byKind[k] = smallset.NewFrom(sid)
				} else {
					subs.Add(sid)
				}
			}

		default:
			d.byTime.Add(f, sid)
		}
	}
}

// Remove the subscription from the dispatcher indexes, one filter at the time.
// After removal, if a set is empty, the corresponding key it's removed from the map
// to allow the map's bucket to be reused.
func (d *dispatcher) Unindex(s subscription) {
	sid := sID(s.uid)
	delete(d.subscriptions, sid)
	d.relay.stats.subscriptions.Add(-1)
	d.relay.stats.filters.Add(-int64(len(s.filters)))

	for _, f := range s.filters {
		switch {
		case len(f.IDs) > 0:
			for _, id := range f.IDs {
				subs, ok := d.byID[id]
				if !ok {
					continue
				}

				subs.Remove(sid)
				if subs.IsEmpty() {
					delete(d.byID, id)
				}
			}

		case len(f.Authors) > 0:
			for _, pk := range f.Authors {
				subs, ok := d.byAuthor[pk]
				if !ok {
					continue
				}

				subs.Remove(sid)
				if subs.IsEmpty() {
					delete(d.byAuthor, pk)
				}
			}

		case len(f.Tags) > 0:
			for key, vals := range f.Tags {
				if !isLetter(key) {
					continue
				}

				for _, v := range vals {
					kv := join(key, v)
					subs, ok := d.byTag[kv]
					if !ok {
						continue
					}

					subs.Remove(sid)
					if subs.IsEmpty() {
						delete(d.byTag, kv)
					}
				}
			}

		case len(f.Kinds) > 0:
			for _, k := range f.Kinds {
				subs, ok := d.byKind[k]
				if !ok {
					continue
				}

				subs.Remove(sid)
				if subs.IsEmpty() {
					delete(d.byKind, k)
				}
			}

		default:
			d.byTime.Remove(f, sid)
		}
	}
}

// isLetter returns whether the string is a single letter (a-z or A-Z).
func isLetter(s string) bool {
	if len(s) != 1 {
		return false
	}
	c := s[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

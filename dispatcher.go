package rely

import (
	"strings"
	"sync/atomic"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/smallset"
)

// sID is the internal representation of a unique subscription identifier, which
// is identical to [subscription.uid]. Used only to make the code more readable
type sID string

// cID is the internal representation of a unique client identifier, which
// is identical to [client.uid]. Used only to make the code more readable
type cID string

// Join multiple strings into one, separated by ":". Useful to produce canonical UIDs.
func join(strs ...string) string { return strings.Join(strs, ":") }

// Dispatcher is responsible for managing the subscriptions state and the indexes,
// essential for efficient broadcasting of events.
type dispatcher struct {
	subscriptions map[sID]subscription
	indexes       *dispatcherIndexes
	stats         dispatcherStats

	open      chan subscription
	close     chan sID
	closeAll  chan cID
	viewSubs  chan subRequest
	broadcast chan *nostr.Event

	// pointer to parent relay, which must only be used to read settings/hooks or send to channels
	relay *Relay
}

// DispatcherIndexes allows for fast search of candidate subscription IDs for a specific event.
// It maintains indexes for all filter fields except search.
type dispatcherIndexes struct {
	byClient map[cID]*smallset.Ordered[sID]
	byID     map[string]*smallset.Ordered[sID]
	byAuthor map[string]*smallset.Ordered[sID]
	byTag    map[string]*smallset.Ordered[sID]
	byKind   map[int]*smallset.Ordered[sID]
	byTime   *timeIndex
}

type dispatcherStats struct {
	clients       atomic.Int64
	subscriptions atomic.Int64
	filters       atomic.Int64
}

func newDispatcher(relay *Relay) *dispatcher {
	return &dispatcher{
		subscriptions: make(map[sID]subscription, 1000),
		indexes:       newDispatcherIndexes(),
		open:          make(chan subscription, 256),
		close:         make(chan sID, 256),
		closeAll:      make(chan cID, 256),
		broadcast:     make(chan *nostr.Event, 256),
		viewSubs:      make(chan subRequest, 256),
		relay:         relay,
	}
}

func newDispatcherIndexes() *dispatcherIndexes {
	return &dispatcherIndexes{
		byClient: make(map[cID]*smallset.Ordered[sID], 1000),
		byID:     make(map[string]*smallset.Ordered[sID], 3000),
		byAuthor: make(map[string]*smallset.Ordered[sID], 3000),
		byTag:    make(map[string]*smallset.Ordered[sID], 3000),
		byKind:   make(map[int]*smallset.Ordered[sID], 3000),
		byTime:   newTimeIndex(600),
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

		case sub := <-d.open:
			d.Open(sub)

		case subID := <-d.close:
			d.Close(subID)

		case cID := <-d.closeAll:
			subs, ok := d.indexes.byClient[cID]
			if !ok {
				break
			}

			for _, id := range subs.Items() {
				d.Close(id)
			}
			delete(d.indexes.byClient, cID)

		case request := <-d.viewSubs:
			subs := d.SubscriptionsOf(request.client)
			request.reply <- subs

		case event := <-d.broadcast:
			d.Broadcast(event)
		}
	}
}

// Open or overwrite a subscription with the dispatcher.
func (d *dispatcher) Open(s subscription) {
	if s.client.isUnregistering.Load() {
		return
	}

	id := sID(s.uid)
	old, exists := d.subscriptions[id]
	if exists {
		old.cancel()
		d.indexes.remove(old)
		d.indexes.add(s)
		d.subscriptions[id] = s

		delta := int64(len(s.filters) - len(old.filters))
		d.stats.filters.Add(delta)
		return
	}

	d.indexes.add(s)
	d.subscriptions[id] = s

	d.stats.subscriptions.Add(1)
	d.stats.filters.Add(int64(len(s.filters)))
}

// Close the subscription with the provided ID, if present.
func (d *dispatcher) Close(id sID) {
	sub, exists := d.subscriptions[id]
	if exists {
		sub.cancel()
		d.indexes.remove(sub)
		delete(d.subscriptions, id)

		d.stats.subscriptions.Add(-1)
		d.stats.filters.Add(-int64(len(sub.filters)))
	}
}

// SubscriptionsOf returns the currently active subscriptions of the client with the provided ID.
func (d *dispatcher) SubscriptionsOf(c *client) []Subscription {
	IDs := d.indexes.byClient[cID(c.uid)]
	subs := make([]Subscription, IDs.Size())
	for i, id := range IDs.Ascend() {
		subs[i] = d.subscriptions[id]
	}
	return subs
}

func (d *dispatcher) Broadcast(e *nostr.Event) {
	for _, id := range d.indexes.candidates(e) {
		sub := d.subscriptions[id]
		if sub.Matches(e) {
			sub.client.send(eventResponse{ID: sub.id, Event: e})
		}
	}
}

// Clear explicitly sets all large index maps to nil to break references,
// so the garbage collector can immediately reclaim the memory.
func (d *dispatcher) Clear() {
	d.subscriptions = nil
	d.indexes.byClient = nil
	d.indexes.byID = nil
	d.indexes.byAuthor = nil
	d.indexes.byKind = nil
	d.indexes.byTag = nil
	d.indexes.byTime = nil
}

// Add the subscription to the dispatcher indexes, one filter at the time.
// Filter indexing is assumed to be safe, given that all filters have passed the Reject.Req.
//
// The subs in the byClient index is not checked before insertion, because it must be
// managed at a higher level in the [dispatcher.register] and [dispatcher.unregister] to avoid multiple
// creation/deletion during the life-time of the client.
func (i dispatcherIndexes) add(s subscription) {
	sid := sID(s.uid)
	cid := cID(s.client.uid)

	subs, ok := i.byClient[cid]
	if !ok {
		i.byClient[cid] = smallset.NewFrom(sid)
	} else {
		subs.Add(sid)
	}

	for _, f := range s.filters {
		switch {
		case len(f.IDs) > 0:
			for _, id := range f.IDs {
				subs, ok := i.byID[id]
				if !ok {
					i.byID[id] = smallset.NewFrom(sid)
				} else {
					subs.Add(sid)
				}
			}

		case len(f.Authors) > 0:
			for _, pk := range f.Authors {
				subs, ok := i.byAuthor[pk]
				if !ok {
					i.byAuthor[pk] = smallset.NewFrom(sid)
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
					subs, ok := i.byTag[kv]
					if !ok {
						i.byTag[kv] = smallset.NewFrom(sid)
					} else {
						subs.Add(sid)
					}
				}
			}

		case len(f.Kinds) > 0:
			for _, k := range f.Kinds {
				subs, ok := i.byKind[k]
				if !ok {
					i.byKind[k] = smallset.NewFrom(sid)
				} else {
					subs.Add(sid)
				}
			}

		default:
			i.byTime.Add(f, sid)
		}
	}
}

// Remove the subscription from the dispatcher indexes, one filter at the time.
// After removal, if a set is empty, the corresponding key it's removed from the map
// to allow the map's bucket to be reused.
//
// This is not true for subs in the byClient index, which is created and deleted
// in the [dispatcher.register] and [dispatcher.unregister] to avoid multiple
// creation/deletion during the life-time of the client.
func (i dispatcherIndexes) remove(s subscription) {
	sid := sID(s.uid)
	cid := cID(s.client.uid)
	i.byClient[cid].Remove(sid)

	for _, f := range s.filters {
		switch {
		case len(f.IDs) > 0:
			for _, id := range f.IDs {
				subs, ok := i.byID[id]
				if !ok {
					continue
				}

				subs.Remove(sid)
				if subs.IsEmpty() {
					delete(i.byID, id)
				}
			}

		case len(f.Authors) > 0:
			for _, pk := range f.Authors {
				subs, ok := i.byAuthor[pk]
				if !ok {
					continue
				}

				subs.Remove(sid)
				if subs.IsEmpty() {
					delete(i.byAuthor, pk)
				}
			}

		case len(f.Tags) > 0:
			for key, vals := range f.Tags {
				if !isLetter(key) {
					continue
				}

				for _, v := range vals {
					kv := join(key, v)
					subs, ok := i.byTag[kv]
					if !ok {
						continue
					}

					subs.Remove(sid)
					if subs.IsEmpty() {
						delete(i.byTag, kv)
					}
				}
			}

		case len(f.Kinds) > 0:
			for _, k := range f.Kinds {
				subs, ok := i.byKind[k]
				if !ok {
					continue
				}

				subs.Remove(sid)
				if subs.IsEmpty() {
					delete(i.byKind, k)
				}
			}

		default:
			i.byTime.Remove(f, sid)
		}
	}
}

// Candidates returns a slice of candidate subscription ids that are likely to match the provided event.
func (i dispatcherIndexes) candidates(e *nostr.Event) []sID {
	candidates := make([]*smallset.Ordered[sID], 0, 10)
	if subs, ok := i.byID[e.ID]; ok {
		candidates = append(candidates, subs)
	}
	if subs, ok := i.byAuthor[e.PubKey]; ok {
		candidates = append(candidates, subs)
	}
	if subs, ok := i.byKind[e.Kind]; ok {
		candidates = append(candidates, subs)
	}
	if subs, ok := i.byTime.Candidates(e.CreatedAt); ok {
		candidates = append(candidates, subs)
	}

	for _, t := range e.Tags {
		if len(t) >= 2 && isLetter(t[0]) {
			kv := join(t[0], t[1])
			if subs, ok := i.byTag[kv]; ok {
				candidates = append(candidates, subs)
			}
		}
	}
	return smallset.Merge(candidates...).Items()
}

// isLetter returns whether the string is a single letter (a-z or A-Z).
func isLetter(s string) bool {
	if len(s) != 1 {
		return false
	}
	c := s[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

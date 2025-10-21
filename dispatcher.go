package rely

import (
	"slices"
	"strings"
	"sync/atomic"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/slicex"
)

// sID is the internal representation of a unique subscription identifier, which
// is identical to [Subscription.UID]. Used only to make the code more readable
type sID string

// Join multiple strings into one, separated by ":". Useful to produce canonical UIDs.
func join(strs ...string) string { return strings.Join(strs, ":") }

// Dispatcher is responsible for managing the clients and subscriptions state,
// essential for efficient broadcasting of events and for a graceful shutdown.
// Its methods are not safe for concurrent use, and must be syncronized externally.
type dispatcher struct {
	clients       map[*client]struct{}
	subscriptions map[sID]Subscription

	indexes *dispatcherIndexes
	stats   dispatcherStats
}

// DispatcherIndexes allows for fast search of candidate subscription IDs for a specific event.
// It maintains multiple indexes for all filter fields except search.
type dispatcherIndexes struct {
	byClient map[string][]sID
	byID     map[string][]sID
	byAuthor map[string][]sID
	byTag    map[string][]sID
	byKind   map[int][]sID
}

type dispatcherStats struct {
	clients       atomic.Int64
	subscriptions atomic.Int64
	filters       atomic.Int64
}

func newDispatcher() *dispatcher {
	return &dispatcher{
		clients:       make(map[*client]struct{}, 1000),
		subscriptions: make(map[sID]Subscription, 1000),
		indexes:       newDispatcherIndexes(),
	}
}

func newDispatcherIndexes() *dispatcherIndexes {
	return &dispatcherIndexes{
		byClient: make(map[string][]sID, 1000),
		byID:     make(map[string][]sID, 3000),
		byAuthor: make(map[string][]sID, 3000),
		byTag:    make(map[string][]sID, 3000),
		byKind:   make(map[int][]sID, 3000),
	}
}

// Register a client with the dispatcher.
func (d *dispatcher) register(c *client) {
	d.clients[c] = struct{}{}
	d.indexes.byClient[c.uid] = make([]sID, 0, 10)
	d.stats.clients.Add(1)
}

// Unregister a client, by closing all of its subscriptions.
func (d *dispatcher) unregister(c *client) {
	delete(d.clients, c)
	d.stats.clients.Add(-1)

	sIDs := d.indexes.byClient[c.uid]
	for _, id := range sIDs {
		d.close(id)
	}
}

// Open or overwrite a subscription with the dispatcher.
func (d *dispatcher) open(s Subscription) {
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

		delta := int64(len(s.Filters) - len(old.Filters))
		d.stats.filters.Add(delta)
		return
	}

	d.indexes.add(s)
	d.subscriptions[id] = s

	d.stats.subscriptions.Add(1)
	d.stats.filters.Add(int64(len(s.Filters)))
}

func (d *dispatcher) close(id sID) {
	sub, exists := d.subscriptions[id]
	if exists {
		sub.cancel()
		d.indexes.remove(sub)
		delete(d.subscriptions, id)

		d.stats.subscriptions.Add(-1)
		d.stats.filters.Add(-int64(len(sub.Filters)))
	}
}

func (d *dispatcher) broadcast(e *nostr.Event) {
	candidates := d.indexes.candidates(e)

	for _, id := range candidates {
		sub := d.subscriptions[id]
		if sub.Matches(e) {
			sub.client.send(eventResponse{ID: sub.ID, Event: e})
		}
	}
}

// Add the subscription to the dispatcher indexes, one filter at the time.
// Filter indexing is assumed to be safe, given that all filters have passed the Reject.Req
func (i dispatcherIndexes) add(s Subscription) {
	sid := sID(s.uid)
	cid := s.client.uid

	current := i.byClient[cid]
	i.byClient[cid] = append(current, sid)

	// filter indexing must be idempotent because filters of a subscription
	// could partially overlap.
	//
	// Example:
	// 	- f1: Authors=[xxx], Since=yyy
	//	- f2: Authors=[xxx], Kinds=[69]
	//
	// If we don't check first, the subscription id would be added twice to byAuthor[xxx].
	// Using a slice is the fastest for very small sets, which should be the norm
	// under a normal load.

	for _, f := range s.Filters {
		switch {
		case len(f.IDs) > 0:
			for _, id := range f.IDs {
				current := i.byID[id]
				if !slices.Contains(current, sid) {
					i.byID[id] = append(current, sid)
				}
			}

		case len(f.Authors) > 0:
			for _, pk := range f.Authors {
				current := i.byAuthor[pk]
				if !slices.Contains(current, sid) {
					i.byAuthor[pk] = append(current, sid)
				}
			}

		case len(f.Tags) > 0:
			for key, vals := range f.Tags {
				if !isLetter(key) {
					continue
				}

				for _, v := range vals {
					kv := join(key, v)
					current := i.byTag[kv]
					if !slices.Contains(current, sid) {
						i.byTag[kv] = append(current, sid)
					}
				}
			}

		case len(f.Kinds) > 0:
			for _, k := range f.Kinds {
				current := i.byKind[k]
				if !slices.Contains(current, sid) {
					i.byKind[k] = append(current, sid)
				}
			}

		case f.Since != nil || f.Until != nil:
			// unsure what to do
		}
	}
}

// Remove the subscription from the dispatcher indexes, one filter at the time.
func (i dispatcherIndexes) remove(s Subscription) {
	sid := sID(s.uid)
	cid := s.client.uid
	removeOrDelete(i.byClient, cid, sid)

	for _, f := range s.Filters {
		switch {
		case len(f.IDs) > 0:
			for _, id := range f.IDs {
				removeOrDelete(i.byID, id, sid)
			}

		case len(f.Authors) > 0:
			for _, pk := range f.Authors {
				removeOrDelete(i.byAuthor, pk, sid)
			}

		case len(f.Tags) > 0:
			for key, vals := range f.Tags {
				if !isLetter(key) {
					continue
				}

				for _, v := range vals {
					kv := join(key, v)
					removeOrDelete(i.byTag, kv, sid)
				}
			}

		case len(f.Kinds) > 0:
			for _, k := range f.Kinds {
				removeOrDelete(i.byKind, k, sid)
			}

		case f.Since != nil || f.Until != nil:
			// unsure what to do
		}
	}
}

// Candidates returns a slice of candidate subscription ids that are likely to match the provided event.
func (i dispatcherIndexes) candidates(e *nostr.Event) []sID {
	candidates := make([]sID, 0, 100)
	candidates = append(candidates, i.byID[e.ID]...)
	candidates = append(candidates, i.byAuthor[e.PubKey]...)
	candidates = append(candidates, i.byKind[e.Kind]...)

	for _, t := range e.Tags {
		if len(t) >= 2 && isLetter(t[0]) {
			kv := join(t[0], t[1])
			candidates = append(candidates, i.byTag[kv]...)
		}
	}
	return slicex.Unique(candidates)
}

// removeOrDelete removes a value from the slice m[key] if present.
// If the resulting slice is empty, the key is deleted from the map.
func removeOrDelete[K comparable, V comparable](m map[K][]V, key K, val V) {
	current := m[key]
	updated, found := remove(current, val)
	if !found {
		return
	}

	if len(updated) == 0 {
		delete(m, key)
		return
	}

	m[key] = updated
}

// remove searches for element `e` in slice `s` and removes it if present.
// It returns the updated slice and a boolean indicating whether removal occurred.
// It zeroes the slot of the removed element for GC safety.
func remove[E comparable](s []E, e E) (result []E, found bool) {
	pos := slices.Index(s, e)
	if pos < 0 {
		return s, false
	}

	if len(s) == 1 {
		// the last element is going to be removed
		return nil, true
	}

	var zero E
	last := len(s) - 1
	s[pos], s[last] = s[last], zero
	return s[:last], true
}

// isLetter returns whether the string is a single letter (a-z or A-Z).
func isLetter(s string) bool {
	if len(s) != 1 {
		return false
	}
	c := s[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

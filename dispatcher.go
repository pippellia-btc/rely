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

	byClient map[string][]sID
	byID     map[string][]sID
	byAuthor map[string][]sID
	byTag    map[string][]sID
	byKind   map[int][]sID
	others   []sID

	stats dispatcherStats
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
		byClient:      make(map[string][]sID, 1000),
		byID:          make(map[string][]sID, 3000),
		byAuthor:      make(map[string][]sID, 3000),
		byTag:         make(map[string][]sID, 3000),
		byKind:        make(map[int][]sID, 3000),
		others:        make([]sID, 0, 1000),
	}
}

func (d *dispatcher) register(c *client) {
	d.clients[c] = struct{}{}
	d.byClient[c.UID()] = make([]sID, 0, 10)
	d.stats.clients.Add(1)
}

func (d *dispatcher) unregister(c *client) {
	delete(d.clients, c)
	d.stats.clients.Add(-1)

	sIDs := d.byClient[c.UID()]
	for _, id := range sIDs {
		d.close(id)
	}
}

func (d *dispatcher) open(s Subscription) {
	if s.client.isUnregistering.Load() {
		return
	}

	id := sID(s.UID())
	old, exists := d.subscriptions[id]
	if exists {
		old.cancel()
		d.unindex(old)
		d.index(s)
		d.subscriptions[id] = s

		delta := int64(len(s.Filters) - len(old.Filters))
		d.stats.filters.Add(delta)
		return
	}

	d.index(s)
	d.subscriptions[id] = s

	d.stats.subscriptions.Add(1)
	d.stats.filters.Add(int64(len(s.Filters)))
}

func (d *dispatcher) close(id sID) {
	sub, exists := d.subscriptions[id]
	if exists {
		sub.cancel()
		d.unindex(sub)
		delete(d.subscriptions, id)

		d.stats.subscriptions.Add(-1)
		d.stats.filters.Add(-int64(len(sub.Filters)))
	}
}

func (d *dispatcher) broadcast(e *nostr.Event) {
	candidates := make([]sID, 0, 100)
	candidates = append(candidates, d.byID[e.ID]...)
	candidates = append(candidates, d.byAuthor[e.PubKey]...)

	for _, t := range e.Tags {
		if len(t) >= 2 && isLetter(t[0]) {
			kv := join(t[0], t[1])
			candidates = append(candidates, d.byTag[kv]...)
		}
	}

	candidates = append(candidates, d.byKind[e.Kind]...)
	candidates = append(candidates, d.others...)
	candidates = slicex.Unique(candidates)

	for _, sID := range candidates {
		sub := d.subscriptions[sID]
		if sub.Matches(e) {
			sub.client.send(eventResponse{ID: sub.ID, Event: e})
		}
	}
}

func (d *dispatcher) index(s Subscription) {
	sid := sID(s.UID())
	cid := s.client.UID()

	current := d.byClient[cid]
	d.byClient[cid] = append(current, sid)

	// filter indexing must be idempotent because filters of a subscription
	// could partially overlap.
	//
	// Example:
	// 	- f1: Authors=[xxx], Since=yyy
	//	- f2: Authors=[xxx], Kinds=[69]
	//
	// If we don't check first, the subscription id would be added twice to byAuthor[xxx].

	for _, f := range s.Filters {
		switch {
		case len(f.IDs) > 0:
			for _, id := range f.IDs {
				current := d.byID[id]
				if !slices.Contains(current, sid) {
					d.byID[id] = append(current, sid)
				}
			}

		case len(f.Authors) > 0:
			for _, pk := range f.Authors {
				current := d.byAuthor[pk]
				if !slices.Contains(current, sid) {
					d.byAuthor[pk] = append(current, sid)
				}
			}

		case len(f.Tags) > 0:
			for key, vals := range f.Tags {
				if !isLetter(key) {
					continue
				}

				for _, v := range vals {
					kv := join(key, v)
					current := d.byTag[kv]
					if !slices.Contains(current, sid) {
						d.byTag[kv] = append(current, sid)
					}
				}
			}

		case len(f.Kinds) > 0:
			for _, k := range f.Kinds {
				current := d.byKind[k]
				if !slices.Contains(current, sid) {
					d.byKind[k] = append(current, sid)
				}
			}

		default:
			if !slices.Contains(d.others, sid) {
				d.others = append(d.others, sid)
			}
		}
	}
}

func (d *dispatcher) unindex(s Subscription) {
	sid := sID(s.UID())
	cid := s.client.UID()
	removeOrDelete(d.byClient, cid, sid)

	for _, f := range s.Filters {
		switch {
		case len(f.IDs) > 0:
			for _, id := range f.IDs {
				removeOrDelete(d.byID, id, sid)
			}

		case len(f.Authors) > 0:
			for _, pk := range f.Authors {
				removeOrDelete(d.byAuthor, pk, sid)
			}

		case len(f.Tags) > 0:
			for key, vals := range f.Tags {
				if !isLetter(key) {
					continue
				}

				for _, v := range vals {
					kv := join(key, v)
					removeOrDelete(d.byTag, kv, sid)
				}
			}

		case len(f.Kinds) > 0:
			for _, k := range f.Kinds {
				removeOrDelete(d.byKind, k, sid)
			}

		default:
			updated, found := remove(d.others, sid)
			if found {
				d.others = updated
			}
		}
	}
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

package rely

import (
	"slices"
	"sync/atomic"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/slicex"
)

// Dispatcher is responsible for managing the clients and subscriptions state,
// essential for efficient broadcasting of events and for a graceful shutdown.
// Its methods are not safe for concurrent use, and must be syncronized externally.
type dispatcher struct {
	clients       map[*client][]sID
	subscriptions map[sID]Subscription

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
		clients:       make(map[*client][]sID, 1000),
		subscriptions: make(map[sID]Subscription, 1000),
		byKind:        make(map[int][]sID, 3000),
		byID:          make(map[string][]sID, 3000),
		byAuthor:      make(map[string][]sID, 3000),
		byTag:         make(map[string][]sID, 3000),
	}
}

func (d *dispatcher) index(sid sID, filters nostr.Filters) {
	for _, f := range filters {
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

func (d *dispatcher) unindex(sid sID, filters nostr.Filters) {
	for _, f := range filters {
		switch {
		case len(f.IDs) > 0:
			for _, id := range f.IDs {
				current := d.byID[id]
				if len(current) <= 1 {
					delete(d.byID, id)
					continue
				}

				pos, last := slices.Index(current, sid), len(current)-1
				current[pos], current[last] = current[last], ""
				d.byID[id] = current[:last]
			}

		case len(f.Authors) > 0:
			for _, pk := range f.Authors {
				current := d.byAuthor[pk]
				if len(current) <= 1 {
					delete(d.byAuthor, pk)
					continue
				}

				pos, last := slices.Index(current, sid), len(current)-1
				current[pos], current[last] = current[last], ""
				d.byAuthor[pk] = current[:last]
			}

		case len(f.Tags) > 0:
			for key, vals := range f.Tags {
				if !isLetter(key) {
					continue
				}

				for _, v := range vals {
					kv := join(key, v)
					current := d.byTag[kv]
					if len(current) <= 1 {
						delete(d.byTag, kv)
						continue
					}

					pos, last := slices.Index(current, sid), len(current)-1
					current[pos], current[last] = current[last], ""
					d.byTag[kv] = current[:last]
				}
			}

		case len(f.Kinds) > 0:
			for _, k := range f.Kinds {
				current := d.byKind[k]
				if len(current) <= 1 {
					delete(d.byKind, k)
					continue
				}

				pos, last := slices.Index(current, sid), len(current)-1
				current[pos], current[last] = current[last], ""
				d.byKind[k] = current[:last]
			}

		default:
			pos, last := slices.Index(d.others, sid), len(d.others)-1
			d.others[pos], d.others[last] = d.others[last], ""
			d.others = d.others[:last]
		}
	}
}

func (d *dispatcher) register(c *client) {
	d.clients[c] = make([]sID, 0, 5)
	d.stats.clients.Add(1)
}

func (d *dispatcher) unregister(c *client) {
	subs := d.clients[c]
	delete(d.clients, c)
	d.stats.clients.Add(-1)

	for _, uid := range subs {
		sub := d.subscriptions[uid]
		sub.cancel()
		delete(d.subscriptions, uid)

		d.stats.subscriptions.Add(-1)
		d.stats.filters.Add(-int64(len(sub.Filters)))
	}
}

func (d *dispatcher) open(new Subscription) {
	if new.client.isUnregistering.Load() {
		return
	}

	sID := sID(new.UID())
	old, exists := d.subscriptions[sID]
	if exists {
		old.cancel()
		d.subscriptions[sID] = new

		delta := int64(len(new.Filters) - len(old.Filters))
		d.stats.filters.Add(delta)
		return
	}

	d.subscriptions[sID] = new
	d.clients[new.client] = append(d.clients[new.client], sID)

	d.stats.subscriptions.Add(1)
	d.stats.filters.Add(int64(len(new.Filters)))
	// TODO: add to inverted indexes later
}

func (d *dispatcher) close(sID sID) {
	sub, exists := d.subscriptions[sID]
	if exists {
		sub.cancel()
		delete(d.subscriptions, sID)
		d.clients[sub.client] = slicex.Exclude(d.clients[sub.client], sID)

		d.stats.subscriptions.Add(-1)
		d.stats.filters.Add(-int64(len(sub.Filters)))
		// TODO: remove from inverted indexes later
	}
}

func (d *dispatcher) broadcast(e *nostr.Event) {
	candidates := make([]sID, 0, 100)
	candidates = append(candidates, d.byID[e.ID]...)
	candidates = append(candidates, d.byKind[e.Kind]...)
	candidates = append(candidates, d.byAuthor[e.PubKey]...)

	for _, t := range e.Tags {
		if len(t) >= 2 && isLetter(t[0]) {
			kv := join(t[0], t[1])
			candidates = append(candidates, d.byTag[kv]...)
		}
	}
	candidates = slicex.Unique(candidates)

	for _, sID := range candidates {
		sub := d.subscriptions[sID]
		if sub.typ == "REQ" && sub.Matches(e) {
			sub.client.send(eventResponse{ID: sub.ID, Event: e})
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

package rely

import (
	"sync/atomic"

	"github.com/nbd-wtf/go-nostr"
)

// Dispatcher is responsible for managing the clients and subscriptions state,
// essential for efficient broadcasting of events and for a graceful shutdown.
// Its methods are not safe for concurrent use, and must be syncronized externally.
type dispatcher struct {
	clients       map[*client]UIDs
	subscriptions map[UID]Subscription
	stats         dispatcherStats
}

type dispatcherStats struct {
	clients       atomic.Int64
	subscriptions atomic.Int64
	filters       atomic.Int64
}

func newDispatcher() *dispatcher {
	return &dispatcher{
		clients:       make(map[*client]UIDs, 1000),
		subscriptions: make(map[UID]Subscription, 1000),
	}
}

func (d *dispatcher) register(c *client) {
	d.clients[c] = make([]UID, 0, 5)
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

	uid := new.UID()
	old, exists := d.subscriptions[uid]
	if exists {
		old.cancel()
		d.subscriptions[uid] = new

		delta := int64(len(new.Filters) - len(old.Filters))
		d.stats.filters.Add(delta)
		return
	}

	d.subscriptions[uid] = new
	d.clients[new.client] = append(d.clients[new.client], uid)

	d.stats.subscriptions.Add(1)
	d.stats.filters.Add(int64(len(new.Filters)))
	// TODO: add to inverted indexes later
}

func (d *dispatcher) close(uid UID) {
	sub, exists := d.subscriptions[uid]
	if exists {
		sub.cancel()
		delete(d.subscriptions, uid)
		d.clients[sub.client] = remove(d.clients[sub.client], uid)

		d.stats.subscriptions.Add(-1)
		d.stats.filters.Add(-int64(len(sub.Filters)))
		// TODO: remove from inverted indexes later
	}
}

func (d *dispatcher) broadcast(e *nostr.Event) {
	// TODO: implement efficient candidate search with inverted indexes later
	for _, sub := range d.subscriptions {
		if sub.typ == "REQ" && sub.Matches(e) {
			sub.client.send(eventResponse{ID: sub.ID, Event: e})
		}
	}
}

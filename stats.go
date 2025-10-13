package rely

import (
	"strconv"
	"time"
)

// Stats exposes relay statistics, useful for monitoring health or rejecting
// connections during peaks of activity. All methods are safe for concurrent use.
type Stats interface {
	// Clients returns the number of active clients connected to the relay.
	Clients() int

	// Subscriptions returns the number of active subscriptions.
	Subscriptions() int

	// Filters returns the number of active filters of REQ subscriptions.
	Filters() int

	// TotalConnections returns the total number of connections since the relay startup.
	TotalConnections() int

	// QueueLoad returns the ratio of queued requests to total capacity,
	// represented as a float between 0 and 1.
	QueueLoad() float64

	// LastRegistrationFail returns the last time a client failed to be added
	// to the registration queue, which happens during periods of high load.
	LastRegistrationFail() time.Time
}

func (r *Relay) Clients() int                    { return int(r.dispatcher.stats.clients.Load()) }
func (r *Relay) Subscriptions() int              { return int(r.dispatcher.stats.subscriptions.Load()) }
func (r *Relay) Filters() int                    { return int(r.dispatcher.stats.filters.Load()) }
func (r *Relay) TotalConnections() int           { return int(r.nextID.Load()) }
func (r *Relay) QueueLoad() float64              { return float64(len(r.queue)) / float64(cap(r.queue)) }
func (r *Relay) LastRegistrationFail() time.Time { return time.Unix(r.lastRegistrationFail.Load(), 0) }

func (r *Relay) assignID() string { return strconv.FormatInt(r.nextID.Add(1), 10) }

package rely

import (
	"strconv"
	"sync/atomic"
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

	// QueueLoad returns the ratio of queued requests to total capacity,
	// represented as a float between 0 and 1.
	QueueLoad() float64

	// LastRegistrationFail returns the last time a client failed to be added
	// to the registration queue, which happens during periods of high load.
	LastRegistrationFail() time.Time

	// TotalConnections returns the total number of connections since the relay startup.
	TotalConnections() int
}

type stats struct {
	clients       atomic.Int64
	subscriptions atomic.Int64
	filters       atomic.Int64

	nextClient           atomic.Int64
	lastRegistrationFail atomic.Int64
}

func (r *Relay) Clients() int          { return int(r.stats.clients.Load()) }
func (r *Relay) Subscriptions() int    { return int(r.stats.subscriptions.Load()) }
func (r *Relay) Filters() int          { return int(r.stats.filters.Load()) }
func (r *Relay) TotalConnections() int { return int(r.stats.nextClient.Load()) }

func (r *Relay) QueueLoad() float64 {
	return float64(len(r.processor.queue)) / float64(cap(r.processor.queue))
}
func (r *Relay) LastRegistrationFail() time.Time {
	return time.Unix(r.stats.lastRegistrationFail.Load(), 0)
}

func (r *Relay) assignID() string { return strconv.FormatInt(r.stats.nextClient.Add(1), 10) }

package rely

import (
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/slicex"
	"github.com/pippellia-btc/smallset"
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
	byClient map[string]*smallset.Ordered[sID]
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

func newDispatcher() *dispatcher {
	return &dispatcher{
		clients:       make(map[*client]struct{}, 1000),
		subscriptions: make(map[sID]Subscription, 1000),
		indexes:       newDispatcherIndexes(),
	}
}

func newDispatcherIndexes() *dispatcherIndexes {
	return &dispatcherIndexes{
		byClient: make(map[string]*smallset.Ordered[sID], 1000),
		byID:     make(map[string]*smallset.Ordered[sID], 3000),
		byAuthor: make(map[string]*smallset.Ordered[sID], 3000),
		byTag:    make(map[string]*smallset.Ordered[sID], 3000),
		byKind:   make(map[int]*smallset.Ordered[sID], 3000),
		byTime:   newTimeIndex(600),
	}
}

// Register a client with the dispatcher.
func (d *dispatcher) register(c *client) {
	d.clients[c] = struct{}{}
	d.indexes.byClient[c.uid] = smallset.New[sID](20)
	d.stats.clients.Add(1)
}

// Unregister a client, by closing all of its subscriptions.
func (d *dispatcher) unregister(c *client) {
	delete(d.clients, c)
	d.stats.clients.Add(-1)

	subs := d.indexes.byClient[c.uid]
	for _, id := range subs.Items() {
		d.close(id)
	}
	delete(d.indexes.byClient, c.uid)
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
// Filter indexing is assumed to be safe, given that all filters have passed the Reject.Req.
//
// The subs in the byClient index is not checked before insertion, because it must be
// managed at a higher level in the [dispatcher.register] and [dispatcher.unregister] to avoid multiple
// creation/deletion during the life-time of the client.
func (i dispatcherIndexes) add(s Subscription) {
	sid := sID(s.uid)
	cid := s.client.uid
	i.byClient[cid].Add(sid)

	for _, f := range s.Filters {
		switch {
		case len(f.IDs) > 0:
			for _, id := range f.IDs {
				subs, ok := i.byID[id]
				if !ok {
					i.byID[id] = smallset.NewFrom(sid)
					continue
				}

				subs.Add(sid)
			}

		case len(f.Authors) > 0:
			for _, pk := range f.Authors {
				subs, ok := i.byAuthor[pk]
				if !ok {
					i.byAuthor[pk] = smallset.NewFrom(sid)
					continue
				}

				subs.Add(sid)
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
						continue
					}

					subs.Add(sid)
				}
			}

		case len(f.Kinds) > 0:
			for _, k := range f.Kinds {
				subs, ok := i.byKind[k]
				if !ok {
					i.byKind[k] = smallset.NewFrom(sid)
					continue
				}

				subs.Add(sid)
			}

		default:
			i.byTime.Add(f, sid)
		}
	}
}

// Remove the subscription from the dispatcher indexes, one filter at the time.
// After removal, a subs is empty, the corresponding key it's removed from the map
// in order to allow memory to be collected.
//
// This is not true for subs in the byClient index, which is created and deleted
// in the [dispatcher.register] and [dispatcher.unregister] to avoid multiple
// creation/deletion during the life-time of the client.
func (i dispatcherIndexes) remove(s Subscription) {
	sid := sID(s.uid)
	cid := s.client.uid
	i.byClient[cid].Remove(sid)

	for _, f := range s.Filters {
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
	candidates := make([]sID, 0, 100)
	if subs, ok := i.byID[e.ID]; ok {
		candidates = append(candidates, subs.Items()...)
	}
	if subs, ok := i.byAuthor[e.PubKey]; ok {
		candidates = append(candidates, subs.Items()...)
	}
	if subs, ok := i.byKind[e.Kind]; ok {
		candidates = append(candidates, subs.Items()...)
	}

	for _, t := range e.Tags {
		if len(t) >= 2 && isLetter(t[0]) {
			kv := join(t[0], t[1])
			if subs, ok := i.byTag[kv]; ok {
				candidates = append(candidates, subs.Items()...)
			}
		}
	}

	candidates = append(candidates, i.byTime.Candidates(e.CreatedAt)...)
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

// timeIndex organize [intervalFilter]s into two categories:
// - current: filters that intersect the (dynamic) time window [now - width, now + width]
// - future: filters that don't intersect the time window but will in the future.
//
// The working assumption is that the vast majority of broadcasted events will have a CreatedAt inside this window.
// Thanks to this assumption, we can reduce the number of candidates,
// which drammatically improves speed and memory usage.
type timeIndex struct {
	width       int64
	lastAdvance int64
	current     []intervalFilter
	future      []intervalFilter
}

// newTimeIndex returns a [timeIndex] using the (dynamic) time window [now - width, now + width].
func newTimeIndex(width int64) *timeIndex {
	return &timeIndex{
		width:   width,
		current: make([]intervalFilter, 0, 1024),
		future:  make([]intervalFilter, 0, 1024),
	}
}

const (
	beginning int64 = -1 << 63
	end       int64 = 1<<63 - 1
)

// intervalFilter represent the since and until fields of a [nostr.Filter],
// as well as the ID of the subscription of its parent REQ.
type intervalFilter struct {
	since, until int64
	sid          sID
}

func newIntervalFilter(f nostr.Filter, sid sID) intervalFilter {
	i := intervalFilter{
		since: beginning,
		until: end,
		sid:   sid,
	}

	if f.Since != nil {
		i.since = int64(*f.Since)
	}

	if f.Until != nil {
		i.until = int64(*f.Until)
	}
	return i
}

// size returns the number of interval filters in current and future.
func (t *timeIndex) size() int {
	return len(t.current) + len(t.future)
}

// Add a filter and associated subscription ID to the timeIndex.
func (t *timeIndex) Add(f nostr.Filter, sid sID) {
	interval := newIntervalFilter(f, sid)
	t.add(interval)
}

func (t *timeIndex) add(interval intervalFilter) {
	if interval.since > interval.until {
		// the interval bounds are invalid, so don't index
		return
	}

	t.advance()
	now := time.Now().Unix()
	min := now - t.width
	max := now + t.width

	if interval.until < min {
		// assumption: it's unlikely that events this old will be broadcasted,
		// so we simply don't index this filter.
		return
	}

	if interval.since > max {
		t.future = append(t.future, interval)
	} else {
		t.current = append(t.current, interval)
	}
}

// Remove the nostr filter with the associated subscription ID from the timeIndex.
func (t *timeIndex) Remove(f nostr.Filter, sid sID) {
	interval := newIntervalFilter(f, sid)
	t.remove(interval)
}

func (t *timeIndex) remove(interval intervalFilter) {
	if interval.since > interval.until {
		// the interval bounds are invalid, so it wasn't indexed
		return
	}

	t.advance()
	max := time.Now().Unix() + t.width

	if interval.since > max {
		updated, found := remove(t.future, interval)
		if found {
			t.future = updated
		}
		return
	}

	updated, found := remove(t.current, interval)
	if found {
		t.current = updated
	}
}

func (t *timeIndex) Candidates(createdAt nostr.Timestamp) []sID {
	now := time.Now().Unix()
	min := now - t.width
	max := now + t.width

	if int64(createdAt) < min || int64(createdAt) > max {
		// fast path that avoids returning candidates that will likely be discarted
		return nil
	}

	t.advance()
	return t.currentIDs()
}

func (t *timeIndex) advance() {
	now := time.Now().Unix()
	if now == t.lastAdvance {
		// only advance once per second, as this is the "resolution" of the unix time
		return
	}

	t.lastAdvance = now
	min := now - t.width
	max := now + t.width

	// remove all expired intervals from current
	i := 0
	for _, interval := range t.current {
		if interval.until >= min {
			t.current[i] = interval
			i++
		}
	}
	t.current = t.current[:i]

	// move intervals from future to current.
	// assumption: the filter still intersects the time window
	i = 0
	for _, interval := range t.future {
		if interval.since <= max {
			t.current = append(t.current, interval)
		} else {
			t.future[i] = interval
			i++
		}
	}
	t.future = t.future[:i]
}

func (t *timeIndex) currentIDs() []sID {
	IDs := make([]sID, len(t.current))
	for i := range t.current {
		IDs[i] = t.current[i].sid
	}
	return IDs
}

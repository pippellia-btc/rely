package rely

import (
	"cmp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/smallset"
)

// sID is the internal representation of a unique subscription identifier, which
// is identical to [subscription.uid]. Used only to make the code more readable
type sID string

// Join multiple strings into one, separated by ":". Useful to produce canonical UIDs.
func join(strs ...string) string { return strings.Join(strs, ":") }

// Dispatcher is responsible for managing the clients and subscriptions state,
// essential for efficient broadcasting of events and for a graceful shutdown.
// Its methods are *not* safe for concurrent use, and must be syncronized externally.
type dispatcher struct {
	clients       map[*client]struct{}
	subscriptions map[sID]subscription

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
		subscriptions: make(map[sID]subscription, 1000),
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
func (d *dispatcher) open(s subscription) {
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
func (d *dispatcher) close(id sID) {
	sub, exists := d.subscriptions[id]
	if exists {
		sub.cancel()
		d.indexes.remove(sub)
		delete(d.subscriptions, id)

		d.stats.subscriptions.Add(-1)
		d.stats.filters.Add(-int64(len(sub.filters)))
	}
}

func (d *dispatcher) viewSubs(c *client) []Subscription {
	IDs := d.indexes.byClient[c.uid]
	subs := make([]Subscription, IDs.Size())
	for i, id := range IDs.Ascend() {
		subs[i] = d.subscriptions[id]
	}
	return subs
}

func (d *dispatcher) broadcast(e *nostr.Event) {
	for _, id := range d.indexes.candidates(e) {
		sub := d.subscriptions[id]
		if sub.Matches(e) {
			sub.client.send(eventResponse{ID: sub.id, Event: e})
		}
	}
}

// Add the subscription to the dispatcher indexes, one filter at the time.
// Filter indexing is assumed to be safe, given that all filters have passed the Reject.Req.
//
// The subs in the byClient index is not checked before insertion, because it must be
// managed at a higher level in the [dispatcher.register] and [dispatcher.unregister] to avoid multiple
// creation/deletion during the life-time of the client.
func (i dispatcherIndexes) add(s subscription) {
	sid := sID(s.uid)
	cid := s.client.uid
	i.byClient[cid].Add(sid)

	for _, f := range s.filters {
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
func (i dispatcherIndexes) remove(s subscription) {
	sid := sID(s.uid)
	cid := s.client.uid
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
	current     *smallset.Custom[intervalFilter]
	future      *smallset.Custom[intervalFilter]
}

// newTimeIndex returns a [timeIndex] using the (dynamic) time window [now - width, now + width].
func newTimeIndex(width int64) *timeIndex {
	return &timeIndex{
		width:   width,
		current: smallset.NewCustom(sortByUntil, 1024),
		future:  smallset.NewCustom(sortBySince, 1024),
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

// IsInvalid returns whether the interval's bound are inverted.
func (i intervalFilter) IsInvalid() bool {
	return i.since > i.until
}

// sortBySince is a comparison function that sorts [intervalFilter]s by their since.
// If they have the same since, then we compare them by their unique id to avoid
// incorrectly deduplicating them.
func sortBySince(i1, i2 intervalFilter) int {
	if i1.since < i2.since {
		return -1
	}
	if i1.since > i2.since {
		return 1
	}
	return cmp.Compare(i1.sid, i2.sid)
}

// sortByUntil is a comparison function that sorts [intervalFilter]s by their until.
// If they have the same until, then we compare them by their unique id to avoid
// incorrectly deduplicating them.
func sortByUntil(i1, i2 intervalFilter) int {
	if i1.until < i2.until {
		return -1
	}
	if i1.until > i2.until {
		return 1
	}
	return cmp.Compare(i1.sid, i2.sid)
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
	return t.current.Size() + t.future.Size()
}

// Add a filter and associated subscription ID to the timeIndex.
func (t *timeIndex) Add(f nostr.Filter, sid sID) {
	interval := newIntervalFilter(f, sid)
	t.add(interval)
}

func (t *timeIndex) add(interval intervalFilter) {
	t.advance()
	if interval.IsInvalid() {
		return
	}

	now := time.Now().Unix()
	min := now - t.width
	max := now + t.width

	if interval.until < min {
		// assumption: it's unlikely that events this old will be broadcasted,
		// so we simply don't index this filter.
		return
	}

	if interval.since > max {
		t.future.Add(interval)
	} else {
		t.current.Add(interval)
	}
}

// Remove the nostr filter with the associated subscription ID from the timeIndex.
func (t *timeIndex) Remove(f nostr.Filter, sid sID) {
	interval := newIntervalFilter(f, sid)
	t.remove(interval)
}

func (t *timeIndex) remove(interval intervalFilter) {
	t.advance()
	if interval.IsInvalid() {
		return
	}

	t.current.Remove(interval)
	t.future.Remove(interval)
}

// Candidates returns the set of subscription IDs that are likely to match
// the event with the provided creation time.
// It returns whether it found any candidates.
func (t *timeIndex) Candidates(createdAt nostr.Timestamp) (*smallset.Ordered[sID], bool) {
	t.advance()
	now := time.Now().Unix()
	min := now - t.width
	max := now + t.width

	if int64(createdAt) < min || int64(createdAt) > max {
		// fast path that avoids returning candidates that will likely be false-positives
		return nil, false
	}

	IDs := t.currentIDs()
	if len(IDs) == 0 {
		return nil, false
	}

	return smallset.NewFrom(IDs...), true
}

func (t *timeIndex) advance() {
	now := time.Now().Unix()
	if now == t.lastAdvance {
		// advance only once per second, as this is the "resolution" of the unix time
		return
	}

	t.lastAdvance = now
	min := now - t.width
	max := now + t.width

	// move intervals from future to current.
	for _, interval := range t.future.Ascend() {
		if interval.since > max {
			break
		}

		t.current.Add(interval)
	}

	t.future.RemoveBefore(intervalFilter{since: max + 1})
	t.current.RemoveBefore(intervalFilter{until: min + 1})
}

func (t *timeIndex) currentIDs() []sID {
	IDs := make([]sID, t.current.Size())
	for i, interval := range t.current.Ascend() {
		IDs[i] = interval.sid
	}
	return IDs
}

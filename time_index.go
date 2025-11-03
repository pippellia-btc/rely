package rely

import (
	"cmp"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/smallset"
)

// timeIndex organize [intervalFilter]s into two categories:
// - current: filters that intersect the (dynamic) time window [now - radius, now + radius]
// - future: filters that don't intersect the time window but will in the future.
//
// The working assumption is that the vast majority of broadcasted events will have a CreatedAt inside this window.
// Thanks to this assumption, we can reduce the number of candidates,
// which drammatically improves speed and memory usage.
type timeIndex struct {
	radius      int64
	lastAdvance int64
	current     *smallset.Custom[intervalFilter]
	future      *smallset.Custom[intervalFilter]
}

// newTimeIndex returns a [timeIndex] using the (dynamic) time window [now - radius, now + radius].
func newTimeIndex(radius int64) *timeIndex {
	return &timeIndex{
		radius:  radius,
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
	if interval.IsInvalid() {
		return
	}

	now := time.Now().Unix()
	min := now - t.radius
	max := now + t.radius

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
	if interval.IsInvalid() {
		// the interval wasn't index, so there is nothing to remove
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
	min := now - t.radius
	max := now + t.radius

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
	min := now - t.radius
	max := now + t.radius

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

package rely

import (
	"slices"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/smallset"
)

func TestTimeIndexAdd(t *testing.T) {
	tests := []struct {
		name                string
		interval            intervalFilter
		inCurrent, inFuture bool
	}{
		{
			name:      "invalid interval, not indexed",
			interval:  intervalFilter{since: 11, until: 10},
			inCurrent: false, inFuture: false,
		},
		{
			name:      "until is too much into the past, not indexed",
			interval:  intervalFilter{since: 11, until: 12},
			inCurrent: false, inFuture: false,
		},
		{
			name:      "indexed into current",
			interval:  intervalFilter{since: time.Now().Unix(), until: time.Now().Add(+10 * time.Second).Unix()},
			inCurrent: true, inFuture: false,
		},
		{
			name:      "indexed into future",
			interval:  intervalFilter{since: time.Now().Add(+1000 * time.Second).Unix(), until: time.Now().Add(+10_000 * time.Second).Unix()},
			inCurrent: false, inFuture: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := newTimeIndex(512)
			index.add(test.interval)

			inCurrent := index.current.Contains(test.interval)
			if inCurrent != test.inCurrent {
				t.Fatalf("expected %v, got %v; current %v", test.inCurrent, inCurrent, index.current)
			}

			inFuture := index.future.Contains(test.interval)
			if inFuture != test.inFuture {
				t.Fatalf("expected %v, got %v; future %v", test.inFuture, inFuture, index.future)
			}
		})
	}
}

func TestTimeIndexAdvance(t *testing.T) {
	tests := []struct {
		name                 string
		current, future      []intervalFilter
		expectedC, expectedF []intervalFilter
	}{
		{
			name:    "removal from current",
			current: []intervalFilter{{until: time.Now().Unix() - 100}}, future: []intervalFilter{},
			expectedC: []intervalFilter{}, expectedF: []intervalFilter{},
		},
		{
			name:    "from future to current",
			current: []intervalFilter{}, future: []intervalFilter{{since: time.Now().Unix() + 1, until: end}},
			expectedC: []intervalFilter{{since: time.Now().Unix() + 1, until: end}}, expectedF: []intervalFilter{},
		},
		{
			name: "multiple",
			current: []intervalFilter{
				{until: time.Now().Unix() - 10, sid: "a"}, // will be removed
				{until: time.Now().Unix() - 10, sid: "b"}, // will be removed
				{until: time.Now().Unix() + 100, sid: "c"},
			},
			future: []intervalFilter{
				{since: time.Now().Unix() + 1, until: end, sid: "x"}, // will go to current
				{since: time.Now().Unix() + 1000, until: end, sid: "y"},
			},
			expectedC: []intervalFilter{
				{until: time.Now().Unix() + 100, sid: "c"},
				{since: time.Now().Unix() + 1, until: end, sid: "x"},
			},
			expectedF: []intervalFilter{
				{since: time.Now().Unix() + 1000, until: end, sid: "y"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := timeIndex{
				radius:  10,
				current: smallset.NewCustomFrom(sortByUntil, test.current...),
				future:  smallset.NewCustomFrom(sortBySince, test.future...),
			}
			index.advance()

			current := index.current.Items()
			if !slices.Equal(current, test.expectedC) {
				t.Errorf("expected current %v, got %v", test.expectedC, current)
			}

			future := index.future.Items()
			if !slices.Equal(future, test.expectedF) {
				t.Errorf("expected future %v, got %v", test.expectedF, future)
			}
		})
	}
}

func BenchmarkTimeIndexAdd(b *testing.B) {
	t := newTimeIndex(512)
	b.ResetTimer()
	for i := range b.N {
		sub := testSubs[i%testSize]
		sid := sID(sub.UID())
		for _, f := range sub.filters {
			t.Add(f, sid)
		}
	}
}

func BenchmarkTimeIndexRemove(b *testing.B) {
	t := newTimeIndex(512)
	for _, sub := range testSubs {
		sid := sID(sub.UID())
		for _, f := range sub.filters {
			t.Add(f, sid)
		}
	}

	b.ResetTimer()
	for i := range b.N {
		sub := testSubs[i%testSize]
		sid := sID(sub.UID())
		for _, f := range sub.filters {
			t.Remove(f, sid)
		}
	}
}

func BenchmarkTimeIndexCandidates(b *testing.B) {
	t := newTimeIndex(512)
	for _, sub := range testSubs {
		sid := sID(sub.UID())
		for _, f := range sub.filters {
			t.Add(f, sid)
		}
	}

	b.ResetTimer()
	for range b.N {
		t.Candidates(nostr.Now())
	}
}

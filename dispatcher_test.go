package rely

import (
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely/tests"
	"github.com/pippellia-btc/slicex"
	"github.com/pippellia-btc/smallset"
)

const testSize = 1000

var testSubs []Subscription

func init() {
	testSubs = make([]Subscription, testSize)
	for i := range testSize {
		id := strconv.Itoa(i)
		sub := Subscription{
			ID:      id,
			Filters: tests.RandomFilters(),
			client:  &client{uid: id},
		}

		testSubs[i] = sub
	}
}

func TestIndexAdd(t *testing.T) {
	i := newDispatcherIndexes()
	sub := Subscription{
		uid:     "0:test",
		Filters: nostr.Filters{{IDs: []string{"xxx"}}},
		client:  &client{uid: "0"},
	}

	i.byClient["0"] = smallset.New[sID](20)
	i.add(sub)
	sIDs := i.byID["xxx"].Items()
	expected := []sID{"0:test"}

	if !reflect.DeepEqual(sIDs, expected) {
		t.Fatalf("expected %v, got %v", expected, sIDs)
	}
}

func TestIndexRemove(t *testing.T) {
	i := newDispatcherIndexes()
	sID := sID("0:test")
	sub := Subscription{
		uid:     string(sID),
		Filters: nostr.Filters{{IDs: []string{"abc"}}},
		client:  &client{uid: "0"},
	}

	i.byClient["0"] = smallset.NewFrom(sID)
	i.byID["abc"] = smallset.NewFrom(sID)
	i.remove(sub)

	if _, ok := i.byID["abc"]; ok {
		t.Fatalf("byID[\"abc\"] should have been deleted")
	}
}

func TestIndexingSymmetry(t *testing.T) {
	i := newDispatcherIndexes()
	for _, sub := range testSubs {
		cid := sub.client.uid
		i.byClient[cid] = smallset.New[sID](20)
		i.add(sub)
	}

	slicex.Shuffle(testSubs)
	for _, sub := range testSubs {
		cid := sub.client.uid
		i.remove(sub)
		delete(i.byClient, cid)
	}

	if len(i.byClient) > 0 || len(i.byID) > 0 || len(i.byAuthor) > 0 || len(i.byKind) > 0 || len(i.byTag) > 0 || i.byTime.size() > 0 {
		t.Errorf("expected all maps empty, got byClient=%d byID=%d byAuthor=%d byKind=%d byTag=%d byTime=%d",
			len(i.byClient), len(i.byID), len(i.byAuthor), len(i.byKind), len(i.byTag), i.byTime.size())
	}
}

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
				width:   10,
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

func TestIsLetter(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"a", true},
		{"Z", true},
		{"m", true},
		{"9", false},
		{"#", false},
		{"aa", false},
		{"", false},
		{"é", false},
	}

	for _, test := range tests {
		got := isLetter(test.input)
		if got != test.expected {
			t.Fatalf("expected %v, got %v", test.expected, got)
		}
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		inputs   []string
		expected string
	}{
		{inputs: nil, expected: ""},
		{inputs: []string{}, expected: ""},
		{inputs: []string{"ciao"}, expected: "ciao"},
		{inputs: []string{"ciao", "mamma"}, expected: "ciao:mamma"},
	}

	for _, test := range tests {
		got := join(test.inputs...)
		if got != test.expected {
			t.Fatalf("expected %v, got %v", test.expected, got)
		}
	}
}

func timestamp(unix int64) *nostr.Timestamp {
	ts := nostr.Timestamp(unix)
	return &ts
}

func BenchmarkIndexAdd(b *testing.B) {
	indexes := newDispatcherIndexes()
	for _, sub := range testSubs {
		cid := sub.client.uid
		indexes.byClient[cid] = smallset.New[sID](20)
	}

	b.ResetTimer()
	for i := range b.N {
		indexes.add(testSubs[i%testSize])
	}
}

func BenchmarkIndexRemove(b *testing.B) {
	indexes := newDispatcherIndexes()
	for _, sub := range testSubs {
		cid := sub.client.uid
		indexes.byClient[cid] = smallset.New[sID](20)
		indexes.add(sub)
	}

	b.ResetTimer()
	for i := range b.N {
		indexes.remove(testSubs[i%testSize])
	}
}

func BenchmarkIndexCandidates(b *testing.B) {
	indexes := newDispatcherIndexes()
	for _, sub := range testSubs {
		cid := sub.client.uid
		indexes.byClient[cid] = smallset.New[sID](20)
		indexes.add(sub)
	}

	event := tests.RandomEvent()

	b.ResetTimer()
	for range b.N {
		indexes.candidates(&event)
	}
}

func BenchmarkTimeIndexAdd(b *testing.B) {
	t := newTimeIndex(512)
	b.ResetTimer()
	for i := range b.N {
		sub := testSubs[i%testSize]
		sid := sID(sub.UID())
		for _, f := range sub.Filters {
			t.Add(f, sid)
		}
	}
}

func BenchmarkTimeIndexRemove(b *testing.B) {
	t := newTimeIndex(512)
	for _, sub := range testSubs {
		sid := sID(sub.UID())
		for _, f := range sub.Filters {
			t.Add(f, sid)
		}
	}

	b.ResetTimer()
	for i := range b.N {
		sub := testSubs[i%testSize]
		sid := sID(sub.UID())
		for _, f := range sub.Filters {
			t.Remove(f, sid)
		}
	}
}

func BenchmarkTimeIndexCandidates(b *testing.B) {
	t := newTimeIndex(512)
	for _, sub := range testSubs {
		sid := sID(sub.UID())
		for _, f := range sub.Filters {
			t.Add(f, sid)
		}
	}

	b.ResetTimer()
	for range b.N {
		t.Candidates(nostr.Now())
	}
}

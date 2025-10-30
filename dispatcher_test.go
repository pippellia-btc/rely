package rely

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely/tests"
	"github.com/pippellia-btc/slicex"
	"github.com/pippellia-btc/smallset"
)

const testSize = 1000

var testSubs []subscription

func init() {
	testSubs = make([]subscription, testSize)
	for i := range testSize {
		id := strconv.Itoa(i)
		sub := subscription{
			id:      id,
			filters: tests.RandomFilters(),
			client:  &client{uid: id},
		}

		testSubs[i] = sub
	}
}

func TestIndexAdd(t *testing.T) {
	i := newDispatcherIndexes()
	sub := subscription{
		uid:     "0:test",
		filters: nostr.Filters{{IDs: []string{"xxx"}}},
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
	sub := subscription{
		uid:     string(sID),
		filters: nostr.Filters{{IDs: []string{"abc"}}},
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

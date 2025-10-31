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

func TestIndex(t *testing.T) {
	d := newDispatcher(&Relay{})
	sub := subscription{
		uid:     "0:test",
		filters: nostr.Filters{{IDs: []string{"xxx"}}},
		client:  &client{uid: "0"},
	}

	d.Index(sub)

	if _, ok := d.subscriptions["0:test"]; !ok {
		t.Errorf("subscription wasn't added to the map")
	}

	sIDs := d.byID["xxx"].Items()
	expected := []sID{"0:test"}

	if !reflect.DeepEqual(sIDs, expected) {
		t.Errorf("expected %v, got %v", expected, sIDs)
	}
}

func TestUnindex(t *testing.T) {
	d := newDispatcher(&Relay{})
	sID := sID("0:test")
	sub := subscription{
		uid:     string(sID),
		filters: nostr.Filters{{IDs: []string{"abc"}}},
		client:  &client{uid: "0"},
	}

	// manual indexing
	d.subscriptions[sID] = sub
	d.byID["abc"] = smallset.NewFrom(sID)

	d.Unindex(sub)

	if _, ok := d.subscriptions[sID]; ok {
		t.Errorf("subscriptions[\"0:test\"] should have been deleted")
	}

	if _, ok := d.byID["abc"]; ok {
		t.Errorf("byID[\"abc\"] should have been deleted")
	}
}

func TestIndexingSymmetry(t *testing.T) {
	i := newDispatcher(&Relay{})
	for _, sub := range testSubs {
		i.Index(sub)
	}

	slicex.Shuffle(testSubs)
	for _, sub := range testSubs {
		i.Unindex(sub)
	}

	if len(i.byID) > 0 || len(i.byAuthor) > 0 || len(i.byKind) > 0 || len(i.byTag) > 0 || i.byTime.size() > 0 {
		t.Errorf("expected all maps empty, got byID=%d byAuthor=%d byKind=%d byTag=%d byTime=%d",
			len(i.byID), len(i.byAuthor), len(i.byKind), len(i.byTag), i.byTime.size())
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

func BenchmarkIndex(b *testing.B) {
	d := newDispatcher(&Relay{})
	b.ResetTimer()
	for i := range b.N {
		d.Index(testSubs[i%testSize])
	}
}

func BenchmarkUnindex(b *testing.B) {
	d := newDispatcher(&Relay{})
	for _, sub := range testSubs {
		d.Index(sub)
	}

	b.ResetTimer()
	for i := range b.N {
		d.Unindex(testSubs[i%testSize])
	}
}

func BenchmarkCandidates(b *testing.B) {
	d := newDispatcher(&Relay{})
	for _, sub := range testSubs {
		d.Index(sub)
	}

	event := tests.RandomEvent()
	b.ResetTimer()
	for range b.N {
		d.Candidates(&event)
	}
}

package rely

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely/tests"
	"github.com/pippellia-btc/slicex"
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

func TestIndex(t *testing.T) {
	d := newDispatcher()
	sub := Subscription{
		ID:      "test",
		Filters: nostr.Filters{{IDs: []string{"xxx"}}},
		client:  &client{uid: "0"},
	}

	d.index(sub)
	sIDs := d.byID["xxx"]
	expected := []sID{"0:test"}

	if !reflect.DeepEqual(sIDs, expected) {
		t.Fatalf("expected %v, got %v", expected, sIDs)
	}
}

func TestUnindex(t *testing.T) {
	d := newDispatcher()
	sub := Subscription{
		ID:      "test",
		Filters: nostr.Filters{{IDs: []string{"abc"}}},
		client:  &client{uid: "0"},
	}

	d.byClient["x:0"] = []sID{"x:0:test"}
	d.byID["abc"] = []sID{"0:test"}
	d.unindex(sub)

	if _, ok := d.byID["abc"]; ok {
		t.Fatalf("expected key 'abc' deleted after double unindex")
	}
}

func TestIndexingSymmetry(t *testing.T) {
	d := newDispatcher()
	for _, sub := range testSubs {
		d.index(sub)
	}

	slicex.Shuffle(testSubs)
	for _, sub := range testSubs {
		d.unindex(sub)
	}

	if len(d.byID) > 0 || len(d.byAuthor) > 0 || len(d.byKind) > 0 || len(d.byTag) > 0 {
		t.Errorf("expected all maps empty, got byID=%d byAuthor=%d byKind=%d byTag=%d",
			len(d.byID), len(d.byAuthor), len(d.byKind), len(d.byTag))
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

func BenchmarkIndex(b *testing.B) {
	d := newDispatcher()
	b.ResetTimer()
	for i := range b.N {
		d.index(testSubs[i%testSize])
	}
}

func BenchmarkUnindex(b *testing.B) {
	d := newDispatcher()
	for _, sub := range testSubs {
		d.index(sub)
	}

	b.ResetTimer()
	for i := range b.N {
		d.unindex(testSubs[i%testSize])
	}
}

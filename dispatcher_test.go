package rely

import (
	"reflect"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely/tests"
	"github.com/pippellia-btc/slicex"
)

func TestIndexIdempotency(t *testing.T) {
	d := newDispatcher()
	sid := sID("test")
	filters := nostr.Filters{{IDs: []string{"xxx"}}}

	d.index(sid, filters)
	d.index(sid, filters)

	sIDs := d.byID["xxx"]
	expected := []sID{sid}
	if !reflect.DeepEqual(sIDs, expected) {
		t.Fatalf("expected %v, got %v", expected, sIDs)
	}
}

func TestUnindexIdempotency(t *testing.T) {
	d := newDispatcher()
	sid := sID("test")
	filters := nostr.Filters{{IDs: []string{"abc"}}}

	d.byID["abc"] = []sID{sid}
	d.unindex(sid, filters)
	d.unindex(sid, filters)

	if _, ok := d.byID["abc"]; ok {
		t.Fatalf("expected key 'abc' deleted after double unindex")
	}
}

func TestIndexingSymmetry(t *testing.T) {
	d := newDispatcher()
	sid := sID("test")

	filters := tests.RandomFilters()
	d.index(sid, filters)

	slicex.Shuffle(filters)
	d.unindex(sid, filters)

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

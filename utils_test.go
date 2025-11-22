package rely

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestApplyBudget(t *testing.T) {
	tests := []struct {
		name     string
		budget   int
		filters  nostr.Filters
		expected nostr.Filters
	}{
		{
			name:   "empty filters",
			budget: 100,
		},
		{
			name:     "budget 0",
			budget:   0,
			filters:  nostr.Filters{{Limit: 69}, {Limit: 420}},
			expected: nostr.Filters{{Limit: 0, LimitZero: true}, {Limit: 0, LimitZero: true}},
		},
		{
			name:     "limitZero",
			budget:   100,
			filters:  nostr.Filters{{LimitZero: true}},
			expected: nostr.Filters{{LimitZero: true}},
		},
		{
			name:     "one filter, overlimit",
			budget:   100,
			filters:  nostr.Filters{{Limit: 1000}},
			expected: nostr.Filters{{Limit: 100}},
		},
		{
			name:     "one filter, unspecified",
			budget:   100,
			filters:  nostr.Filters{{}},
			expected: nostr.Filters{{Limit: 100}},
		},
		{
			name:     "overlimit, all specified",
			budget:   100,
			filters:  nostr.Filters{{Limit: 1}, {Limit: 500}, {Limit: 99}},
			expected: nostr.Filters{{Limit: 1}, {Limit: 82}, {Limit: 16}},
		},
		{
			name:     "negative limit",
			budget:   100,
			filters:  nostr.Filters{{Limit: -1}, {Limit: 400}},
			expected: nostr.Filters{{Limit: 20}, {Limit: 80}},
		},
		{
			name:     "all underlimit",
			budget:   100,
			filters:  nostr.Filters{{Limit: 1}, {Limit: 1}},
			expected: nostr.Filters{{Limit: 1}, {Limit: 1}},
		},
		{
			name:     "all unspecified",
			budget:   100,
			filters:  nostr.Filters{{}, {}, {}},
			expected: nostr.Filters{{Limit: 33}, {Limit: 33}, {Limit: 33}},
		},
		{
			name:     "more filters than budget",
			budget:   2,
			filters:  nostr.Filters{{Limit: 10}, {Limit: 10}, {Limit: 10}},
			expected: nostr.Filters{{Limit: 1}, {Limit: 1}, {LimitZero: true}},
		},
		{
			name:     "overlimit, all specified",
			budget:   100,
			filters:  nostr.Filters{{Limit: 500}, {Limit: 100}, {Limit: 0}},
			expected: nostr.Filters{{Limit: 71}, {Limit: 14}, {Limit: 14}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ApplyBudget(test.budget, test.filters...)
			if !reflect.DeepEqual(test.filters, test.expected) {
				t.Fatalf("expected filters %v, got %v", test.expected, test.filters)
			}
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{url: "", expected: ""},
		{url: "wss://example.com/ciao", expected: "example.com/ciao"},
		{url: "http://example.com/ciao", expected: "example.com/ciao"},
		{url: "ws://example.com/", expected: "example.com"},
		{url: "  example.com/ciao  ", expected: "example.com/ciao"},
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("Case=%d", i), func(t *testing.T) {
			result := normalizeURL(test.url)
			if !strings.EqualFold(result, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, result)
			}
		})
	}
}

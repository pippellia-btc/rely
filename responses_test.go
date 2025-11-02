package rely

import (
	"fmt"
	"slices"
	"testing"

	"github.com/goccy/go-json"
	"github.com/nbd-wtf/go-nostr"
)

func TestMarshalRawEventResponse(t *testing.T) {
	tests := []struct {
		response rawEventResponse
		expected []byte
	}{
		{
			response: rawEventResponse{
				ID:    "sub-1",
				Event: json.RawMessage(`{"kind":1,"content":"hello"}`),
			},
			expected: []byte(`["EVENT","sub-1",{"kind":1,"content":"hello"}]`),
		},
		{
			response: rawEventResponse{
				ID:    `sub"123\id\n`, // unsafe ID
				Event: json.RawMessage(`{"kind":1,"content":"hello"}`),
			},
			expected: []byte(`["EVENT","sub\"123\\id\\n",{"kind":1,"content":"hello"}]`),
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("Case=%d", i), func(t *testing.T) {
			res, err := test.response.MarshalJSON()
			if err != nil {
				t.Fatalf("expected nil, got %v", err)
			}

			if !json.Valid(res) {
				t.Fatalf("got invalid json %v", res)
			}

			if !slices.Equal(res, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, res)
			}
		})
	}
}

var (
	benchEvent = nostr.Event{
		ID:        "e0a28a87157353b28153e355b6e99ad9e1a097578039e079d4ade30075e5e3ce",
		PubKey:    "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
		CreatedAt: nostr.Timestamp(1762099567),
		Kind:      1,
		Content:   "hello from the nostr army knife",
		Sig:       "2ffc44c03fa03cc22134b660c2bdfe88f23a3d3076a2c8ef0d99a6e6d6615452a136fef689252e789e78e568e0fbc4cdb6bf7d8e396b73fa78b7c065c381620a",
	}

	benchEventResponse = eventResponse{ID: "testing", Event: &benchEvent}
)

func BenchmarkMarshalEventResponse(b *testing.B) {
	for range b.N {
		_, err := benchEventResponse.MarshalJSON()
		if err != nil {
			b.Fatalf("benchmark failed: %v", err)
		}
	}
}

func BenchmarkMarshalRawEventResponse(b *testing.B) {
	bytes, err := benchEventResponse.Event.MarshalJSON()
	if err != nil {
		b.Fatalf("benchmark failed: %v", err)
	}

	response := rawEventResponse{
		ID:    benchEventResponse.ID,
		Event: json.RawMessage(bytes),
	}

	b.ResetTimer()
	for range b.N {
		_, err := response.MarshalJSON()
		if err != nil {
			b.Fatalf("benchmark failed: %v", err)
		}
	}
}

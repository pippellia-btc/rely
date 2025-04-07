package rely

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		data []byte

		label string
		array []json.RawMessage
		err   error
	}{
		{
			name: "invalid array",
			data: []byte(`["CLOSE", ]`),
			err:  ErrGeneric,
		},
		{
			name: "label is not a string",
			data: []byte(`[111, "ciao"]`),
			err:  ErrGeneric,
		},
		{
			name: "array is too short",
			data: []byte(`["CLOSE"]`),
			err:  ErrGeneric,
		},

		{
			name:  "valid",
			data:  []byte(`["CLOSE", "abc"]`),
			label: "CLOSE",
			array: []json.RawMessage{json.RawMessage(`"abc"`)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			label, array, err := JSONArray(test.data)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if label != test.label {
				t.Fatalf("expected label %s, got %s", test.label, label)
			}

			if !reflect.DeepEqual(array, test.array) {
				t.Fatalf("expected json array %v, got %v", test.array, array)
			}
		})
	}
}

func TestParseToEventRequest(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *EventRequest
		err      *RequestError
	}{
		{
			name: "invalid",
			data: []byte(`["EVENT", "sdada"]`),
			err:  &RequestError{Err: ErrInvalidEventRequest},
		},
		{
			name:     "valid kind 1",
			data:     []byte(`["EVENT", {"kind":1,"id":"dc90c95f09947507c1044e8f48bcf6350aa6bff1507dd4acfc755b9239b5c962","pubkey":"3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d","created_at":1644271588,"tags":[],"content":"now that https://blueskyweb.org/blog/2-7-2022-overview was announced we can stop working on nostr?","sig":"230e9d8f0ddaf7eb70b5f7741ccfa37e87a455c9a469282e3464e2052d3192cd63a167e196e381ef9d7e69e9ea43af2443b839974dc85d8aaab9efe1d9296524"}]`),
			expected: &EventRequest{Event: &nostr.Event{ID: "dc90c95f09947507c1044e8f48bcf6350aa6bff1507dd4acfc755b9239b5c962", PubKey: "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d", CreatedAt: 1644271588, Kind: 1, Tags: nostr.Tags{}, Content: "now that https://blueskyweb.org/blog/2-7-2022-overview was announced we can stop working on nostr?", Sig: "230e9d8f0ddaf7eb70b5f7741ccfa37e87a455c9a469282e3464e2052d3192cd63a167e196e381ef9d7e69e9ea43af2443b839974dc85d8aaab9efe1d9296524"}},
		},
		{
			name:     "valid kind 3",
			data:     []byte(`["EVENT", {"kind":3,"id":"9e662bdd7d8abc40b5b15ee1ff5e9320efc87e9274d8d440c58e6eed2dddfbe2","pubkey":"373ebe3d45ec91977296a178d9f19f326c70631d2a1b0bbba5c5ecc2eb53b9e7","created_at":1644844224,"tags":[["p","3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"],["p","75fc5ac2487363293bd27fb0d14fb966477d0f1dbc6361d37806a6a740eda91e"],["p","46d0dfd3a724a302ca9175163bdf788f3606b3fd1bb12d5fe055d1e418cb60ea"]],"content":"{\"wss://nostr-pub.wellorder.net\":{\"read\":true,\"write\":true},\"wss://nostr.bitcoiner.social\":{\"read\":false,\"write\":true},\"wss://expensive-relay.fiatjaf.com\":{\"read\":true,\"write\":true},\"wss://relayer.fiatjaf.com\":{\"read\":true,\"write\":true},\"wss://relay.bitid.nz\":{\"read\":true,\"write\":true},\"wss://nostr.rocks\":{\"read\":true,\"write\":true}}","sig":"811355d3484d375df47581cb5d66bed05002c2978894098304f20b595e571b7e01b2efd906c5650080ffe49cf1c62b36715698e9d88b9e8be43029a2f3fa66be"}]`),
			expected: &EventRequest{Event: &nostr.Event{ID: "9e662bdd7d8abc40b5b15ee1ff5e9320efc87e9274d8d440c58e6eed2dddfbe2", PubKey: "373ebe3d45ec91977296a178d9f19f326c70631d2a1b0bbba5c5ecc2eb53b9e7", CreatedAt: 1644844224, Kind: 3, Tags: nostr.Tags{{"p", "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"}, {"p", "75fc5ac2487363293bd27fb0d14fb966477d0f1dbc6361d37806a6a740eda91e"}, {"p", "46d0dfd3a724a302ca9175163bdf788f3606b3fd1bb12d5fe055d1e418cb60ea"}}, Content: "{\"wss://nostr-pub.wellorder.net\":{\"read\":true,\"write\":true},\"wss://nostr.bitcoiner.social\":{\"read\":false,\"write\":true},\"wss://expensive-relay.fiatjaf.com\":{\"read\":true,\"write\":true},\"wss://relayer.fiatjaf.com\":{\"read\":true,\"write\":true},\"wss://relay.bitid.nz\":{\"read\":true,\"write\":true},\"wss://nostr.rocks\":{\"read\":true,\"write\":true}}", Sig: "811355d3484d375df47581cb5d66bed05002c2978894098304f20b595e571b7e01b2efd906c5650080ffe49cf1c62b36715698e9d88b9e8be43029a2f3fa66be"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, json, err := JSONArray(test.data)
			if err != nil {
				t.Fatalf("expected error nil, got %v", err)
			}

			event, err := ParseEventRequest(json)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if !reflect.DeepEqual(event, test.expected) {
				t.Fatalf("expected event request %v, got %v", test.expected, event)
			}
		})
	}
}

func TestParseToReqRequest(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *ReqRequest
		err      *RequestError
	}{
		{
			name: "ID not a string",
			data: []byte(`["REQ", 111, {"kinds": [1]}]`),
			err:  &RequestError{Err: ErrInvalidSubscriptionID},
		},
		{
			name: "incorrect lenght",
			data: []byte(`["REQ", "abc"]`),
			err:  &RequestError{ID: "abc", Err: ErrInvalidReqRequest},
		},
		{
			name: "empty ID",
			data: []byte(`["REQ", "", {"kinds": [1]}]`),
			err:  &RequestError{ID: "", Err: ErrInvalidSubscriptionID},
		},
		{
			name: "ID is too long",
			data: []byte(`["REQ", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", {"kinds": [1]}]`),
			err:  &RequestError{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Err: ErrInvalidSubscriptionID},
		},
		{
			name:     "valid",
			data:     []byte(`["REQ", "abcd", {"kinds": [1]}, {"kinds": [30023 ], "#d": ["buteko",    "batuke"]}]`),
			expected: &ReqRequest{subID: "abcd", Filters: nostr.Filters{{Kinds: []int{1}, Tags: nostr.TagMap{}}, {Kinds: []int{30023}, Tags: nostr.TagMap{"d": {"buteko", "batuke"}}}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, json, err := JSONArray(test.data)
			if err != nil {
				t.Fatalf("expected error nil, got %v", err)
			}

			req, err := ParseReqRequest(json)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if !reflect.DeepEqual(req, test.expected) {
				t.Fatalf("expected req request %v, got %v", test.expected, req)
			}
		})
	}
}

func TestParseToCloseRequest(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *CloseRequest
		err      *RequestError
	}{
		{
			name: "ID not a string",
			data: []byte(`["CLOSE", 111]`),
			err:  &RequestError{Err: ErrInvalidSubscriptionID},
		},
		{
			name: "empty ID",
			data: []byte(`["CLOSE", ""]`),
			err:  &RequestError{ID: "", Err: ErrInvalidSubscriptionID},
		},
		{
			name: "ID is too long",
			data: []byte(`["CLOSE", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]`),
			err:  &RequestError{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Err: ErrInvalidSubscriptionID},
		},
		{
			name:     "valid",
			data:     []byte(`["CLOSE", "abcd"]`),
			expected: &CloseRequest{subID: "abcd"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, json, err := JSONArray(test.data)
			if err != nil {
				t.Fatalf("expected error nil, got %v", err)
			}

			close, err := ParseCloseRequest(json)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if !reflect.DeepEqual(close, test.expected) {
				t.Fatalf("expected req request %v, got %v", test.expected, close)
			}
		})
	}
}

func TestParseAuthRequest(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *AuthRequest
		err      *RequestError
	}{
		{
			name: "invalid event",
			data: []byte(`["AUTH", "sdada"]`),
			err:  &RequestError{Err: ErrInvalidAuthRequest},
		},
		{
			name:     "valid",
			data:     []byte(`["AUTH", {"kind":22242,"id":"d7ae36c37cd2e2b2fde223036952b7df315be26dbeff6a3d659cf6fd1af904e0", "pubkey":"79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798", "created_at":1744028944, "tags":[["challenge","whatever"]],"content":"","sig":"5bda0b8a1daf8b229daede2c875f650bb74c430e5d8ea109d616154a98f4d70913cd6d5b8befc7f472d05903cc717527678a976ce60d38bb2805e62a2d83d2f4"}]`),
			expected: &AuthRequest{Event: &nostr.Event{Kind: 22242, ID: "d7ae36c37cd2e2b2fde223036952b7df315be26dbeff6a3d659cf6fd1af904e0", PubKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798", CreatedAt: 1744028944, Tags: nostr.Tags{{"challenge", "whatever"}}, Sig: "5bda0b8a1daf8b229daede2c875f650bb74c430e5d8ea109d616154a98f4d70913cd6d5b8befc7f472d05903cc717527678a976ce60d38bb2805e62a2d83d2f4"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, json, err := JSONArray(test.data)
			if err != nil {
				t.Fatalf("expected error nil, got %v", err)
			}

			auth, err := ParseAuthRequest(json)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if !reflect.DeepEqual(auth, test.expected) {
				t.Fatalf("expected event request %v, got %v", test.expected, auth)
			}
		})
	}
}

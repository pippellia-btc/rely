package rely

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"

	gojson "github.com/goccy/go-json"
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
			expected: nostr.Filters{{Limit: 0}, {Limit: 0}},
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
			expected: nostr.Filters{{Limit: 1}, {Limit: 83}, {Limit: 16}},
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

func TestParseLabel(t *testing.T) {
	tests := []struct {
		name string
		data []byte

		label string
		err   error
	}{
		{
			name:  "valid CLOSE",
			data:  []byte(`["CLOSE", "abc"]`),
			label: "CLOSE",
		},
		{
			name:  "valid EVENT",
			data:  []byte(`["EVENT", "abc"]`),
			label: "EVENT",
		},
		{
			name:  "valid AUTH",
			data:  []byte(`["AUTH", "abc"]`),
			label: "AUTH",
		},
		{
			name:  "valid REQ",
			data:  []byte(`["REQ", "abc"]`),
			label: "REQ",
		},
		{
			name:  "valid COUNT",
			data:  []byte(`["COUNT", "abc"]`),
			label: "COUNT",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := bytes.NewReader(test.data)
			d := gojson.NewDecoder(r)

			label, err := parseLabel(d)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if label != test.label {
				t.Fatalf("expected label %s, got %s", test.label, label)
			}
		})
	}
}

func TestParseEvent(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *eventRequest
		err      *requestError
	}{
		{
			name: "invalid",
			data: []byte(`["EVENT", "sdada"]`),
			err:  &requestError{Err: ErrInvalidEventRequest},
		},
		{
			name:     "valid kind 1",
			data:     []byte(`["EVENT", {"kind":1,"id":"dc90c95f09947507c1044e8f48bcf6350aa6bff1507dd4acfc755b9239b5c962","pubkey":"3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d","created_at":1644271588,"tags":[],"content":"now that https://blueskyweb.org/blog/2-7-2022-overview was announced we can stop working on nostr?","sig":"230e9d8f0ddaf7eb70b5f7741ccfa37e87a455c9a469282e3464e2052d3192cd63a167e196e381ef9d7e69e9ea43af2443b839974dc85d8aaab9efe1d9296524"}]`),
			expected: &eventRequest{Event: &nostr.Event{ID: "dc90c95f09947507c1044e8f48bcf6350aa6bff1507dd4acfc755b9239b5c962", PubKey: "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d", CreatedAt: 1644271588, Kind: 1, Tags: nostr.Tags{}, Content: "now that https://blueskyweb.org/blog/2-7-2022-overview was announced we can stop working on nostr?", Sig: "230e9d8f0ddaf7eb70b5f7741ccfa37e87a455c9a469282e3464e2052d3192cd63a167e196e381ef9d7e69e9ea43af2443b839974dc85d8aaab9efe1d9296524"}},
		},
		{
			name:     "valid kind 3",
			data:     []byte(`["EVENT", {"kind":3,"id":"9e662bdd7d8abc40b5b15ee1ff5e9320efc87e9274d8d440c58e6eed2dddfbe2","pubkey":"373ebe3d45ec91977296a178d9f19f326c70631d2a1b0bbba5c5ecc2eb53b9e7","created_at":1644844224,"tags":[["p","3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"],["p","75fc5ac2487363293bd27fb0d14fb966477d0f1dbc6361d37806a6a740eda91e"],["p","46d0dfd3a724a302ca9175163bdf788f3606b3fd1bb12d5fe055d1e418cb60ea"]],"content":"{\"wss://nostr-pub.wellorder.net\":{\"read\":true,\"write\":true},\"wss://nostr.bitcoiner.social\":{\"read\":false,\"write\":true},\"wss://expensive-relay.fiatjaf.com\":{\"read\":true,\"write\":true},\"wss://relayer.fiatjaf.com\":{\"read\":true,\"write\":true},\"wss://relay.bitid.nz\":{\"read\":true,\"write\":true},\"wss://nostr.rocks\":{\"read\":true,\"write\":true}}","sig":"811355d3484d375df47581cb5d66bed05002c2978894098304f20b595e571b7e01b2efd906c5650080ffe49cf1c62b36715698e9d88b9e8be43029a2f3fa66be"}]`),
			expected: &eventRequest{Event: &nostr.Event{ID: "9e662bdd7d8abc40b5b15ee1ff5e9320efc87e9274d8d440c58e6eed2dddfbe2", PubKey: "373ebe3d45ec91977296a178d9f19f326c70631d2a1b0bbba5c5ecc2eb53b9e7", CreatedAt: 1644844224, Kind: 3, Tags: nostr.Tags{{"p", "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"}, {"p", "75fc5ac2487363293bd27fb0d14fb966477d0f1dbc6361d37806a6a740eda91e"}, {"p", "46d0dfd3a724a302ca9175163bdf788f3606b3fd1bb12d5fe055d1e418cb60ea"}}, Content: "{\"wss://nostr-pub.wellorder.net\":{\"read\":true,\"write\":true},\"wss://nostr.bitcoiner.social\":{\"read\":false,\"write\":true},\"wss://expensive-relay.fiatjaf.com\":{\"read\":true,\"write\":true},\"wss://relayer.fiatjaf.com\":{\"read\":true,\"write\":true},\"wss://relay.bitid.nz\":{\"read\":true,\"write\":true},\"wss://nostr.rocks\":{\"read\":true,\"write\":true}}", Sig: "811355d3484d375df47581cb5d66bed05002c2978894098304f20b595e571b7e01b2efd906c5650080ffe49cf1c62b36715698e9d88b9e8be43029a2f3fa66be"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := bytes.NewReader(test.data)
			d := gojson.NewDecoder(r)

			_, err := parseLabel(d)
			if err != nil {
				t.Fatalf("expected error nil, got %v", err)
			}

			event, err := parseEvent(d)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if !reflect.DeepEqual(event, test.expected) {
				t.Fatalf("expected event request %v, got %v", test.expected, event)
			}
		})
	}
}

func TestParseReq(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *reqRequest
		err      *requestError
	}{
		{
			name: "ID not a string",
			data: []byte(`["REQ", 111, {"kinds": [1]}]`),
			err:  &requestError{Err: ErrInvalidSubscriptionID},
		},
		{
			name: "incorrect lenght",
			data: []byte(`["REQ", "abc"]`),
			err:  &requestError{ID: "abc", Err: ErrInvalidReqRequest},
		},
		{
			name: "empty ID",
			data: []byte(`["REQ", "", {"kinds": [1]}]`),
			err:  &requestError{ID: "", Err: ErrInvalidSubscriptionID},
		},
		{
			name: "ID is too long",
			data: []byte(`["REQ", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", {"kinds": [1]}]`),
			err:  &requestError{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Err: ErrInvalidSubscriptionID},
		},
		{
			name:     "valid",
			data:     []byte(`["REQ", "abcd", {"kinds": [1]}, {"kinds": [30023], "#d": ["buteko", "batuke"]}]`),
			expected: &reqRequest{id: "abcd", Filters: nostr.Filters{{Kinds: []int{1}, Tags: nostr.TagMap{}}, {Kinds: []int{30023}, Tags: nostr.TagMap{"d": {"buteko", "batuke"}}}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := bytes.NewReader(test.data)
			d := gojson.NewDecoder(r)

			_, err := parseLabel(d)
			if err != nil {
				t.Fatalf("expected nil, got %v", err)
			}

			req, err := parseReq(d)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if !reflect.DeepEqual(req, test.expected) {
				t.Fatalf("expected req request %v, got %v", test.expected, req)
			}
		})
	}
}

func TestParseCount(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *countRequest
		err      *requestError
	}{
		{
			name: "ID not a string",
			data: []byte(`["COUNT", 111, {"kinds": [1]}]`),
			err:  &requestError{Err: ErrInvalidSubscriptionID},
		},
		{
			name: "incorrect lenght",
			data: []byte(`["COUNT", "abc"]`),
			err:  &requestError{ID: "abc", Err: ErrInvalidCountRequest},
		},
		{
			name: "empty ID",
			data: []byte(`["COUNT", "", {"kinds": [1]}]`),
			err:  &requestError{ID: "", Err: ErrInvalidSubscriptionID},
		},
		{
			name: "ID is too long",
			data: []byte(`["COUNT", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", {"kinds": [1]}]`),
			err:  &requestError{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Err: ErrInvalidSubscriptionID},
		},
		{
			name:     "valid",
			data:     []byte(`["COUNT", "abcd", {"kinds": [1]}, {"kinds": [30023], "#d": ["buteko", "batuke"]}]`),
			expected: &countRequest{id: "abcd", Filters: nostr.Filters{{Kinds: []int{1}, Tags: nostr.TagMap{}}, {Kinds: []int{30023}, Tags: nostr.TagMap{"d": {"buteko", "batuke"}}}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := bytes.NewReader(test.data)
			d := gojson.NewDecoder(r)

			_, err := parseLabel(d)
			if err != nil {
				t.Fatalf("expected error nil, got %v", err)
			}

			count, err := parseCount(d)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if !reflect.DeepEqual(count, test.expected) {
				t.Fatalf("expected count request %v, got %v", test.expected, count)
			}
		})
	}
}

func TestParseClose(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *closeRequest
		err      *requestError
	}{
		{
			name: "ID not a string",
			data: []byte(`["CLOSE", 111]`),
			err:  &requestError{Err: ErrInvalidSubscriptionID},
		},
		{
			name: "empty ID",
			data: []byte(`["CLOSE", ""]`),
			err:  &requestError{ID: "", Err: ErrInvalidSubscriptionID},
		},
		{
			name: "ID is too long",
			data: []byte(`["CLOSE", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]`),
			err:  &requestError{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Err: ErrInvalidSubscriptionID},
		},
		{
			name:     "valid",
			data:     []byte(`["CLOSE", "abcd"]`),
			expected: &closeRequest{subID: "abcd"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := bytes.NewReader(test.data)
			d := gojson.NewDecoder(r)

			_, err := parseLabel(d)
			if err != nil {
				t.Fatalf("expected error nil, got %v", err)
			}

			close, err := parseClose(d)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if !reflect.DeepEqual(close, test.expected) {
				t.Fatalf("expected req request %v, got %v", test.expected, close)
			}
		})
	}
}

func TestParseAuth(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected *authRequest
		err      *requestError
	}{
		{
			name: "invalid event",
			data: []byte(`["AUTH", "sdada"]`),
			err:  &requestError{Err: ErrInvalidAuthRequest},
		},
		{
			name:     "valid",
			data:     []byte(`["AUTH", {"kind":22242,"id":"d7ae36c37cd2e2b2fde223036952b7df315be26dbeff6a3d659cf6fd1af904e0", "pubkey":"79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798", "created_at":1744028944, "tags":[["challenge","whatever"]],"content":"","sig":"5bda0b8a1daf8b229daede2c875f650bb74c430e5d8ea109d616154a98f4d70913cd6d5b8befc7f472d05903cc717527678a976ce60d38bb2805e62a2d83d2f4"}]`),
			expected: &authRequest{Event: &nostr.Event{Kind: 22242, ID: "d7ae36c37cd2e2b2fde223036952b7df315be26dbeff6a3d659cf6fd1af904e0", PubKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798", CreatedAt: 1744028944, Tags: nostr.Tags{{"challenge", "whatever"}}, Sig: "5bda0b8a1daf8b229daede2c875f650bb74c430e5d8ea109d616154a98f4d70913cd6d5b8befc7f472d05903cc717527678a976ce60d38bb2805e62a2d83d2f4"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := bytes.NewReader(test.data)
			d := gojson.NewDecoder(r)

			_, err := parseLabel(d)
			if err != nil {
				t.Fatalf("expected error nil, got %v", err)
			}

			auth, err := parseAuth(d)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected error %v, got %v", test.err, err)
			}

			if !reflect.DeepEqual(auth, test.expected) {
				t.Fatalf("expected event request %v, got %v", test.expected, auth)
			}
		})
	}
}

func TestValidateAuth(t *testing.T) {
	tests := []struct {
		name     string
		auth     *authRequest
		expected error
	}{
		{
			name:     "invalid kind",
			auth:     &authRequest{Event: &nostr.Event{Kind: 69, ID: "abc", CreatedAt: nostr.Now()}},
			expected: ErrInvalidAuthKind,
		},
		{
			name:     "too much into the past",
			auth:     &authRequest{Event: Signed(nostr.Event{Kind: 22242, CreatedAt: nostr.Now() - nostr.Timestamp(time.Minute+1)})},
			expected: ErrInvalidTimestamp,
		},
		{
			name:     "too much into the future",
			auth:     &authRequest{Event: Signed(nostr.Event{Kind: 22242, CreatedAt: nostr.Now() + nostr.Timestamp(time.Minute+1)})},
			expected: ErrInvalidTimestamp,
		},
		{
			name:     "no relay tag",
			auth:     &authRequest{Event: Signed(nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"challenge", "challenge"}}})},
			expected: ErrInvalidAuthRelay,
		},
		{
			name:     "relay tag is different",
			auth:     &authRequest{Event: Signed(nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"challenge", "challenge"}, {"relay", "different"}}})},
			expected: ErrInvalidAuthRelay,
		},
		{
			name:     "invalid ID",
			auth:     &authRequest{Event: &nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"challenge", "challenge"}, {"relay", "example.com"}}}},
			expected: ErrInvalidEventID,
		},
		{
			name:     "no challenge tag",
			auth:     &authRequest{Event: Signed(nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"relay", "example.com"}}})},
			expected: ErrInvalidAuthChallenge,
		},
		{
			name:     "challenge tag is different",
			auth:     &authRequest{Event: Signed(nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"relay", "example.com"}, {"challenge", "different"}}})},
			expected: ErrInvalidAuthChallenge,
		},
		{
			name:     "valid",
			auth:     &authRequest{Event: Signed(nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"relay", "example.com"}, {"challenge", "challenge"}}})},
			expected: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auther := auther{
				challenge: "challenge",
				domain:    "example.com",
			}

			requestErr := auther.Validate(test.auth)
			var err error
			if requestErr != nil {
				err = requestErr.Err
			}

			if !errors.Is(err, test.expected) {
				t.Fatalf("expected error %v, got %v", test.expected, err)
			}
		})
	}
}

func BenchmarkCreateChallenge(b *testing.B) {
	for i := 0; i < b.N; i++ {
		challenge := make([]byte, 16)
		rand.Read(challenge)
		hex.EncodeToString(challenge)
	}
}

func Signed(e nostr.Event) *nostr.Event {
	sk := nostr.GeneratePrivateKey()
	e.Sign(sk)
	return &e
}

var (
	dataEvent = []byte(`["EVENT", {"kind":1,"id":"dc90c95f09947507c1044e8f48bcf6350aa6bff1507dd4acfc755b9239b5c962","pubkey":"3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d","created_at":1644271588,"tags":[],"content":"now that https://blueskyweb.org/blog/2-7-2022-overview was announced we can stop working on nostr?","sig":"230e9d8f0ddaf7eb70b5f7741ccfa37e87a455c9a469282e3464e2052d3192cd63a167e196e381ef9d7e69e9ea43af2443b839974dc85d8aaab9efe1d9296524"}]`)
	dataReq   = []byte(`["REQ", "abcd", {"kinds": [1]}, {"kinds": [30023], "#d": ["buteko", "batuke"]}]`)

	readerEvent = bytes.NewReader(dataEvent)
	readerReq   = bytes.NewReader(dataReq)
)

func BenchmarkParseEvent(b *testing.B) {
	for range b.N {
		readerEvent.Seek(0, io.SeekStart)
		d := gojson.NewDecoder(readerEvent)

		_, err := parseLabel(d)
		if err != nil {
			b.Fatal(err)
		}

		if _, err := parseEvent(d); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseReq(b *testing.B) {
	for range b.N {
		readerReq.Seek(0, io.SeekStart)
		dec := gojson.NewDecoder(readerReq)
		_, err := parseLabel(dec)
		if err != nil {
			b.Fatal(err)
		}

		if _, err := parseReq(dec); err != nil {
			b.Fatal(err)
		}
	}
}

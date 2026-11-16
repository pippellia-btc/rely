package rely

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip77/negentropy"
	"github.com/nbd-wtf/go-nostr/nip77/negentropy/storage/vector"
)

func negTestRelay(t *testing.T, configure func(*Relay)) (*Relay, *websocket.Conn, context.CancelFunc) {
	t.Helper()
	r := NewRelay(WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	configure(r)
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	server := httptest.NewServer(r)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		cancel()
		server.Close()
		r.Wait()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Close()
		cancel()
		server.Close()
		r.Wait()
	})
	return r, conn, cancel
}

func negSend(t *testing.T, conn *websocket.Conn, message ...any) {
	t.Helper()
	if err := conn.WriteJSON(message); err != nil {
		t.Fatal(err)
	}
}

func negRead(t *testing.T, conn *websocket.Conn, label, id string) []string {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var message []string
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	if len(message) < 2 || message[0] != label || message[1] != id {
		t.Fatalf("expected %s %s, got %v", label, id, message)
	}
	return message
}

func negItems(begin, end int) []NegentropyItem {
	items := make([]NegentropyItem, 0, end-begin)
	for i := begin; i < end; i++ {
		id := sha256.Sum256([]byte(fmt.Sprint(i)))
		// Exercise ID ordering among events with identical timestamps.
		items = append(items, NegentropyItem{hex.EncodeToString(id[:]), 1700000000})
	}
	return items
}

func TestNegentropyReconciliation(t *testing.T) {
	for _, test := range []struct {
		name                 string
		clientFrom, clientTo int
		serverFrom, serverTo int
	}{
		{"empty", 0, 0, 0, 0},
		{"identical", 0, 200, 0, 200},
		{"overlap", 0, 500, 250, 750},
		{"disjoint", 0, 500, 500, 1000},
		{"download", 0, 0, 0, 500},
		{"upload", 0, 500, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			local := negItems(test.clientFrom, test.clientTo)
			remote := negItems(test.serverFrom, test.serverTo)
			_, conn, _ := negTestRelay(t, func(r *Relay) {
				// A snapshot must not be truncated to the ordinary REQ budget.
				r.settings.Sys.responseLimit = 1
				r.On.Negentropy = func(ctx context.Context, c Client, id string, filter nostr.Filter) ([]NegentropyItem, error) {
					if !slices.Equal(filter.Kinds, []int{1}) {
						return nil, errors.New("wrong filter")
					}
					return remote, nil
				}
			})
			v := vector.New()
			for _, item := range local {
				v.Insert(item.Timestamp, item.ID)
			}
			v.Seal()
			engine := negentropy.New(v, 4096)
			engine.Haves = make(chan string, 2000)
			engine.HaveNots = make(chan string, 2000)
			negSend(t, conn, "NEG-OPEN", "sync", nostr.Filter{Kinds: []int{1}}, engine.Start())
			for round := 0; ; round++ {
				if round > 100 {
					t.Fatal("reconciliation did not converge")
				}
				message := negRead(t, conn, "NEG-MSG", "sync")
				if len(message[2]) > 2*4096 {
					t.Fatal("response exceeded frame limit")
				}
				next, err := engine.Reconcile(message[2])
				if err != nil {
					t.Fatal(err)
				}
				if next == "" {
					break
				}
				negSend(t, conn, "NEG-MSG", "sync", next)
			}
			haves, needs := []string{}, []string{}
			for id := range engine.Haves {
				haves = append(haves, id)
			}
			for id := range engine.HaveNots {
				needs = append(needs, id)
			}
			wantHaves, wantNeeds := []string{}, []string{}
			for _, item := range local {
				if !slices.Contains(remote, item) {
					wantHaves = append(wantHaves, item.ID)
				}
			}
			for _, item := range remote {
				if !slices.Contains(local, item) {
					wantNeeds = append(wantNeeds, item.ID)
				}
			}
			for _, list := range [][]string{haves, needs, wantHaves, wantNeeds} {
				slices.Sort(list)
			}
			if !slices.Equal(haves, wantHaves) || !slices.Equal(needs, wantNeeds) {
				t.Fatalf("incorrect difference: have %d/%d, need %d/%d",
					len(haves), len(wantHaves), len(needs), len(wantNeeds))
			}
			negSend(t, conn, "NEG-CLOSE", "sync")
			negSend(t, conn, "NEG-MSG", "sync", "61")
			negRead(t, conn, "NEG-ERR", "sync")
		})
	}
}

func TestNegentropyWireAndNamespaces(t *testing.T) {
	_, conn, _ := negTestRelay(t, func(r *Relay) {
		r.On.Negentropy = func(context.Context, Client, string, nostr.Filter) ([]NegentropyItem, error) {
			return nil, nil
		}
	})
	negSend(t, conn, "REQ", "same", nostr.Filter{})
	negRead(t, conn, "EOSE", "same")
	negSend(t, conn, "NEG-OPEN", "same", nostr.Filter{}, "62")
	if got := negRead(t, conn, "NEG-MSG", "same")[2]; got != "61" {
		t.Fatalf("wrong version negotiation: %s", got)
	}
	negSend(t, conn, "CLOSE", "same")
	// Hand-encoded empty ID list: V1, infinity, empty ID prefix, ID list, 0 IDs.
	negSend(t, conn, "NEG-MSG", "same", "6100000200")
	if got := negRead(t, conn, "NEG-MSG", "same")[2]; got != "6100000200" {
		t.Fatalf("wrong empty-set reply: %s", got)
	}
}

func TestNegentropyCancellation(t *testing.T) {
	for _, action := range []string{"close", "replace", "disconnect", "shutdown", "timeout"} {
		t.Run(action, func(t *testing.T) {
			loaded := make(chan context.Context, 2)
			_, conn, cancel := negTestRelay(t, func(r *Relay) {
				if action == "timeout" {
					r.settings.Negentropy.Timeout = 50 * time.Millisecond
				}
				r.On.Negentropy = func(ctx context.Context, c Client, id string, filter nostr.Filter) ([]NegentropyItem, error) {
					loaded <- ctx
					<-ctx.Done()
					return nil, ctx.Err()
				}
			})
			negSend(t, conn, "NEG-OPEN", "s", nostr.Filter{}, "61")
			var ctx context.Context
			select {
			case ctx = <-loaded:
			case <-time.After(5 * time.Second):
				t.Fatal("snapshot hook never started")
			}
			switch action {
			case "close":
				negSend(t, conn, "NEG-CLOSE", "s")
			case "replace":
				negSend(t, conn, "NEG-OPEN", "s", nostr.Filter{}, "61")
			case "disconnect":
				conn.Close()
			case "shutdown":
				cancel()
			case "timeout":
				negRead(t, conn, "NEG-ERR", "s")
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("snapshot hook was not canceled")
			}
			if action == "replace" {
				select {
				case <-loaded:
				case <-time.After(5 * time.Second):
					t.Fatal("replacement snapshot never started")
				}
				negSend(t, conn, "NEG-CLOSE", "s")
			}
			if action == "close" || action == "replace" {
				negSend(t, conn, "NEG-MSG", "s", "61")
				// A canceled worker must not send a stale response.
				negRead(t, conn, "NEG-ERR", "s")
			}
		})
	}
}

func TestNegentropyRejection(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*Relay)
		prefix    string
	}{
		{"disabled", func(r *Relay) { r.On.Negentropy = nil }, "unsupported:"},
		{"policy", func(r *Relay) {
			r.Reject.Negentropy.Append(func(Client, string, nostr.Filter) error {
				return errors.New("auth-required: authenticate first")
			})
		}, "auth-required:"},
		{"snapshot", func(r *Relay) {
			r.On.Negentropy = func(context.Context, Client, string, nostr.Filter) ([]NegentropyItem, error) {
				return nil, errors.New("database unavailable")
			}
		}, "error:"},
		{"items", func(r *Relay) { r.settings.Negentropy.MaxItems = 1 }, "blocked:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, conn, _ := negTestRelay(t, func(r *Relay) {
				r.On.Negentropy = func(context.Context, Client, string, nostr.Filter) ([]NegentropyItem, error) {
					return negItems(0, 2), nil
				}
				test.configure(r)
			})
			negSend(t, conn, "NEG-OPEN", "s", nostr.Filter{}, "61")
			if reason := negRead(t, conn, "NEG-ERR", "s")[2]; !strings.HasPrefix(reason, test.prefix) {
				t.Fatalf("unexpected reason %q", reason)
			}
		})
	}
}

func TestNegentropySessionLimit(t *testing.T) {
	_, conn, _ := negTestRelay(t, func(r *Relay) {
		r.settings.Negentropy.MaxSessions = 1
		r.On.Negentropy = func(context.Context, Client, string, nostr.Filter) ([]NegentropyItem, error) {
			return nil, nil
		}
	})
	negSend(t, conn, "NEG-OPEN", "one", nostr.Filter{}, "61")
	negRead(t, conn, "NEG-MSG", "one")
	negSend(t, conn, "NEG-OPEN", "two", nostr.Filter{}, "61")
	negRead(t, conn, "NEG-ERR", "two")
	negSend(t, conn, "NEG-CLOSE", "one")
	negSend(t, conn, "NEG-OPEN", "two", nostr.Filter{}, "61")
	negRead(t, conn, "NEG-MSG", "two")
}

func TestNegentropyBusySession(t *testing.T) {
	loaded := make(chan context.Context, 1)
	_, conn, _ := negTestRelay(t, func(r *Relay) {
		r.On.Negentropy = func(ctx context.Context, c Client, id string, filter nostr.Filter) ([]NegentropyItem, error) {
			loaded <- ctx
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	negSend(t, conn, "NEG-OPEN", "s", nostr.Filter{}, "61")
	var ctx context.Context
	select {
	case ctx = <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot hook never started")
	}
	negSend(t, conn, "NEG-MSG", "s", "61")
	negRead(t, conn, "NEG-ERR", "s")
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("busy session was not canceled")
	}
}

func TestNegentropyOutputOverflow(t *testing.T) {
	_, conn, _ := negTestRelay(t, func(*Relay) {})
	// Use a separate client wrapper so the relay's writer cannot drain this queue.
	c := &client{conn: conn, responses: make(chan response, 1)}
	c.sendNegentropy(negResponse{"NEG-MSG", "s", "61"})
	c.sendNegentropy(negResponse{"NEG-MSG", "s", "61"})
	if c.DroppedResponses() != 1 {
		t.Fatal("overflow not recorded")
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`[]`)); err == nil {
		t.Fatal("overflow did not close the socket")
	}
}

func TestNegentropyProcessorOverload(t *testing.T) {
	r := NewRelay(WithQueueCapacity(1), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	r.On.Negentropy = func(context.Context, Client, string, nostr.Filter) ([]NegentropyItem, error) {
		t.Fatal("overloaded request reached storage")
		return nil, nil
	}
	c := &client{relay: r, responses: make(chan response, 1)}
	r.processor.queue <- countRequest{client: c, id: "occupy"}
	d := json.NewDecoder(strings.NewReader(`["NEG-OPEN","s",{},"61"]`))
	label, _ := parseLabel(d)
	c.handleNegentropy(label, d)
	response := (<-c.responses).(negResponse)
	if response.label != "NEG-ERR" || !strings.HasPrefix(response.payload, "blocked:") {
		t.Fatalf("unexpected response: %+v", response)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.negSessions) != 0 {
		t.Fatal("overloaded session leaked")
	}
}

func TestNegentropySnapshotValidation(t *testing.T) {
	valid := negItems(0, 1)[0]
	for _, items := range [][]NegentropyItem{
		{{ID: "bad", Timestamp: 1}},
		{{ID: valid.ID, Timestamp: -1}},
		{{ID: valid.ID, Timestamp: maxNegTimestamp}},
		{valid, valid},
		{{ID: strings.Repeat("00", 32)}, {ID: strings.Repeat("00", 31) + "01"}},
	} {
		if _, err := newNegEngine(items, DefaultNegentropyConfig()); err == nil {
			t.Errorf("accepted invalid snapshot: %v", items)
		}
	}
}

func TestParseNegentropy(t *testing.T) {
	for _, input := range []string{
		`["NEG-OPEN","s",null,"61"]`,
		`["NEG-OPEN","s",{},{},"61"]`,
		`["NEG-OPEN","s",{},"61",1]`,
		`["NEG-MSG","s","61"`,
		`["NEG-MSG","s","61"] {}`,
		`["NEG-MSG","s",null]`,
		`["NEG-MSG","","61"]`,
		`["NEG-CLOSE","s",1]`,
	} {
		d := json.NewDecoder(strings.NewReader(input))
		label, err := parseLabel(d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseNegentropy(label, d); err == nil {
			t.Errorf("accepted malformed envelope %s", input)
		}
	}
}

func TestValidateNegMessage(t *testing.T) {
	for _, message := range []string{
		"", "6", "zz", "61ff", "61000002ffffffffffffffff7f",
		"610021", "61000001", "61000003", "61000000010000",
		strings.Repeat("00", maxNegMessageSize+1),
	} {
		if err := validateNegMessage(message); err == nil {
			t.Errorf("accepted invalid payload %.80s", message)
		}
	}
}

func FuzzNegentropyPayload(f *testing.F) {
	for _, input := range []string{"61", "6100000200", "61000001" + strings.Repeat("00", 16), "62"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, message string) {
		if validateNegMessage(message) != nil {
			return
		}
		engine, err := newNegEngine(negItems(0, 64), DefaultNegentropyConfig())
		if err != nil {
			t.Fatal(err)
		}
		// Accepted input must not panic in the dependency decoder.
		engine.Reconcile(strings.ToLower(message))
	})
}

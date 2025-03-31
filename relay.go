package rely

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

const (
	DefaultWriteWait      time.Duration = 10 * time.Second
	DefaultPongWait       time.Duration = 60 * time.Second
	DefaultPingPeriod     time.Duration = 45 * time.Second
	DefaultMaxMessageSize int64         = 512000
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

type Relay struct {
	// the set of active clients
	clients map[*Client]bool

	// the channel used to register/unregister a client
	register   chan *Client
	unregister chan *Client

	// the queue of EVENTs to be processed
	eventQueue chan *EventRequest

	// the queue of REQs to be processed
	reqQueue chan *ReqRequest

	RelayFunctions

	Address string
	WebsocketLimits
}

// RelayFunctions is a collection of functions the user of this framework can customize.
type RelayFunctions struct {
	// a connection is accepted if and only if err is nil. All functions MUST be thread-safe.
	RejectConnection []func(*http.Request) error

	// an event is accepted if and only if err is nil. All functions MUST be thread-safe.
	RejectEvent []func(*Client, *nostr.Event) error

	// the filters are accepted if and only if err is nil. All functions MUST be thread-safe.
	RejectFilters []func(context.Context, *Client, nostr.Filters) error

	// the action the relay performs after establishing a connection with the specified client.
	OnConnect func(*Client) error

	// the action the relay performs on an EVENT coming from the specified client
	OnEvent func(*Client, *nostr.Event) error

	// the action the relay performs on the filters coming from a REQ from the specified client
	OnFilters func(context.Context, *Client, nostr.Filters) ([]nostr.Event, error)
}

// NewRelayFunctions that do nothing, to avoid panicking on a nil method.
func NewRelayFunctions() RelayFunctions {
	return RelayFunctions{
		OnConnect: func(c *Client) error { return nil },
		OnEvent:   func(c *Client, e *nostr.Event) error { return nil },
		OnFilters: func(ctx context.Context, c *Client, f nostr.Filters) ([]nostr.Event, error) { return nil, nil },
	}
}

type WebsocketLimits struct {
	WriteWait      time.Duration
	PongWait       time.Duration
	PingPeriod     time.Duration
	MaxMessageSize int64
}

func NewWebsocketLimits() WebsocketLimits {
	return WebsocketLimits{
		WriteWait:      DefaultWriteWait,
		PongWait:       DefaultPongWait,
		PingPeriod:     DefaultPingPeriod,
		MaxMessageSize: DefaultMaxMessageSize,
	}
}

func NewRelay() *Relay {
	r := &Relay{
		Address:         "localhost:3334",
		eventQueue:      make(chan *EventRequest, 1000),
		reqQueue:        make(chan *ReqRequest, 1000),
		clients:         make(map[*Client]bool, 100),
		register:        make(chan *Client, 10),
		unregister:      make(chan *Client, 10),
		RelayFunctions:  NewRelayFunctions(),
		WebsocketLimits: NewWebsocketLimits(),
	}

	r.RejectEvent = append(r.RejectEvent, BadID, BadSignature)
	return r
}

type Response = json.Marshaler

type OkResponse struct {
	ID     string
	Saved  bool
	Reason string
}

func (o OkResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{"OK", o.ID, o.Saved, o.Reason})
}

type ClosedResponse struct {
	ID     string
	Reason string
}

func (c ClosedResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"CLOSED", c.ID, c.Reason})
}

type EventResponse struct {
	ID    string
	Event *nostr.Event
}

func (e EventResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{"EVENT", e.ID, e.Event})
}

type EoseResponse struct {
	ID string
}

func (e EoseResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"EOSE", e.ID})
}

type NoticeResponse struct {
	Message string
}

func (n NoticeResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"NOTICE", n.Message})
}

// StartAndServe starts the relay, listens to the provided address and handles http requests.
// It's a blocking operation. Use Start if you don't want to listen and serve right away.
func (r *Relay) StartAndServe(ctx context.Context) error {
	go r.start(ctx)
	return http.ListenAndServe(r.Address, r)
}

// Start the relay in another goroutine, which can later be served using http.ListenAndServe.
// Customize its behaviour by writing OnConnect, OnEvent, OnFilters and other [RelayFunctions].
func (r *Relay) Start(ctx context.Context) {
	go r.start(ctx)
}

func (r *Relay) start(ctx context.Context) {
	defer r.kill()

	for {
		select {
		case <-ctx.Done():
			return

		case client := <-r.register:
			r.clients[client] = true
			if err := r.OnConnect(client); err != nil {
				client.send <- NoticeResponse{Message: err.Error()}
			}

		case client := <-r.unregister:
			if _, ok := r.clients[client]; ok {
				delete(r.clients, client)
				close(client.send)
			}

		case event := <-r.eventQueue:
			if err := r.OnEvent(event.client, event.Event); err != nil {
				event.client.send <- OkResponse{ID: event.Event.ID, Saved: false, Reason: err.Error()}
				continue
			}
			event.client.send <- OkResponse{ID: event.Event.ID, Saved: true}

			for client := range r.clients {
				if client == event.client {
					continue
				}

				if match, subID := client.matchesSubscription(event.Event); match {
					client.send <- EventResponse{ID: subID, Event: event.Event}
				}
			}

		case req := <-r.reqQueue:
			events, err := r.OnFilters(req.ctx, req.client, req.Filters)
			if err != nil {
				if req.ctx.Err() == nil {
					// the error was NOT caused by the user cancelling the request, so we send a CLOSE
					req.client.send <- ClosedResponse{ID: req.ID, Reason: err.Error()}
				}

				req.client.closeSubscription(req.ID)
				continue
			}

			for _, event := range events {
				req.client.send <- EventResponse{ID: req.ID, Event: &event}
			}

			req.client.send <- EoseResponse{ID: req.ID}
		}
	}
}

// kill sends a close response for each subscription of each client, and then closes all relay queues and channels.
func (r *Relay) kill() {
	log.Println("shutting down the relay...")
	defer log.Println("relay stopped")

	for client := range r.clients {
		for _, sub := range client.Subscriptions {
			client.send <- ClosedResponse{ID: sub.ID, Reason: "shutting down the relay"}
		}
	}

	close(r.register)
	close(r.unregister)
	close(r.eventQueue)
	close(r.reqQueue)
}

// ServeHTTP implements http.Handler interface, rejecting connections as specified
// and only handling WebSocket connections.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	for _, reject := range r.RejectConnection {
		if err := reject(req); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	if req.Header.Get("Upgrade") == "websocket" {
		r.HandleWebsocket(w, req)
		return
	}

	http.Error(w, "Expected WebSocket connection", http.StatusUpgradeRequired)
}

// HandleWebsocket upgrades the http request to a websocket, creates a [Client], and registers it with the [Relay].
// The client will then read and write to the websocket in two separate goroutines, preventing multiple readers/writers.
func (r *Relay) HandleWebsocket(w http.ResponseWriter, req *http.Request) {
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Printf("failed to upgrade to websocket: %v", err)
		return
	}

	client := &Client{IP: IP(req), relay: r, conn: conn, send: make(chan Response, 100)}
	r.register <- client

	go client.write()
	go client.read()
}

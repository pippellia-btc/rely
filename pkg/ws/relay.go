package ws

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

const (
	DefaultWriteWait           time.Duration = 10 * time.Second
	DefaultPongWait            time.Duration = 60 * time.Second
	DefaultPingPeriod          time.Duration = 45 * time.Second
	DefaultMaxMessageSize      int64         = 512000
	DefaultMaxFiltersPerClient int           = 20
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

type Relay struct {
	EventQueue chan *EventRequest
	ReqQueue   chan *ReqRequest

	Clients    map[*Client]bool
	Register   chan *Client
	Unregister chan *Client

	RelayFunctions
	ClientLimits
}

type RelayFunctions struct {
	// a filter is accepted iff. err is nil. WARNING: All functions MUST be thread-safe.
	RejectFilter []func(f *nostr.Filter) error
	Query        func(ctx context.Context, f *nostr.Filter) ([]nostr.Event, error)

	// an event is accepted iff. err is nil. WARNING: All functions MUST be thread-safe.
	RejectEvent []func(e *nostr.Event) error
	Save        func(e *nostr.Event) error
}

type ClientLimits struct {
	// websocket limits
	WriteWait      time.Duration
	PongWait       time.Duration
	PingPeriod     time.Duration
	MaxMessageSize int64

	// client limits
	MaxFiltersPerClient int
}

func NewClientLimits() ClientLimits {
	return ClientLimits{
		WriteWait:           DefaultWriteWait,
		PongWait:            DefaultPongWait,
		PingPeriod:          DefaultPingPeriod,
		MaxMessageSize:      DefaultMaxMessageSize,
		MaxFiltersPerClient: DefaultMaxFiltersPerClient,
	}
}

func NewRelay() *Relay {
	r := &Relay{
		EventQueue:   make(chan *EventRequest, 1000),
		ReqQueue:     make(chan *ReqRequest, 1000),
		Clients:      make(map[*Client]bool, 100),
		Register:     make(chan *Client, 10),
		Unregister:   make(chan *Client, 10),
		ClientLimits: NewClientLimits(),
	}

	r.RejectEvent = append(r.RejectEvent, BadID, BadSignature)
	return r
}

func (r *Relay) Run() {
	for {
		select {
		case client := <-r.Register:
			r.Clients[client] = true

		case client := <-r.Unregister:
			if _, ok := r.Clients[client]; ok {
				delete(r.Clients, client)
				close(client.Send)
			}

		case event := <-r.EventQueue:
			if err := r.Save(event.Event); err != nil {
				event.client.Send <- OkResponse{ID: event.Event.ID, Saved: false, Reason: err.Error()}
				break
			}

			event.client.Send <- OkResponse{ID: event.Event.ID, Saved: true}
			for client := range r.Clients {
				match, subID := client.MatchesSubscription(event.Event)
				if match {
					client.Send <- EventResponse{ID: subID, Event: event.Event}
				}
			}

		case req := <-r.ReqQueue:
			events := make([]nostr.Event, 0, len(req.Filters)) // conservative pre-allocation
			for _, filter := range req.Filters {
				new, err := r.Query(req.ctx, &filter)
				if err != nil {
					req.client.Send <- ClosedResponse{ID: req.ID, Reason: err.Error()}
					break
				}

				events = append(events, new...)
			}

			for _, event := range events {
				req.client.Send <- EventResponse{ID: req.ID, Event: &event}
			}

			req.client.Send <- EoseResponse{ID: req.ID}
		}
	}
}

// HandleWebsocket upgrades the http request to a websocket, creates a [Client], and registers it with the [Relay].
// The client will then read and write to the websocket in two separate goroutines, preventing multiple readers/writers.
func (r *Relay) HandleWebsocket(w http.ResponseWriter, req *http.Request) {
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Printf("failed to upgrate to websocket: %v", err)
		return
	}

	client := &Client{Relay: r, Conn: conn, Send: make(chan Response, 100)}
	r.Register <- client

	go client.Write()
	go client.Read()
}

// BadID returns an error if the event's ID is invalid
func BadID(e *nostr.Event) error {
	if !e.CheckID() {
		return ErrInvalidEventID
	}
	return nil
}

// BadSignature returns an error if the event's signature is invalid
func BadSignature(e *nostr.Event) error {
	match, err := e.CheckSignature()
	if !match {
		if err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidEventSignature, err.Error())
		}
		return ErrInvalidEventSignature
	}

	return nil
}

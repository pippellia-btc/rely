package rely

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
	EventQueue chan *EventRequest
	ReqQueue   chan *ReqRequest

	Clients    map[*Client]bool
	Register   chan *Client
	Unregister chan *Client

	RelayFunctions
	WebsocketLimits
}

type RelayFunctions struct {
	// a connection/event/filter is accepted if and only if err is nil.
	// WARNING: All functions MUST be thread-safe.
	RejectConnection []func(*http.Request) error
	RejectEvent      []func(*Client, *nostr.Event) error
	RejectFilters    []func(context.Context, *Client, nostr.Filters) error

	Query func(context.Context, *Client, nostr.Filters) ([]nostr.Event, error)
	Save  func(*Client, *nostr.Event) error
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
		EventQueue:      make(chan *EventRequest, 1000),
		ReqQueue:        make(chan *ReqRequest, 1000),
		Clients:         make(map[*Client]bool, 100),
		Register:        make(chan *Client, 10),
		Unregister:      make(chan *Client, 10),
		WebsocketLimits: NewWebsocketLimits(),
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
			if err := r.Save(event.client, event.Event); err != nil {
				event.client.Send <- OkResponse{ID: event.Event.ID, Saved: false, Reason: err.Error()}
				break
			}
			event.client.Send <- OkResponse{ID: event.Event.ID, Saved: true}

			for client := range r.Clients {
				if client == event.client {
					continue
				}

				if match, subID := client.MatchesSubscription(event.Event); match {
					client.Send <- EventResponse{ID: subID, Event: event.Event}
				}
			}

		case req := <-r.ReqQueue:
			events, err := r.Query(req.ctx, req.client, req.Filters)
			if err != nil {
				if req.ctx.Err() == nil {
					// the error was NOT caused by the user cancelling the request
					// so we close the subscription with the error message
					req.client.Send <- ClosedResponse{ID: req.ID, Reason: err.Error()}
				}
				break
			}

			for _, event := range events {
				req.client.Send <- EventResponse{ID: req.ID, Event: &event}
			}

			req.client.Send <- EoseResponse{ID: req.ID}
		}
	}
}

// ServeHTTP implements http.Handler interface, only handling WebSocket connections.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
		log.Printf("failed to upgrate to websocket: %v", err)
		return
	}

	client := &Client{Relay: r, Conn: conn, Send: make(chan Response, 100)}
	r.Register <- client
	log.Printf("registering client")

	go client.Write()
	go client.Read()
}

// BadID returns an error if the event's ID is invalid
func BadID(c *Client, e *nostr.Event) error {
	if !e.CheckID() {
		return ErrInvalidEventID
	}
	return nil
}

// BadSignature returns an error if the event's signature is invalid
func BadSignature(c *Client, e *nostr.Event) error {
	match, err := e.CheckSignature()
	if !match {
		if err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidEventSignature, err.Error())
		}
		return ErrInvalidEventSignature
	}

	return nil
}

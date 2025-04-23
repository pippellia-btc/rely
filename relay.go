package rely

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

var ErrOverloaded = errors.New("the relay is overloaded, please try again later")

const (
	DefaultWriteWait      time.Duration = 10 * time.Second
	DefaultPongWait       time.Duration = 60 * time.Second
	DefaultPingPeriod     time.Duration = 45 * time.Second
	DefaultMaxMessageSize int64         = 500000 // 0.5MB
)

type Relay struct {
	// the set of active clients
	clients        map[*Client]struct{}
	clientsCounter atomic.Int64

	// the channels used to register/unregister a client
	register   chan *Client
	unregister chan *Client

	// the queue for EVENTs and REQs
	queue chan Request

	// the last (unix) time a client registration failed due to the register channel being full
	lastRegistrationFail atomic.Int64

	RelayFunctions
	Websocket WebsocketOptions
}

// enqueue tries to add the request to the queue of the relay.
// If it's full, it return the error [ErrOverloaded]
func (r *Relay) enqueue(request Request) *RequestError {
	select {
	case r.queue <- request:
		return nil
	default:
		return &RequestError{ID: request.ID(), Err: ErrOverloaded}
	}
}

// RelayFunctions is a collection of functions the users of rely can customize.
type RelayFunctions struct {
	// a connection is accepted if and only if err is nil. All functions MUST be thread-safe.
	RejectConnection []func(Stats, *http.Request) error

	// an event is accepted if and only if err is nil. All functions MUST be thread-safe.
	RejectEvent []func(*Client, *nostr.Event) error

	// the filters are accepted if and only if err is nil. All functions MUST be thread-safe.
	RejectFilters []func(*Client, nostr.Filters) error

	// the action the relay performs after establishing a connection with the specified client.
	OnConnect func(*Client) error

	// the action the relay performs on an EVENT coming from the specified client
	OnEvent func(*Client, *nostr.Event) error

	// the action the relay performs on the filters coming from a REQ from the specified client
	OnFilters func(context.Context, *Client, nostr.Filters) ([]nostr.Event, error)
}

// NewRelayFunctions that only logs stuff, to avoid panicking on a nil method.
func NewRelayFunctions() RelayFunctions {
	return RelayFunctions{
		OnConnect: func(c *Client) error { return nil },
		OnEvent:   logEvent,
		OnFilters: logFilters,
	}
}

// Stats exposes relay statistics useful for rejecting connections during peaks of activity.
// All methods are thread-safe, and can be called from multiple goroutines.
type Stats interface {
	// Clients returns the number of active clients connected to the relay
	Clients() int

	// QueueLoad returns the ratio of queued requests to total capacity,
	// represented as a float between 0 and 1.
	QueueLoad() float64

	// LastRegistrationFail returns the last time a client failed to be added
	// to the registration queue, which happens during periods of high load.
	LastRegistrationFail() time.Time
}

func (r *Relay) Clients() int                    { return int(r.clientsCounter.Load()) }
func (r *Relay) QueueLoad() float64              { return float64(len(r.queue)) / float64(cap(r.queue)) }
func (r *Relay) LastRegistrationFail() time.Time { return time.Unix(r.lastRegistrationFail.Load(), 0) }

type WebsocketOptions struct {
	Upgrader       websocket.Upgrader
	WriteWait      time.Duration
	PongWait       time.Duration
	PingPeriod     time.Duration
	MaxMessageSize int64
}

func NewWebsocketOptions() WebsocketOptions {
	return WebsocketOptions{
		Upgrader:       websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024},
		WriteWait:      DefaultWriteWait,
		PongWait:       DefaultPongWait,
		PingPeriod:     DefaultPingPeriod,
		MaxMessageSize: DefaultMaxMessageSize,
	}
}

// NewRelay creates a new relay with default values and functions.
// In particular, the relay rejects connections in periods of high load, and rejects invalid nostr events.
// Customize its behaviour by writing OnConnect, OnEvent, OnFilters and other [RelayFunctions].
func NewRelay() *Relay {
	r := &Relay{
		queue:          make(chan Request, 10000),
		clients:        make(map[*Client]struct{}, 100),
		register:       make(chan *Client, 100),
		unregister:     make(chan *Client, 100),
		RelayFunctions: NewRelayFunctions(),
		Websocket:      NewWebsocketOptions(),
	}

	r.RejectConnection = append(r.RejectConnection, RecentFailure)
	r.RejectEvent = append(r.RejectEvent, InvalidID, InvalidSignature)
	return r
}

// StartAndServe starts the relay, listens to the provided address and handles http requests.
// It's a blocking operation, that stops only when the context get cancelled.
// Use [Relay.Start] if you don't want to listen and serve right away.
// Customize its behaviour by writing OnConnect, OnEvent, OnFilters and other [RelayFunctions].
func (r *Relay) StartAndServe(ctx context.Context, address string) error {
	r.Start(ctx)
	exitErr := make(chan error, 1)
	server := &http.Server{Addr: address, Handler: r}

	go func() {
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			// http.ErrServerClosed is fired when calling server.Shutdown
			exitErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			return err
		}

		return nil

	case err := <-exitErr:
		return err
	}
}

// Start the relay in a separate goroutine. The relay will later need to be served using http.ListenAndServe.
// Customize its behaviour by writing OnConnect, OnEvent, OnFilters and other [RelayFunctions].
func (r *Relay) Start(ctx context.Context) {
	go r.start(ctx)
}

func (r *Relay) start(ctx context.Context) {
	defer r.close()

	for {
		select {
		case <-ctx.Done():
			return

		case client := <-r.register:
			r.clients[client] = struct{}{}
			r.clientsCounter.Add(1)

			if err := r.OnConnect(client); err != nil {
				client.send(NoticeResponse{Message: err.Error()})
			}

		case client := <-r.unregister:
			delete(r.clients, client)
			close(client.toSend)

			// perform batch unregistration to prevent [Client.Disconnect] from getting stuck
			// on the channel send when many disconnections occur at the same time.
			n := int64(len(r.unregister))
			r.clientsCounter.Add(-1 - n)
			for range n {
				client = <-r.unregister
				delete(r.clients, client)
				close(client.toSend)
			}

		case request := <-r.queue:
			if _, ok := r.clients[request.From()]; !ok {
				// the client has been unregistered
				continue
			}

			switch request := request.(type) {
			case *EventRequest:
				err := r.OnEvent(request.client, request.Event)
				if err != nil {
					request.client.send(OkResponse{ID: request.ID(), Saved: false, Reason: err.Error()})
					continue
				}
				request.client.send(OkResponse{ID: request.ID(), Saved: true})

				// send the event to all connected clients whose subscriptions match it
				for client := range r.clients {
					if client == request.client {
						continue
					}

					if match, subID := client.matchesSubscription(request.Event); match {
						client.send(EventResponse{ID: subID, Event: request.Event})
					}
				}

			case *ReqRequest:
				events, err := r.OnFilters(request.ctx, request.client, request.Filters)
				if err != nil {
					if request.ctx.Err() == nil {
						// the error was NOT caused by the user cancelling the request, so we send a CLOSE
						request.client.send(ClosedResponse{ID: request.ID(), Reason: err.Error()})
					}

					request.client.closeSubscription(request.ID())
					continue
				}

				for _, event := range events {
					request.client.send(EventResponse{ID: request.ID(), Event: &event})
				}

				request.client.send(EoseResponse{ID: request.ID()})
			}
		}
	}
}

// close sends a close response for each subscription of each client.
func (r *Relay) close() {
	log.Println("shutting down the relay...")
	defer log.Println("relay stopped")

	for client := range r.clients {
		for _, sub := range client.subscriptions {
			client.send(ClosedResponse{ID: sub.ID, Reason: "shutting down the relay"})
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
	for _, reject := range r.RejectConnection {
		if err := reject(r, req); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	conn, err := r.Websocket.Upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Printf("failed to upgrade to websocket: %v", err)
		return
	}

	client := &Client{ip: IP(req), relay: r, conn: conn, toSend: make(chan Response, 100)}

	select {
	case r.register <- client:
		// successfully added client to registration queue, start reading and writing
		go client.write()
		go client.read()

	default:
		// if the registration queue is full, drop the connection to avoid overloading, and signal a failure
		r.lastRegistrationFail.Store(time.Now().Unix())
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "server is overloaded"))
		conn.Close()
	}
}

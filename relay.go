package rely

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

var (
	ErrOverloaded       = errors.New("the relay is overloaded, please try again later")
	ErrUnsupportedNIP45 = errors.New("NIP-45 COUNT is not supported")
)

const (
	DefaultWriteWait      time.Duration = 10 * time.Second
	DefaultPongWait       time.Duration = 60 * time.Second
	DefaultPingPeriod     time.Duration = 45 * time.Second
	DefaultMaxMessageSize int64         = 500000 // 0.5MB
	DefaultBufferSize     int           = 1024   // 1KB
)

type Relay struct {
	// the set of active clients
	clients      map[*client]struct{}
	clientsCount atomic.Int64

	// the channels used to register/unregister a client
	register   chan *client
	unregister chan *client

	// the queue for EVENTs and REQs
	queue chan request

	// the last (unix) time a client registration failed due to the register channel being full
	lastRegistrationFail atomic.Int64

	// domain is the relay domain name (e.g., "example.com") used to validate the NIP-42 "relay" tag.
	// It should be explicitly set with [WithDomain]; if unset, a warning will be logged and NIP-42 will fail.
	domain string
	websocketOptions

	RelayFunctions
}

// RelayFunctions is a collection of functions the users of rely can customize.
// These functions MUST not be changed when the relay is running.
type RelayFunctions struct {
	// a connection is accepted if and only if err is nil. All functions MUST be thread-safe.
	RejectConnection []func(Stats, *http.Request) error

	// an event is accepted if and only if err is nil. All functions MUST be thread-safe.
	RejectEvent []func(Client, *nostr.Event) error

	// the filters are accepted if and only if err is nil. All functions MUST be thread-safe.
	RejectReq []func(Client, nostr.Filters) error

	// the filters are accepted if and only if err is nil. All functions MUST be thread-safe.
	RejectCount []func(Client, nostr.Filters) error

	// the action the relay performs after establishing a connection with the specified client.
	OnConnect func(Client) error

	// the action the relay performs on an EVENT coming from the specified client.
	OnEvent func(Client, *nostr.Event) error

	// the action the relay performs on a REQ coming from the specified client.
	OnReq func(context.Context, Client, nostr.Filters) ([]nostr.Event, error)

	// the action the relay performs on a COUNT coming from the specified client.
	OnCount func(context.Context, Client, nostr.Filters) (count int64, approx bool, err error)
}

// NewRelayFunctions that only logs stuff, to avoid panicking on a nil method.
func newRelayFunctions() RelayFunctions {
	return RelayFunctions{
		RejectConnection: []func(Stats, *http.Request) error{RecentFailure},
		RejectEvent:      []func(Client, *nostr.Event) error{InvalidID, InvalidSignature},
		OnConnect:        func(Client) error { return nil },
		OnEvent:          logEvent,
		OnReq:            logFilters,
	}
}

// Stats exposes relay statistics useful for rejecting connections during peaks of activity. All methods are safe for concurrent use.
type Stats interface {
	// Clients returns the number of active clients connected to the relay.
	Clients() int

	// QueueLoad returns the ratio of queued requests to total capacity,
	// represented as a float between 0 and 1.
	QueueLoad() float64

	// LastRegistrationFail returns the last time a client failed to be added
	// to the registration queue, which happens during periods of high load.
	LastRegistrationFail() time.Time
}

func (r *Relay) Clients() int                    { return int(r.clientsCount.Load()) }
func (r *Relay) QueueLoad() float64              { return float64(len(r.queue)) / float64(cap(r.queue)) }
func (r *Relay) LastRegistrationFail() time.Time { return time.Unix(r.lastRegistrationFail.Load(), 0) }

type websocketOptions struct {
	upgrader       websocket.Upgrader
	writeWait      time.Duration
	pongWait       time.Duration
	pingPeriod     time.Duration
	maxMessageSize int64
}

func newWebsocketOptions() websocketOptions {
	return websocketOptions{
		upgrader:       websocket.Upgrader{ReadBufferSize: DefaultBufferSize, WriteBufferSize: DefaultBufferSize},
		writeWait:      DefaultWriteWait,
		pongWait:       DefaultPongWait,
		pingPeriod:     DefaultPingPeriod,
		maxMessageSize: DefaultMaxMessageSize,
	}
}

type Option func(*Relay)

func WithDomain(d string) Option       { return func(r *Relay) { r.domain = strings.TrimSpace(d) } }
func WithQueueCapacity(cap int) Option { return func(r *Relay) { r.queue = make(chan request, cap) } }

func WithReadBufferSize(s int) Option       { return func(r *Relay) { r.upgrader.ReadBufferSize = s } }
func WithWriteBufferSize(s int) Option      { return func(r *Relay) { r.upgrader.WriteBufferSize = s } }
func WithWriteWait(d time.Duration) Option  { return func(r *Relay) { r.writeWait = d } }
func WithPongWait(d time.Duration) Option   { return func(r *Relay) { r.pongWait = d } }
func WithPingPeriod(d time.Duration) Option { return func(r *Relay) { r.pingPeriod = d } }
func WithMaxMessageSize(s int64) Option     { return func(r *Relay) { r.maxMessageSize = s } }

// NewRelay creates a new Relay instance with sane defaults and customizable internal behavior.
// Customize its structure with functional options (e.g., [WithDomain], [WithPingPeriod]).
// Customize its behaviour by writing OnEvent, OnReq and other [RelayFunctions].
//
// Example:
//
//	relay := NewRelay(
//	    WithDomain("example.com"), // required for proper NIP-42 validation
//	    WithQueueCapacity(5000),
//	    WithPingPeriod(30 * time.Second),
//	)
func NewRelay(opts ...Option) *Relay {
	r := &Relay{
		queue:            make(chan request, 1000),
		clients:          make(map[*client]struct{}, 100),
		register:         make(chan *client, 100),
		unregister:       make(chan *client, 100),
		RelayFunctions:   newRelayFunctions(),
		websocketOptions: newWebsocketOptions(),
	}

	for _, opt := range opts {
		opt(r)
	}

	r.validate()
	return r
}

// enqueue tries to add the request to the queue of the relay.
// If it's full, it returns the error [ErrOverloaded]
func (r *Relay) enqueue(req request) *requestError {
	select {
	case r.queue <- req:
		return nil
	default:
		return &requestError{ID: req.ID(), Err: ErrOverloaded}
	}
}

// validate panics if structural relay parameters are invalid, and logs warnings
// for non-fatal but potentially misconfigured settings (e.g., missing domain).
func (r *Relay) validate() {
	if r.pingPeriod < 1*time.Second {
		panic("ping period must be greater than 1s")
	}

	if r.pongWait <= r.pingPeriod {
		panic("pong wait must be greater than ping period")
	}

	if r.writeWait <= 1*time.Second {
		panic("write wait must be greater than 1s")
	}

	if r.maxMessageSize < 512 {
		panic("max message size must be greater than 512 bytes to accept nostr events")
	}

	if r.domain == "" {
		log.Println("WARN: you must set the relay's domain to validate NIP-42 auth")
	}
}

// StartAndServe starts the relay, listens to the provided address and handles http requests.
// It's a blocking operation, that stops only when the context get cancelled.
// Use [Relay.Start] if you don't want to listen and serve right away.
// Customize its behaviour by writing OnConnect, OnEvent, OnReq and other [RelayFunctions].
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
// Customize its behaviour by writing OnConnect, OnEvent, OnReq and other [RelayFunctions].
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
			r.clientsCount.Add(1)

			if err := r.OnConnect(client); err != nil {
				client.send(noticeResponse{Message: err.Error()})
			}

		case client := <-r.unregister:
			delete(r.clients, client)
			close(client.toSend)

			// perform batch unregistration to prevent [client.Disconnect] from getting stuck
			// on the channel send when many disconnections occur at the same time.
			n := int64(len(r.unregister))
			r.clientsCount.Add(-1 - n)
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
			case *eventRequest:
				err := r.OnEvent(request.client, request.Event)
				if err != nil {
					request.client.send(okResponse{ID: request.ID(), Saved: false, Reason: err.Error()})
					continue
				}
				request.client.send(okResponse{ID: request.ID(), Saved: true})

				for client := range r.clients {
					for _, ID := range client.matchingSubscriptions(request.Event) {
						client.send(eventResponse{ID: ID, Event: request.Event})
					}
				}

			case *reqRequest:
				events, err := r.OnReq(request.ctx, request.client, request.Filters)
				if err != nil {
					if request.ctx.Err() == nil {
						// the error was NOT caused by the user cancelling the REQ, so we send a CLOSED
						request.client.send(closedResponse{ID: request.ID(), Reason: err.Error()})
					}

					request.client.closeSubscription(request.ID())
					continue
				}

				for _, event := range events {
					request.client.send(eventResponse{ID: request.ID(), Event: &event})
				}
				request.client.send(eoseResponse{ID: request.ID()})

			case *countRequest:
				count, approx, err := r.OnCount(request.ctx, request.client, request.Filters)
				if err != nil {
					if request.ctx.Err() == nil {
						// the error was NOT caused by the user cancelling the COUNT, so we send a CLOSED
						request.client.send(closedResponse{ID: request.ID(), Reason: err.Error()})
					}

					request.client.closeSubscription(request.ID())
					continue
				}

				request.client.send(countResponse{ID: request.ID(), Count: count, Approx: approx})
				request.client.closeSubscription(request.ID())
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
			client.send(closedResponse{ID: sub.ID, Reason: "shutting down the relay"})
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

// HandleWebsocket upgrades the http request to a websocket, creates a [client], and registers it with the [Relay].
// The client will then read and write to the websocket in two separate goroutines, preventing multiple readers/writers.
func (r *Relay) HandleWebsocket(w http.ResponseWriter, req *http.Request) {
	for _, reject := range r.RejectConnection {
		if err := reject(r, req); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	conn, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Printf("failed to upgrade to websocket: %v", err)
		return
	}

	client := &client{ip: IP(req), relay: r, conn: conn, toSend: make(chan response, 100)}

	select {
	case r.register <- client:
		// successfully added client to registration queue, start reading and writing
		go client.write()
		go client.read()

	default:
		// the registration queue is full, drop the connection to avoid overloading, and signal a failure
		r.lastRegistrationFail.Store(time.Now().Unix())
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "server is overloaded"))
		conn.Close()
	}
}

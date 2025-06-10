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

var (
	ErrOverloaded       = errors.New("the relay is overloaded, please try again later")
	ErrUnsupportedNIP45 = errors.New("NIP-45 COUNT is not supported")
)

type Relay struct {
	// the set of active clients
	clients      map[*client]struct{}
	clientsCount atomic.Int64

	// the channels used to register/unregister a client
	register   chan *client
	unregister chan *client

	// the last (unix) time a client registration failed due to the register channel being full
	lastRegistrationFail atomic.Int64

	// the channel used to broadcast events to all matching clients
	broadcast chan *nostr.Event

	// the queue for EVENTs, REQs and COUNTs
	queue chan request

	systemOptions
	websocketOptions
	RelayFunctions
}

// RelayFunctions is a collection of functions the users of rely can customize.
// These functions MUST not be changed when the relay is running, and MUST be thread-safe.
type RelayFunctions struct {
	// a connection is accepted if and only if all errs are nil.
	RejectConnection []func(Stats, *http.Request) error

	// an EVENT is accepted if and only if all errs are nil.
	RejectEvent []func(Client, *nostr.Event) error

	// a REQ is accepted if and only if all errs are nil.
	RejectReq []func(Client, nostr.Filters) error

	// a COUNT is accepted if and only if all errs are nil.
	RejectCount []func(Client, nostr.Filters) error

	// the actions to perform after establishing a connection with the specified client.
	OnConnect func(Client) error

	// the actions to perform on an EVENT coming from the specified client.
	OnEvent func(Client, *nostr.Event) error

	// the actions to perform on a REQ coming from the specified client.
	OnReq func(context.Context, Client, nostr.Filters) ([]nostr.Event, error)

	// the actions to perform on a COUNT coming from the specified client.
	OnCount func(context.Context, Client, nostr.Filters) (count int64, approx bool, err error)
}

func newRelayFunctions() RelayFunctions {
	return RelayFunctions{
		RejectConnection: []func(Stats, *http.Request) error{RecentFailure},
		RejectEvent:      []func(Client, *nostr.Event) error{InvalidID, InvalidSignature},
		OnConnect:        func(Client) error { return nil },
		OnEvent:          logEvent,
		OnReq:            logFilters,
	}
}

// Stats exposes relay statistics useful for rejecting connections during peaks of activity.
// All methods are safe for concurrent use.
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

// NewRelay creates a new Relay instance with sane defaults and customizable internal behavior.
// Customize its structure with functional options (e.g., [WithDomain], [WithQueueCapacity]).
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
		clients:    make(map[*client]struct{}, 100),
		register:   make(chan *client, 100),
		unregister: make(chan *client, 100),
		broadcast:  make(chan *nostr.Event, 1000),
		queue:      make(chan request, 1000),

		systemOptions:    newSystemOptions(),
		websocketOptions: newWebsocketOptions(),
		RelayFunctions:   newRelayFunctions(),
	}

	for _, opt := range opts {
		opt(r)
	}

	r.validate()
	return r
}

// enqueue tries to add the request to the queue of the relay.
// If it's full, it returns [ErrOverloaded]
func (r *Relay) enqueue(req request) *requestError {
	select {
	case r.queue <- req:
		return nil
	default:
		if r.logOverload {
			log.Printf("failed to enqueue request with ID %s: %v", req.ID(), ErrOverloaded)
		}
		return &requestError{ID: req.ID(), Err: ErrOverloaded}
	}
}

// Broadcast the event to all clients whose subscriptions match it.
func (r *Relay) Broadcast(e *nostr.Event) error {
	select {
	case r.broadcast <- e:
		return nil
	default:
		return ErrOverloaded
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
			exitErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		return server.Shutdown(ctx)

	case err := <-exitErr:
		return err
	}
}

// Start the relay in a separate goroutine. The relay will later need to be served using http.ListenAndServe.
// Customize its behaviour by writing OnConnect, OnEvent, OnReq and other [RelayFunctions].
func (r *Relay) Start(ctx context.Context) {
	go r.coordinator(ctx)
	go r.processor(ctx)
}

// Coordinate the registration and unregistration of clients, and the broadcasting of events.
func (r *Relay) coordinator(ctx context.Context) {
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
			for range n {
				client = <-r.unregister
				delete(r.clients, client)
				close(client.toSend)
			}

			r.clientsCount.Add(-1 - n)

		case event := <-r.broadcast:
			for client := range r.clients {
				for _, ID := range client.matchingSubscriptions(event) {
					client.send(eventResponse{ID: ID, Event: event})
				}
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

// Process the requests in the relay queue with [Relay.maxProcessors] processors,
// by appliying the user defined [RelayFunctions].
func (r *Relay) processor(ctx context.Context) {
	sem := make(chan struct{}, r.maxProcessors)

	for {
		select {
		case <-ctx.Done():
			return

		case request := <-r.queue:
			if request.IsExpired() {
				continue
			}

			sem <- struct{}{}
			go func() {
				r.process(request)
				<-sem
			}()
		}
	}
}

// process the request according to its type by using the provided [RelayFunctions].
func (r *Relay) process(request request) {
	switch request := request.(type) {
	case *eventRequest:
		err := r.OnEvent(request.client, request.Event)
		if err != nil {
			request.client.send(okResponse{ID: request.ID(), Saved: false, Reason: err.Error()})
			return
		}
		request.client.send(okResponse{ID: request.ID(), Saved: true})

		err = r.Broadcast(request.Event)
		if err != nil && r.logOverload {
			log.Printf("failed to broadcast event ID %s: %v", request.ID(), err)
		}

	case *reqRequest:
		events, err := r.OnReq(request.ctx, request.client, request.Filters)
		if err != nil {
			if request.ctx.Err() == nil {
				// the error was NOT caused by the user cancelling the REQ, so we send a CLOSED
				request.client.send(closedResponse{ID: request.ID(), Reason: err.Error()})
			}

			request.client.closeSubscription(request.ID())
			return
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
			return
		}

		request.client.send(countResponse{ID: request.ID(), Count: count, Approx: approx})
		request.client.closeSubscription(request.ID())
	}
}

// ServeHTTP implements the http.Handler interface, only handling WebSocket connections.
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
		go client.write()
		go client.read()

	default:
		r.lastRegistrationFail.Store(time.Now().Unix())
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "server is overloaded"))
		conn.Close()

		if r.logOverload {
			log.Println("failed to register client: channel is full")
		}
	}
}

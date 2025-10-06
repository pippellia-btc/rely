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
	clients              map[*client]struct{} // set of connected clients
	clientsCount         atomic.Int64         // number of connected clients
	lastRegistrationFail atomic.Int64         // last time a client registration failed

	register   chan *client
	unregister chan *client
	queue      chan request      // the queue for EVENTs, REQs and COUNTs
	broadcast  chan *nostr.Event // used to broadcast events to all matching clients

	RelayFunctions

	systemOptions
	websocketOptions
}

// RelayFunctions consist of two groups of functions that define the behavior of the relay.
//
//   - Reject functions:
//
//     These are used to conditionally reject incoming data, before they are processed.
//     Each slice is evaluated in order, and if any function returns an error, the input is rejected.
//
//   - On functions:
//
//     These define the actions to perform after a certain situation happened.
//     They allow you to hook into and customize how the relay reacts to connections, auths,
//     EVENTs, REQs, and COUNTs. If the function returns an error, the appropriate
//     message is sent to the client (e.g. onEvent -> OK, onReq -> CLOSE).
//
// All functions must be thread-safe and must not be modified at runtime.
type RelayFunctions struct {
	RejectConnection []func(Stats, *http.Request) error
	RejectEvent      []func(Client, *nostr.Event) error
	RejectReq        []func(Client, nostr.Filters) error
	RejectCount      []func(Client, nostr.Filters) error

	OnEvent func(Client, *nostr.Event) error
	OnReq   func(context.Context, Client, nostr.Filters) ([]nostr.Event, error)
	OnCount func(context.Context, Client, nostr.Filters) (count int64, approx bool, err error)

	OnConnect    func(Client)
	OnDisconnect func(Client)
	OnAuth       func(Client)

	// OnGreedyClient is called when the client’s response buffer is full,
	// which happens if the client sends new REQs before reading all responses from previous ones.
	OnGreedyClient func(Client)
}

func newRelayFunctions() RelayFunctions {
	return RelayFunctions{
		RejectConnection: []func(Stats, *http.Request) error{RegistrationFailWithin(time.Second)},
		RejectEvent:      []func(Client, *nostr.Event) error{InvalidID, InvalidSignature},

		OnConnect:      func(Client) {},
		OnDisconnect:   func(Client) {},
		OnAuth:         func(c Client) {},
		OnGreedyClient: DisconnectOnDrops(200),

		OnEvent: logEvent,
		OnReq:   logFilters,
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
		clients:    make(map[*client]struct{}, 1000),
		register:   make(chan *client, 1000),
		unregister: make(chan *client, 1000),
		broadcast:  make(chan *nostr.Event, 1000),
		queue:      make(chan request, 1000),

		RelayFunctions: newRelayFunctions(),

		systemOptions:    newSystemOptions(),
		websocketOptions: newWebsocketOptions(),
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
		if r.logPressure {
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
		if r.logPressure {
			log.Printf("failed to broadcast event with ID %s: %v", e.ID, ErrOverloaded)
		}
		return ErrOverloaded
	}
}

// StartAndServe starts the relay, listens to the provided address and handles http requests.
// It's a blocking operation, that stops only when the context gets cancelled.
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

// Start the relay in separate goroutines. The relay will later need to be served using http.ListenAndServe.
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
			r.OnConnect(client)

		case client := <-r.unregister:
			delete(r.clients, client)
			close(client.responses)
			r.OnDisconnect(client)

			// perform batch unregistration to prevent [client.Disconnect] from getting stuck
			// on the channel send when many disconnections occur at the same time.
			n := int64(len(r.unregister))
			for range n {
				client = <-r.unregister
				delete(r.clients, client)
				close(client.responses)
				r.OnDisconnect(client)
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
		for i := range client.subs {
			client.subs[i].cancel()
			client.send(closedResponse{ID: client.subs[i].ID, Reason: "shutting down the relay"})
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
		r.Broadcast(request.Event)

	case *reqRequest:
		budget := request.client.RemainingCapacity()
		ApplyBudget(budget, request.Filters...)

		events, err := r.OnReq(request.ctx, request.client, request.Filters)
		if err != nil {
			if request.ctx.Err() == nil {
				// error not caused by the user's CLOSE
				request.client.send(closedResponse{ID: request.ID(), Reason: err.Error()})
			}

			request.client.closeSubscription(request.ID())
			return
		}

		for i := range events {
			request.client.send(eventResponse{ID: request.ID(), Event: &events[i]})
		}
		request.client.send(eoseResponse{ID: request.ID()})

	case *countRequest:
		count, approx, err := r.OnCount(request.ctx, request.client, request.Filters)
		if err != nil {
			if request.ctx.Err() == nil {
				// error not caused by the user's CLOSE
				request.client.send(closedResponse{ID: request.ID(), Reason: err.Error()})
			}

			request.client.closeSubscription(request.ID())
			return
		}

		request.client.send(countResponse{ID: request.ID(), Count: count, Approx: approx})
		request.client.closeSubscription(request.ID())
	}
}

// ServeHTTP implements the [http.Handler] interface, handling WebSocket connections
// and NIP-11 Relay Information Document requests.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.Header.Get("Upgrade") == "websocket":
		r.ServeWS(w, req)

	case req.Header.Get("Accept") == "application/nostr+json":
		r.ServeNIP11(w)

	default:
		http.Error(w, "Unsupported request", http.StatusBadRequest)
	}
}

// ServeWS upgrades the http request to a websocket, creates a [client], and registers it with the [Relay].
// The client will then read and write to the websocket in two separate goroutines, preventing multiple readers/writers.
func (r *Relay) ServeWS(w http.ResponseWriter, req *http.Request) {
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

	client := &client{
		ip:        IP(req),
		relay:     r,
		conn:      conn,
		responses: make(chan response, r.responseLimit),
	}

	select {
	case r.register <- client:
		go client.write()
		go client.read()

	default:
		r.lastRegistrationFail.Store(time.Now().Unix())
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, ErrOverloaded.Error()))
		conn.Close()

		if r.logPressure {
			log.Printf("failed to register client %s: channel is full", client.ip)
		}
	}
}

// ServeNIP11 serves the NIP-11 relay information document.
func (r *Relay) ServeNIP11(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/nostr+json")
	w.WriteHeader(http.StatusOK)
	w.Write(r.info)
}

package rely

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
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
	uid    string
	nextID atomic.Int64
	wg     sync.WaitGroup // needed for graceful shutdown

	dispatcher *dispatcher

	lastRegistrationFail atomic.Int64

	register   chan *client
	unregister chan *client
	open       chan Subscription
	close      chan sID
	broadcast  chan *nostr.Event
	process    chan request

	Hooks
	systemOptions
	websocketOptions
}

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
		uid:              relayUID(),
		dispatcher:       newDispatcher(),
		register:         make(chan *client, 256),
		unregister:       make(chan *client, 256),
		open:             make(chan Subscription, 256),
		close:            make(chan sID, 256),
		broadcast:        make(chan *nostr.Event, 1024),
		process:          make(chan request, 1024),
		Hooks:            DefaultHooks(),
		systemOptions:    newSystemOptions(),
		websocketOptions: newWebsocketOptions(),
	}

	for _, opt := range opts {
		opt(r)
	}

	r.validate()
	return r
}

// StartAndServe starts the relay, listens to the provided address and handles http requests.
//
// It's a blocking operation, that stops only when the context gets cancelled.
// Use [Relay.Start] if you don't want to listen and serve right away, but then
// don't forget to wait for a graceful shutdown with [Relay.Wait].
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

		err := server.Shutdown(ctx)
		r.Wait()
		return err

	case err := <-exitErr:
		return err
	}
}

// Start the relay in separate goroutines in a non-blocking fashion.
//
// The relay will later need to be served using [http.ListenAndServe] or equivalent.
// See [Relay.StartAndServe] for an example on how to do it.
// For a proper shutdown process, you have to call [Relay.Wait] before closing your program.
func (r *Relay) Start(ctx context.Context) {
	r.wg.Add(2)
	go r.dispatchLoop(ctx)
	go r.processLoop(ctx)
}

// Wait blocks until the relay has shut down completely.
//
// This is useful only when you manually call [Relay.Start] instead of [Relay.StartAndServe]
// and need to wait for a graceful shutdown before the program exits.
func (r *Relay) Wait() {
	r.wg.Wait()
}

// The dispatchLoop coordinates the registration and unregistration of clients,
// and the broadcasting of events.
func (r *Relay) dispatchLoop(ctx context.Context) {
	defer r.wg.Done()

	for {
		select {
		case <-ctx.Done():
			r.shutdown()
			return

		case client := <-r.register:
			r.dispatcher.register(client)
			r.wg.Add(2)
			go client.read()
			go client.write()
			r.On.Connect(client)

		case client := <-r.unregister:
			r.dispatcher.unregister(client)
			r.On.Disconnect(client)

			// perform batch unregistration to prevent [client.Disconnect] from getting stuck
			// on the channel send when many disconnections occur at the same time.
			for range len(r.unregister) {
				client = <-r.unregister
				r.dispatcher.unregister(client)
				r.On.Disconnect(client)
			}

		case sub := <-r.open:
			r.dispatcher.open(sub)

		case subID := <-r.close:
			r.dispatcher.close(subID)

		case event := <-r.broadcast:
			r.dispatcher.broadcast(event)
		}
	}
}

// Shutdown correctly unregisters all connected clients for a safe shutdown.
func (r *Relay) shutdown() {
	log.Println("shutting down the relay...")
	defer log.Println("relay stopped")

	// Drain clients from the registering queue, making their respective
	// [client.read] and [client.write] return. It's assumed that it's no longer
	// possible for clients to be sent to the registering queue
drainRegister:
	for {
		select {
		case client := <-r.register:
			if client.isUnregistering.CompareAndSwap(false, true) {
				close(client.done)
			}
		default:
			break drainRegister
		}
	}

	// Remove clients in the map not in the unregistering queue.
	for client := range r.dispatcher.clients {
		if client.isUnregistering.CompareAndSwap(false, true) {
			close(client.done)
			r.dispatcher.unregister(client)
		}
	}

	// Drain clients from the unregistering queue. This step must be done last because
	// clients can end up in this queue on their own, and the [client.Disconnect] is blocking
drainUnregister:
	for {
		select {
		case client := <-r.unregister:
			delete(r.dispatcher.clients, client)
			r.dispatcher.stats.clients.Add(-1)
		default:
			break drainUnregister
		}
	}

	// Drain subscriptions because some clients, or the [Relay.processor]
	// might be blocked because the channel send to close is blocking
drainSubscriptions:
	for {
		select {
		case <-r.close:
		default:
			break drainSubscriptions
		}
	}
}

// The respondLoop process the requests with [Relay.maxProcessors] processors,
// by appliying the user defined [Hooks].
func (r *Relay) processLoop(ctx context.Context) {
	defer r.wg.Done()

	sem := make(chan struct{}, r.maxProcessors)

	for {
		select {
		case <-ctx.Done():
			return

		case request := <-r.process:
			if request.IsExpired() {
				continue
			}

			sem <- struct{}{}
			go func() {
				r.processOne(request)
				<-sem
			}()
		}
	}
}

// ProcessOne request according to its type by using the provided [Hooks].
func (r *Relay) processOne(request request) {
	ID := request.ID()
	switch request := request.(type) {
	case *eventRequest:
		err := r.On.Event(request.client, request.Event)
		if err != nil {
			request.client.send(okResponse{ID: ID, Saved: false, Reason: err.Error()})
			return
		}

		request.client.send(okResponse{ID: ID, Saved: true})
		r.Broadcast(request.Event)

	case *reqRequest:
		budget := request.client.RemainingCapacity()
		ApplyBudget(budget, request.Filters...)

		events, err := r.On.Req(request.ctx, request.client, request.Filters)
		if err != nil {
			if request.ctx.Err() == nil {
				// error not caused by the user's CLOSE
				request.client.send(closedResponse{ID: ID, Reason: err.Error()})
			}

			r.close <- sID(request.UID())
			return
		}

		for i := range events {
			request.client.send(eventResponse{ID: ID, Event: &events[i]})
		}
		request.client.send(eoseResponse{ID: ID})

	case *countRequest:
		count, approx, err := r.On.Count(request.ctx, request.client, request.Filters)
		if err != nil {
			if request.ctx.Err() == nil {
				// error not caused by the user's CLOSE
				request.client.send(closedResponse{ID: ID, Reason: err.Error()})
			}

			r.close <- sID(request.UID())
			return
		}

		request.client.send(countResponse{ID: ID, Count: count, Approx: approx})
		r.close <- sID(request.UID())
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

// tryProcess tries to add the request to the processing queue of the relay.
// If it's full, it returns [ErrOverloaded] inside the [requestError]
func (r *Relay) tryProcess(rq request) *requestError {
	select {
	case r.process <- rq:
		return nil
	default:
		if r.logPressure {
			log.Printf("failed to enqueue request %s: %v", rq.UID(), ErrOverloaded)
		}
		return &requestError{ID: rq.ID(), Err: ErrOverloaded}
	}
}

// tryOpen tries to add the subscription to the open subscription queue of the relay.
// If it's full, it returns [ErrOverloaded] inside the [requestError]
func (r *Relay) tryOpen(s Subscription) *requestError {
	select {
	case r.open <- s:
		return nil
	default:
		if r.logPressure {
			log.Printf("failed to open subscription %s: %v", s.UID(), ErrOverloaded)
		}
		return &requestError{ID: s.ID, Err: ErrOverloaded}
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
	for _, reject := range r.Reject.Connection {
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
		id:        r.assignID(),
		ip:        IP(req),
		auther:    auther{domain: r.domain},
		relay:     r,
		conn:      conn,
		responses: make(chan response, r.responseLimit),
		done:      make(chan struct{}),
	}

	select {
	case r.register <- client:

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

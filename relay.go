package rely

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

var (
	ErrShuttingDown     = errors.New("the relay is shutting down, please try again later")
	ErrOverloaded       = errors.New("the relay is overloaded, please try again later")
	ErrUnsupportedNIP45 = errors.New("NIP-45 COUNT is not supported")
)

type Relay struct {
	nextClient atomic.Int64
	wg         sync.WaitGroup // for waiting for client's goroutines
	done       chan struct{}
	log        *slog.Logger

	dispatcher *dispatcher

	lastRegistrationFail atomic.Int64

	register   chan *client
	unregister chan *client
	open       chan subscription
	close      chan sID
	viewSubs   chan subRequest
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
		dispatcher:       newDispatcher(),
		register:         make(chan *client, 256),
		unregister:       make(chan *client, 256),
		open:             make(chan subscription, 256),
		close:            make(chan sID, 256),
		broadcast:        make(chan *nostr.Event, 1024),
		viewSubs:         make(chan subRequest, 256),
		process:          make(chan request, 1024),
		Hooks:            DefaultHooks(),
		systemOptions:    newSystemOptions(),
		websocketOptions: newWebsocketOptions(),
		done:             make(chan struct{}),
		log:              slog.Default(),
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
		r.log.Info("serving the relay", "address", address)
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
	r.log.Info("starting up the relay")
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

		case request := <-r.viewSubs:
			request.reply <- r.dispatcher.viewSubs(request.client)
		}
	}
}

// Shutdown correctly unregisters all connected clients for a safe shutdown.
func (r *Relay) shutdown() {
	r.log.Info("shutting down the relay...")
	defer r.log.Info("relay stopped")

	// closing the done channel stops [Relay.ServeHTTP] from registering new clients.
	close(r.done)

	// Close the websocket connections of clients yet to be registered.
drainRegister:
	for {
		select {
		case client := <-r.register:
			client.writeCloseGoingAway()
			client.conn.Close()
		default:
			break drainRegister
		}
	}

	// Remove clients in the map not in the unregistering queue.
	// It's important to distinguish to avoid double closing the client.done.
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
			r.dispatcher.unregister(client)
		default:
			break drainUnregister
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

			r.closeSubscription(request.UID())
			return
		}

		for i := range events {
			request.client.send(eventResponse{ID: ID, Event: &events[i]})
		}
		request.client.send(eoseResponse{ID: ID})

	case *countRequest:
		count, approx, err := r.On.Count(request.client, request.Filters)
		if err != nil {
			request.client.send(closedResponse{ID: ID, Reason: err.Error()})
			return
		}

		request.client.send(countResponse{ID: ID, Count: count, Approx: approx})
		r.closeSubscription(request.UID())
	}
}

// Broadcast the event to all clients whose subscriptions match it.
func (r *Relay) Broadcast(e *nostr.Event) error {
	select {
	case r.broadcast <- e:
		return nil
	case <-r.done:
		return ErrShuttingDown
	default:
		r.log.Warn("failed to broadcast event", "id", e.ID, "error", ErrOverloaded)
		return ErrOverloaded
	}
}

// tryProcess tries to add the request to the processing queue of the relay.
// If it's full, it returns [ErrOverloaded] inside the [requestError]
func (r *Relay) tryProcess(rq request) *requestError {
	select {
	case r.process <- rq:
		return nil
	case <-r.done:
		return &requestError{ID: rq.ID(), Err: ErrShuttingDown}
	default:
		r.log.Warn("failed to enqueue request", "uid", rq.UID(), "error", ErrOverloaded)
		return &requestError{ID: rq.ID(), Err: ErrOverloaded}
	}
}

// tryOpen tries to add the subscription to the open subscription queue of the relay.
// If it's full, it returns [ErrOverloaded] inside the [requestError]
func (r *Relay) tryOpen(s subscription) *requestError {
	select {
	case r.open <- s:
		return nil
	case <-r.done:
		return &requestError{ID: s.id, Err: ErrShuttingDown}
	default:
		r.log.Warn("failed to open subscription", "uid", s.UID(), "error", ErrOverloaded)
		return &requestError{ID: s.id, Err: ErrOverloaded}
	}
}

// closeSubscription wait until it's able to send in the closing subscriptions queue,
// or the relay shuts down. It's blocking because failure to enqueue implies a memory leak.
func (r *Relay) closeSubscription(sid string) {
	select {
	case r.close <- sID(sid):
		return
	case <-r.done:
		return
	}
}

// ServeHTTP implements the [http.Handler] interface, handling WebSocket connections
// and NIP-11 Relay Information Document requests.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	select {
	case <-r.done:
		http.Error(w, ErrShuttingDown.Error(), http.StatusServiceUnavailable)
		return
	default:
		// proceed
	}

	for _, reject := range r.Reject.Connection {
		if err := reject(r, req); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

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
	conn, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		r.log.Error("failed to upgrade to websocket", "error", err)
		return
	}

	client := &client{
		uid:         r.assignID(),
		ip:          IP(req),
		connectedAt: time.Now(),
		auth:        authState{domain: r.domain},
		relay:       r,
		conn:        conn,
		responses:   make(chan response, r.responseLimit),
		done:        make(chan struct{}),
	}

	select {
	case r.register <- client:

	case <-r.done:
		client.writeCloseGoingAway()
		client.conn.Close()

	default:
		r.lastRegistrationFail.Store(time.Now().Unix())
		client.writeCloseTryLater()
		client.conn.Close()
		r.log.Warn("failed to register client", "ip", client.ip, "error", "channel is full")
	}
}

// ServeNIP11 serves the NIP-11 relay information document.
func (r *Relay) ServeNIP11(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/nostr+json")
	w.WriteHeader(http.StatusOK)
	w.Write(r.info)
}

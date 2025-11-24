package rely

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/smallset"
)

var (
	ErrShuttingDown     = errors.New("the relay is shutting down, please try again later")
	ErrOverloaded       = errors.New("the relay is overloaded, please try again later")
	ErrUnsupportedNIP45 = errors.New("NIP-45 COUNT is not supported")
)

// Relay is the fundamental structure of the rely package, acting as an orchestrator
// for the other specialized actors in the system.
// Its main responsabilities are to register and unregister [clients], and route
// work to the specialized actors like [dispatcher] and [processor].
type Relay struct {
	clients    map[*client]struct{}
	register   chan *client
	unregister chan *client

	dispatcher *dispatcher
	processor  *processor
	stats

	log *slog.Logger

	Hooks
	systemSettings
	websocketSettings

	wg   sync.WaitGroup
	done chan struct{}
}

// NewRelay creates a new Relay instance with sane defaults and customizable internal behavior.
// Customize its structure with functional options (e.g., [WithDomain], [WithQueueCapacity]).
// Customize its behaviour by defining On.Event, On.Req and other [Hooks].
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
		clients:           make(map[*client]struct{}, 1000),
		register:          make(chan *client, 256),
		unregister:        make(chan *client, 256),
		log:               slog.Default(),
		Hooks:             DefaultHooks(),
		systemSettings:    newSystemSettings(),
		websocketSettings: newWebsocketSettings(),
		done:              make(chan struct{}),
	}

	r.dispatcher = newDispatcher(r)
	r.processor = newProcessor(r)

	for _, opt := range opts {
		opt(r)
	}

	r.validate()
	return r
}

// Broadcast the event to all clients whose subscriptions match it.
func (r *Relay) Broadcast(e *nostr.Event) error {
	select {
	case r.dispatcher.broadcast <- e:
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
	case r.processor.queue <- rq:
		return nil
	case <-r.done:
		return &requestError{ID: rq.ID(), Err: ErrShuttingDown}
	default:
		r.log.Warn("failed to enqueue request", "uid", rq.UID(), "error", ErrOverloaded)
		return &requestError{ID: rq.ID(), Err: ErrOverloaded}
	}
}

// Index sends the indexing update of subscription to the dispatcher.
func (r *Relay) index(s subscription) {
	select {
	case r.dispatcher.updates <- update{operation: index, sub: s}:
		return
	case <-r.done:
		return
	}
}

// Unindex sends the unindexing update of subscription to the dispatcher.
func (r *Relay) unindex(s subscription) {
	select {
	case r.dispatcher.updates <- update{operation: unindex, sub: s}:
		return
	case <-r.done:
		return
	}
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
	r.wg.Add(3)
	go r.run(ctx)
	go r.dispatcher.Run()
	go r.processor.Run()
}

// Wait blocks until the relay has shut down completely.
//
// This is useful only when you manually call [Relay.Start] instead of [Relay.StartAndServe]
// and need to wait for a graceful shutdown before the program exits.
func (r *Relay) Wait() {
	r.wg.Wait()
}

// Run syncronizes access to the clients map. It performs:
//   - client registration
//   - client unregistration
//   - shutdown when the context is cancelled
func (r *Relay) run(ctx context.Context) {
	defer func() {
		r.shutdown()
		r.wg.Done()
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case client := <-r.register:
			r.clients[client] = struct{}{}
			r.stats.clients.Add(1)

			r.wg.Add(2)
			go client.read()
			go client.write()
			r.On.Connect(client)

		case client := <-r.unregister:
			delete(r.clients, client)
			r.stats.clients.Add(-1)
			r.On.Disconnect(client)

			// perform batch unregistration to prevent [client.Disconnect] from getting stuck
			// on the channel send when many disconnections occur at the same time.
			for range len(r.unregister) {
				client = <-r.unregister
				delete(r.clients, client)
				r.stats.clients.Add(-1)
				r.On.Disconnect(client)
			}
		}
	}
}

// Shutdown correctly unregisters all connected clients for a safe shutdown.
func (r *Relay) shutdown() {
	r.log.Info("shutting down the relay...")
	defer r.log.Info("relay stopped")

	// Closing the done channel stops [Relay.ServeHTTP] from registering new clients.
	// It also signals the [dispatcher.Run] and [processor.Run] to return.
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
	for client := range r.clients {
		if client.isUnregistering.CompareAndSwap(false, true) {
			close(client.done)
			r.stats.clients.Add(-1)
		}
	}

	// Drain clients from the unregistering queue. This step must be done last because
	// clients can end up in this queue on their own, and the [client.Disconnect] is blocking
drainUnregister:
	for {
		select {
		case <-r.unregister:
			r.stats.clients.Add(-1)
		default:
			break drainUnregister
		}
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
func (r *Relay) ServeWS(w http.ResponseWriter, req *http.Request) {
	conn, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		r.log.Error("failed to upgrade to websocket", "error", err)
		return
	}

	client := &client{
		subs: make(map[string]subscription, 10),
		auth: authState{
			pubkeys:    smallset.New[string](10),
			maxPubkeys: 64,
			domain:     r.domain,
		},
		uid:         r.assignID(),
		ip:          IP(req),
		connectedAt: time.Now(),
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
		r.stats.lastRegistrationFail.Store(time.Now().Unix())
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

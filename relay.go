package rely

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
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
	nextID               atomic.Int64
	clients              map[*client]UIDs
	clientsCount         atomic.Int64
	lastRegistrationFail atomic.Int64

	subscriptions      map[UID]Subscription
	subscriptionsCount atomic.Int64
	filtersCount       atomic.Int64
	wg                 sync.WaitGroup // needed for graceful shutdown

	registerClient    chan *client
	unregisterClient  chan *client
	openSubscription  chan Subscription
	closeSubscription chan UID

	broadcastEvent chan *nostr.Event // used to broadcast events to all matching clients
	queue          chan request      // the queue for EVENTs, REQs and COUNTs

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
		RejectConnection: []func(Stats, *http.Request) error{RegistrationFailWithin(3 * time.Second)},
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

	// Subscriptions returns the number of active subscriptions.
	Subscriptions() int

	// Filters returns the number of active filters of REQ subscriptions.
	Filters() int

	// TotalConnections returns the total number of connections since the relay startup.
	TotalConnections() int

	// QueueLoad returns the ratio of queued requests to total capacity,
	// represented as a float between 0 and 1.
	QueueLoad() float64

	// LastRegistrationFail returns the last time a client failed to be added
	// to the registration queue, which happens during periods of high load.
	LastRegistrationFail() time.Time
}

func (r *Relay) Clients() int                    { return int(r.clientsCount.Load()) }
func (r *Relay) Subscriptions() int              { return int(r.subscriptionsCount.Load()) }
func (r *Relay) Filters() int                    { return int(r.filtersCount.Load()) }
func (r *Relay) TotalConnections() int           { return int(r.nextID.Load()) }
func (r *Relay) QueueLoad() float64              { return float64(len(r.queue)) / float64(cap(r.queue)) }
func (r *Relay) LastRegistrationFail() time.Time { return time.Unix(r.lastRegistrationFail.Load(), 0) }

func (r *Relay) assignID() string { return strconv.FormatInt(r.nextID.Add(1), 10) }

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
		clients:           make(map[*client]UIDs, 1000),
		subscriptions:     make(map[UID]Subscription, 3000),
		registerClient:    make(chan *client, 1000),
		unregisterClient:  make(chan *client, 1000),
		openSubscription:  make(chan Subscription, 1000),
		closeSubscription: make(chan UID, 1000),
		broadcastEvent:    make(chan *nostr.Event, 1000),
		queue:             make(chan request, 1000),

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

// Broadcast the event to all clients whose subscriptions match it.
func (r *Relay) Broadcast(e *nostr.Event) error {
	select {
	case r.broadcastEvent <- e:
		return nil
	default:
		if r.logPressure {
			log.Printf("failed to broadcast event with ID %s: %v", e.ID, ErrOverloaded)
		}
		return ErrOverloaded
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
// The relay will later need to be served using [http.ListenAndServe] or equivalent;
// see [Relay.StartAndServe] for an example on how to do it.
// For a proper shutdown process, you have to call [Relay.Wait] before closing your program.
func (r *Relay) Start(ctx context.Context) {
	r.wg.Add(2)
	go r.coordinator(ctx)
	go r.processor(ctx)
}

// Wait blocks until the relay has shut down completely.
//
// This is useful only when you manually call [Relay.Start] and need to wait for a
// graceful shutdown before the program exits.
// The shutdown process is initiated by cancelling the context passed to [Relay.Start].
func (r *Relay) Wait() {
	r.wg.Wait()
}

// Coordinate the registration and unregistration of clients, and the broadcasting of events.
func (r *Relay) coordinator(ctx context.Context) {
	defer r.wg.Done()

	for {
		select {
		case <-ctx.Done():
			r.shutdown()
			return

		case client := <-r.registerClient:
			r.register(client)

		case client := <-r.unregisterClient:
			r.unregister(client)

			// perform batch unregistration to prevent [client.Disconnect] from getting stuck
			// on the channel send when many disconnections occur at the same time.
			for range len(r.unregisterClient) {
				client = <-r.unregisterClient
				r.unregister(client)
			}

		case sub := <-r.openSubscription:
			r.open(sub)

		case subID := <-r.closeSubscription:
			r.close(subID)

		case event := <-r.broadcastEvent:
			r.broadcast(event)
		}
	}
}

func (r *Relay) register(c *client) {
	// avoid rare case when a subscription is opened before client registration
	if _, exists := r.clients[c]; !exists {
		r.clients[c] = make([]UID, 0, 5)
	}
	r.clientsCount.Add(1)
}

func (r *Relay) unregister(c *client) {
	uids, exists := r.clients[c]
	if exists {
		delete(r.clients, c)
		r.clientsCount.Add(-1)

		for _, uid := range uids {
			sub := r.subscriptions[uid]
			sub.cancel()
			delete(r.subscriptions, uid)

			r.subscriptionsCount.Add(-1)
			r.filtersCount.Add(-int64(len(sub.Filters)))
		}
	}
}

func (r *Relay) open(new Subscription) {
	if new.client.isUnregistering.Load() {
		return
	}

	uid := new.UID()
	old, exists := r.subscriptions[uid]
	if exists {
		old.cancel()
		r.subscriptions[uid] = new

		delta := int64(len(new.Filters) - len(old.Filters))
		r.filtersCount.Add(delta)
		return
	}

	r.subscriptions[uid] = new
	r.clients[new.client] = append(r.clients[new.client], uid)

	r.subscriptionsCount.Add(1)
	r.filtersCount.Add(int64(len(new.Filters)))
	// TODO: add to inverted indexes later
}

func (r *Relay) close(uid UID) {
	sub, exists := r.subscriptions[uid]
	if exists {
		sub.cancel()
		delete(r.subscriptions, uid)
		r.clients[sub.client] = remove(r.clients[sub.client], uid)

		r.subscriptionsCount.Add(-1)
		r.filtersCount.Add(-int64(len(sub.Filters)))
		// TODO: remove from inverted indexes later
	}
}

func (r *Relay) broadcast(e *nostr.Event) {
	// TODO: implement efficient candidate search with inverted indexes later
	for _, sub := range r.subscriptions {
		if sub.typ == "REQ" && sub.Matches(e) {
			sub.client.send(eventResponse{ID: sub.ID, Event: e})
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
		case client := <-r.registerClient:
			if client.isUnregistering.CompareAndSwap(false, true) {
				close(client.done)
			}
		default:
			break drainRegister
		}
	}

	// Remove clients in the map not in the unregistering queue.
	for client := range r.clients {
		if client.isUnregistering.CompareAndSwap(false, true) {
			close(client.done)
			r.unregister(client)
		}
	}

	// Drain clients from the unregistering queue. This step must be done last because
	// clients can end up in this queue on their own, and the [client.Disconnect] is blocking
drainUnregister:
	for {
		select {
		case client := <-r.unregisterClient:
			delete(r.clients, client)
			r.clientsCount.Add(-1)
		default:
			break drainUnregister
		}
	}

	// Drain subscriptions because some clients, or the [Relay.processor]
	// might be blocked because the channel send to closeSubscription is blocking
drainSubscriptions:
	for {
		select {
		case <-r.closeSubscription:
		default:
			break drainSubscriptions
		}
	}
}

// Process the requests in the relay queue with [Relay.maxProcessors] processors,
// by appliying the user defined [RelayFunctions].
func (r *Relay) processor(ctx context.Context) {
	defer r.wg.Done()

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
	ID := request.ID()
	switch request := request.(type) {
	case *eventRequest:
		err := r.OnEvent(request.client, request.Event)
		if err != nil {
			request.client.send(okResponse{ID: ID, Saved: false, Reason: err.Error()})
			return
		}

		request.client.send(okResponse{ID: ID, Saved: true})
		r.Broadcast(request.Event)

	case *reqRequest:
		budget := request.client.RemainingCapacity()
		ApplyBudget(budget, request.Filters...)

		events, err := r.OnReq(request.ctx, request.client, request.Filters)
		if err != nil {
			if request.ctx.Err() == nil {
				// error not caused by the user's CLOSE
				request.client.send(closedResponse{ID: ID, Reason: err.Error()})
			}

			r.closeSubscription <- request.UID()
			return
		}

		for i := range events {
			request.client.send(eventResponse{ID: ID, Event: &events[i]})
		}
		request.client.send(eoseResponse{ID: ID})

	case *countRequest:
		count, approx, err := r.OnCount(request.ctx, request.client, request.Filters)
		if err != nil {
			if request.ctx.Err() == nil {
				// error not caused by the user's CLOSE
				request.client.send(closedResponse{ID: ID, Reason: err.Error()})
			}

			r.closeSubscription <- request.UID()
			return
		}

		request.client.send(countResponse{ID: ID, Count: count, Approx: approx})
		r.closeSubscription <- request.UID()
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
		id:        r.assignID(),
		ip:        IP(req),
		auther:    auther{domain: r.domain},
		relay:     r,
		conn:      conn,
		responses: make(chan response, r.responseLimit),
		done:      make(chan struct{}),
	}

	select {
	case r.registerClient <- client:
		r.wg.Add(2)
		go client.write()
		go client.read()
		r.OnConnect(client)

	default:
		r.lastRegistrationFail.Store(time.Now().Unix())
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, ErrOverloaded.Error()))
		conn.Close()

		if r.logPressure {
			log.Printf("failed to registerClient client %s: channel is full", client.ip)
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

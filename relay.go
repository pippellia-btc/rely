package rely

import (
	"context"
	"errors"
	"log"
	"net/http"
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
	clients map[*Client]struct{}

	// the channel used to register/unregister a client
	register   chan *Client
	unregister chan *Client

	// the queue of EVENTs to be processed
	eventQueue chan *EventRequest

	// the queue of REQs to be processed
	reqQueue chan *ReqRequest

	// the last time a client registration failed due to the register channel being full
	lastRegistrationFail time.Time

	RelayFunctions
	Websocket WebsocketOptions
}

func (r *Relay) enqueueEvent(event *EventRequest) *RequestError {
	select {
	case r.eventQueue <- event:
		return nil
	default:
		return &RequestError{ID: event.Event.ID, Err: ErrOverloaded}
	}
}

func (r *Relay) enqueueReq(req *ReqRequest) *RequestError {
	select {
	case r.reqQueue <- req:
		return nil
	default:
		return &RequestError{ID: req.ID, Err: ErrOverloaded}
	}
}

// RelayFunctions is a collection of functions the user of this framework can customize.
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

// Stats exposes relay statistics useful for rejecting connections
type Stats interface {
	Clients() int
	LastRegistrationFail() time.Time

	ReqQueueLoad() float64
	EventQueueLoad() float64
}

func (r *Relay) Clients() int                    { return len(r.clients) }
func (r *Relay) LastRegistrationFail() time.Time { return r.lastRegistrationFail }
func (r *Relay) ReqQueueLoad() float64           { return float64(len(r.reqQueue)) / float64(cap(r.reqQueue)) }
func (r *Relay) EventQueueLoad() float64 {
	return float64(len(r.eventQueue)) / float64(cap(r.eventQueue))
}

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
func NewRelay() *Relay {
	r := &Relay{
		eventQueue:     make(chan *EventRequest, 10000),
		reqQueue:       make(chan *ReqRequest, 10000),
		clients:        make(map[*Client]struct{}, 1000),
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
// Use Start if you don't want to listen and serve right away.
// Customize its behaviour by writing OnConnect, OnEvent, OnFilters and other [RelayFunctions].
func (r *Relay) StartAndServe(ctx context.Context, address string) error {
	r.Start(ctx)
	server := &http.Server{Addr: address, Handler: r}
	exitErr := make(chan error, 1)

	go func() {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			// this error is the normal termination of the program
			err = nil
		}

		exitErr <- err
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}

	return <-exitErr
}

// Start the relay in another goroutine, which can later be served using http.ListenAndServe.
// Customize its behaviour by writing OnConnect, OnEvent, OnFilters and other [RelayFunctions].
func (r *Relay) Start(ctx context.Context) {
	go r.start(ctx)
}

func (r *Relay) start(ctx context.Context) {
	defer r.kill()

	for {
		select {
		case <-ctx.Done():
			return

		case client := <-r.register:
			r.clients[client] = struct{}{}
			if err := r.OnConnect(client); err != nil {
				client.send(NoticeResponse{Message: err.Error()})
			}

		case client := <-r.unregister:
			if _, ok := r.clients[client]; ok {
				delete(r.clients, client)
				close(client.toSend)
			}

			// perform batch flushing to unregister as many clients as possible,
			// which is important to avoid the [Client.read] to get stuck on the
			// channel send in the defer when many disconnections occur at the same time.
			n := len(r.unregister)
			for i := 0; i < n; i++ {
				client = <-r.unregister
				if _, ok := r.clients[client]; ok {
					delete(r.clients, client)
					close(client.toSend)
				}
			}

		case event := <-r.eventQueue:
			if _, ok := r.clients[event.client]; !ok {
				// the client has been unregistered
				continue
			}

			if err := r.OnEvent(event.client, event.Event); err != nil {
				event.client.send(OkResponse{ID: event.Event.ID, Saved: false, Reason: err.Error()})
				continue
			}
			event.client.send(OkResponse{ID: event.Event.ID, Saved: true})

			for client := range r.clients {
				if client == event.client {
					continue
				}

				if match, subID := client.matchesSubscription(event.Event); match {
					client.send(EventResponse{ID: subID, Event: event.Event})
				}
			}

		case req := <-r.reqQueue:
			if _, ok := r.clients[req.client]; !ok {
				// the client has been unregistered
				continue
			}

			events, err := r.OnFilters(req.ctx, req.client, req.Filters)
			if err != nil {
				if req.ctx.Err() == nil {
					// the error was NOT caused by the user cancelling the request, so we send a CLOSE
					req.client.send(ClosedResponse{ID: req.ID, Reason: err.Error()})
				}

				req.client.closeSubscription(req.ID)
				continue
			}

			for _, event := range events {
				req.client.send(EventResponse{ID: req.ID, Event: &event})
			}

			req.client.send(EoseResponse{ID: req.ID})
		}
	}
}

// kill sends a close response for each subscription of each client, and then closes all relay channels.
func (r *Relay) kill() {
	log.Println("shutting down the relay...")
	defer log.Println("relay stopped")

	for client := range r.clients {
		for _, sub := range client.Subscriptions {
			client.send(ClosedResponse{ID: sub.ID, Reason: "shutting down the relay"})
		}
	}
}

// ServeHTTP implements http.Handler interface, rejecting connections as specified
// and only handling WebSocket connections.
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

	client := &Client{IP: IP(req), relay: r, conn: conn, toSend: make(chan Response, 100)}

	select {
	case r.register <- client:
		// successfully added client to registration queue, start reading and writing
		go client.write()
		go client.read()

	default:
		// if the registration queue is full, drop the connection to avoid overloading, and signal a failure
		r.lastRegistrationFail = time.Now()
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "server is overloaded"))
		conn.Close()
	}
}

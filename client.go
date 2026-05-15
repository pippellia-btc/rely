package rely

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	"github.com/pippellia-btc/rely/auth"

	ws "github.com/gorilla/websocket"
)

// Client represents the nostr client connected to the relay. All methods are safe for concurrent use.
type Client interface {
	// UID is the unique identified for the client, useful to tie its identity to
	// external statistics or resources.
	UID() string

	// IP address of the client. For rate-limiting purposes you should use [IP.Group]
	// or [IP.GroupPrefix] as a normalized representation of the IP.
	IP() IP

	// Pubkeys return the slice of unique pubkeys the client used to authenticate with NIP-42.
	// To initiate the authentication, call [Client.SendAuth].
	Pubkeys() []string

	// IsAuthed returns whether the client has performed authentication with one
	// or more pubkeys. It's a more efficient version of len(Client.Pubkeys) > 0.
	IsAuthed() bool

	// SendAuth sends the client a newly generated AUTH challenge.
	// This resets the authentication state: any previously authenticated pubkey is cleared,
	// and a new challenge is generated and sent.
	SendAuth()

	// ConnectedAt returns the time when the client connected.
	ConnectedAt() time.Time

	// Age returns how long the client has been connected.
	// Short for time.Since(client.ConnectedAt()).
	Age() time.Duration

	// Subscriptions returns a snapshot of the currently active [Subscription]s of the client.
	Subscriptions() []Subscription

	// SendNotice to the client, useful for greetings, warnings and other informational messages.
	SendNotice(msg string)

	// Disconnect the client, closing its websocket connection with a [websocket.CloseNormalClosure]
	Disconnect()

	// DroppedResponses returns the total number of responses that were dropped
	// because the client’s response channel was full. This value is monotonic
	// and it's useful for implementing backpressure or flow-control strategies.
	DroppedResponses() int

	// RemainingCapacity returns a snapshot of how many slots are currently
	// available in the client's response buffer. Useful for implementing
	// backpressure or flow-control strategies.
	RemainingCapacity() int
}

// client is a middleman between the websocket connection and the [Relay].
// It's responsible for parsing and validating the [request]s,
// sending them to the [Relay], and for writing the [response]s it receives from it.
//
// The client lifecycle starts after registration in the [Relay.run],
// where [client.read] and [client.write] are spawned. The shutdown cycle is as follows:
//
// - [client.read] returns
// - [client.Disconnect] is called
// - [client.IsUnregistering] is set to true, [client.done] is closed
// - [client.write] returns, with a [websocket.CloseNormalClosure]
// - [client.conn] is closed
// - [client.read] returns
// - ...
//
// There are two entrypoints to trigger the shutdown cycle:
// - read errors in the [client.read] (automatic)
// - the call to [client.Disconnect] (automatic or manual)
type client struct {
	mu   sync.Mutex
	subs map[string]subscription

	auth        *auth.State
	ip          IP
	uid         string
	connectedAt time.Time

	invalidMessages  int
	droppedResponses atomic.Int64

	// pointer to parent relay, which must only be used for:
	//	- reading settings/hooks
	//	- sending to channels
	// 	- incrementing atomic counters
	relay     *Relay
	conn      *ws.Conn
	responses chan response

	isUnregistering atomic.Bool
	done            chan struct{}
}

func (c *client) UID() string            { return c.uid }
func (c *client) IP() IP                 { return c.ip }
func (c *client) Pubkeys() []string      { return c.auth.Pubkeys() }
func (c *client) IsAuthed() bool         { return c.auth.IsAuthed() }
func (c *client) ConnectedAt() time.Time { return c.connectedAt }
func (c *client) Age() time.Duration     { return time.Since(c.connectedAt) }
func (c *client) DroppedResponses() int  { return int(c.droppedResponses.Load()) }
func (c *client) RemainingCapacity() int { return cap(c.responses) - len(c.responses) }
func (c *client) SendNotice(msg string)  { c.send(noticeResponse{Message: msg}) }

func (c *client) Disconnect() {
	if c.isUnregistering.CompareAndSwap(false, true) {
		close(c.done)
		c.relay.unregister <- c
		c.CloseAllSubs()
	}
}

func (c *client) SendAuth() {
	c.auth.Reset(func(challenge string) {
		c.send(authResponse{Challenge: challenge})
	})
}

// The client reads from the websocket and parses the data into the appropriate structure (e.g. [reqRequest]).
// It manages creation and cancellation of subscriptions, and sends the request to the [Relay] to be processed.
func (c *client) read() {
	defer func() {
		c.Disconnect()
		c.relay.wg.Done()
	}()

	c.conn.SetReadLimit(c.relay.settings.WS.maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(c.relay.settings.WS.pongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(c.relay.settings.WS.pongWait)); return nil })

	for {
		if c.invalidMessages >= 5 {
			return
		}

		messageType, reader, err := c.conn.NextReader()
		if err != nil {
			if isUnexpectedClose(err) {
				c.relay.log.Debug("unexpected close error", "error", err, "client", c.ip)
			}
			return
		}

		if messageType != ws.TextMessage {
			c.invalidMessages++
			c.send(noticeResponse{Message: fmt.Sprintf("%v: received binary message", ErrGeneric)})
			continue
		}

		decoder := json.NewDecoder(reader)
		label, err := parseLabel(decoder)
		if err != nil {
			c.invalidMessages++
			c.send(noticeResponse{Message: fmt.Sprintf("%v: %v", ErrGeneric, err)})
			continue
		}

		switch label {
		case "EVENT":
			event, err := parseEvent(decoder)
			if err != nil {
				c.invalidMessages++
				c.send(okResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			err = c.handleEvent(event)
			if err != nil {
				c.send(okResponse{ID: err.ID, Saved: false, Reason: err.Error()})
			}

		case "REQ":
			req, err := parseReq(decoder)
			if err != nil {
				c.invalidMessages++
				c.send(closedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			err = c.handleReq(req)
			if err != nil {
				c.send(closedResponse{ID: err.ID, Reason: err.Error()})
			}

		case "COUNT":
			count, err := parseCount(decoder)
			if err != nil {
				c.invalidMessages++
				c.send(closedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			err = c.handleCount(count)
			if err != nil {
				c.send(closedResponse{ID: err.ID, Reason: err.Error()})
			}

		case "CLOSE":
			close, err := parseClose(decoder)
			if err != nil {
				c.invalidMessages++
				c.send(noticeResponse{Message: err.Error()})
				continue
			}

			c.CloseSub(close.ID)

		case "AUTH":
			auth, err := auth.Parse(decoder)
			if err != nil {
				c.invalidMessages++
				c.send(okResponse{ID: auth.ID, Saved: false, Reason: err.Error()})
				continue
			}

			if err := c.handleAuth(auth); err != nil {
				c.send(okResponse{ID: err.ID, Saved: false, Reason: err.Error()})
			}

		default:
			c.invalidMessages++
			c.send(noticeResponse{Message: ErrUnsupportedType.Error()})
		}
	}
}

// send a [response] to the client in a non-blocking way.
// It prevents sending if the client is unregistering.
func (c *client) send(r response) {
	if c.isUnregistering.Load() {
		return
	}

	select {
	case c.responses <- r:
	default:
		c.droppedResponses.Add(1)
		// Note: client.send MUST be non-blocking to guarantee a correct functioning of the relay,
		// since it is called by all the foundational components (processor, dispatcher...).
		//
		// Previously we called the GreedyClient hook here, which could in fact be a blocking operation,
		// and that caused several issues:
		//
		//  1. If GreedyClient called client.send (for example via client.SendNotice), the relay would
		//     experience uncontrolled recursion (client.send --> GreedyClient --> client.send --> ...)
		//
		//  2. If GreedyClient called client.Disconnect, the relay under load could experience
		//     a multi step deadlock:
		//     - the dispatcher's updates channel is full
		//     - an event is received and the dispatcher broadcasts
		// 	   - the client it broadcasts to has a full buffer
		//     - GreedyClient is called, which invokes client.Disconnect
		//     - client.Disconnect calls client.CloseAllSubs, which in turn tries to send
		// 		an unindexing update to the dispatcher's updates channel (deadlock)
	}
}

// The client writes to the websocket whatever [response] it receives in its channel.
// Periodically it writes [websocket.PingMessage]s.
func (c *client) write() {
	ticker := time.NewTicker(c.relay.settings.WS.pingPeriod)
	defer func() {
		c.conn.Close()
		ticker.Stop()
		c.relay.wg.Done()
	}()

	for {
		// Fast path disconnection.
		// The select down the line can take a few loops before the disconnection
		// is triggered, since if multiple cases are available, one is choosen at random.
		if c.isUnregistering.Load() {
			c.writeCloseNormal()
			return
		}

		select {
		case <-c.done:
			c.writeCloseNormal()
			return

		case response := <-c.responses:
			if len(c.responses)+1 >= cap(c.responses) {
				// before this response was dequeued, the channel was full,
				// which happens when production (client.send) outpaces consumption.
				c.relay.When.GreedyClient(c)
			}

			bytes, err := response.MarshalJSON()
			if err != nil {
				c.relay.log.Error("failed to marshal response", "response", response, "error", err)
			}

			if err := c.writeMessage(bytes); err != nil {
				if isUnexpectedClose(err) {
					c.relay.log.Debug("unexpected error when attemping to write", "error", err, "client", c.ip)
				}
				return
			}

		case <-ticker.C:
			if err := c.writePing(); err != nil {
				if isUnexpectedClose(err) {
					c.relay.log.Debug("unexpected error when attemping to ping", "error", err, "client", c.ip)
				}
				return
			}
		}
	}
}

func (c *client) handleEvent(e eventRequest) *requestError {
	for _, reject := range c.relay.Reject.Event {
		if err := reject(c, e.Event); err != nil {
			return &requestError{ID: e.Event.ID, Err: err}
		}
	}

	e.client = c
	return c.relay.tryProcess(e)
}

func (c *client) handleReq(req reqRequest) *requestError {
	for _, reject := range c.relay.Reject.Req {
		if err := reject(c, req.id, req.Filters); err != nil {
			return &requestError{ID: req.id, Err: err}
		}
	}

	sub := subscription{
		uid:       join(c.uid, req.id),
		id:        req.id,
		filters:   slices.Clone(req.Filters),
		createdAt: time.Now(),
		client:    c,
	}

	req.ctx, sub.cancel = context.WithCancel(context.Background())
	req.client = c

	if err := c.tryOpen(sub); err != nil {
		return err
	}
	if err := c.relay.tryProcess(req); err != nil {
		c.CloseSub(sub.id)
		return err
	}
	return nil
}

func (c *client) handleCount(count countRequest) *requestError {
	if c.relay.On.Count == nil {
		// nip-45 is optional
		return &requestError{ID: count.id, Err: ErrUnsupportedNIP45}
	}

	for _, reject := range c.relay.Reject.Count {
		if err := reject(c, count.id, count.Filters); err != nil {
			return &requestError{ID: count.id, Err: err}
		}
	}

	count.client = c
	return c.relay.tryProcess(count)
}

func (c *client) handleAuth(request auth.Request) *requestError {
	if err := c.auth.Validate(request); err != nil {
		// increase the invalid messages count to avoid attacking clients from spamming
		// the relay with invalid auth requests, as they will eventually be disconnected.
		c.invalidMessages++
		return &requestError{ID: request.ID, Err: err}
	}

	if err := c.auth.Add(request.Pubkey); err != nil {
		return &requestError{ID: request.ID, Err: err}
	}

	c.send(okResponse{ID: request.ID, Saved: true})
	c.relay.On.Auth(c)
	return nil
}

func (c *client) writeMessage(b []byte) error {
	c.conn.SetWriteDeadline(time.Now().Add(c.relay.settings.WS.writeWait))
	return c.conn.WriteMessage(ws.TextMessage, b)
}

func (c *client) writeCloseNormal() error {
	return c.conn.WriteControl(
		ws.CloseMessage,
		ws.FormatCloseMessage(ws.CloseNormalClosure, ""),
		time.Now().Add(c.relay.settings.WS.writeWait),
	)
}

func (c *client) writeCloseGoingAway() error {
	return c.conn.WriteControl(
		ws.CloseMessage,
		ws.FormatCloseMessage(ws.CloseGoingAway, ErrShuttingDown.Error()),
		time.Now().Add(c.relay.settings.WS.writeWait),
	)
}

func (c *client) writeCloseTryLater() error {
	return c.conn.WriteControl(
		ws.CloseMessage,
		ws.FormatCloseMessage(ws.CloseTryAgainLater, ErrOverloaded.Error()),
		time.Now().Add(c.relay.settings.WS.writeWait),
	)
}

func (c *client) writePing() error {
	return c.conn.WriteControl(
		ws.PingMessage,
		nil,
		time.Now().Add(c.relay.settings.WS.writeWait),
	)
}

func isUnexpectedClose(err error) bool {
	return ws.IsUnexpectedCloseError(err,
		ws.CloseNormalClosure,
		ws.CloseGoingAway,
		ws.CloseNoStatusReceived,
		ws.CloseAbnormalClosure)
}

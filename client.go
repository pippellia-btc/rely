package rely

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"

	ws "github.com/gorilla/websocket"
)

// The Client where the request comes from. All methods are safe for concurrent use.
type Client interface {
	// UID is the unique identified for the client, useful to tie its identity to
	// external statistics or resources.
	UID() string

	// IP address of the client.
	IP() string

	// Pubkey the client used to authenticate with NIP-42, or an empty string if it didn't.
	// To initiate the authentication, call [Client.SendAuth].
	Pubkey() string

	// Subscriptions returns the currently active "REQ" subscriptions of the client.
	// Subscriptions() []subscription

	// SendNotice to the client, useful for greeting, warnings and other informational messages.
	SendNotice(string)

	// SendAuth sends the client a newly generated AUTH challenge.
	// This resets the authentication state: any previously authenticated pubkey is cleared,
	// and a new challenge is generated and sent.
	SendAuth()

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
// The client lifecycle starts after registration in the [Relay.dispatchLoop],
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
	uid    string
	ip     string
	auther auther

	relay     *Relay
	conn      *ws.Conn
	responses chan response

	done             chan struct{}
	isUnregistering  atomic.Bool
	droppedResponses atomic.Int64
	invalidMessages  int
}

func (c *client) UID() string    { return c.uid }
func (c *client) IP() string     { return c.ip }
func (c *client) Pubkey() string { return c.auther.Pubkey() }

// func (c *client) Subscriptions() []subscription { return c.subscriptions.List() }
func (c *client) DroppedResponses() int  { return int(c.droppedResponses.Load()) }
func (c *client) RemainingCapacity() int { return cap(c.responses) - len(c.responses) }

func (c *client) SendNotice(message string) {
	c.send(noticeResponse{Message: message})
}

func (c *client) SendAuth() {
	bytes := make([]byte, authChallengeBytes)
	rand.Read(bytes)
	challenge := hex.EncodeToString(bytes)

	c.auther.mu.Lock()
	defer c.auther.mu.Unlock()

	c.auther.pubkey = ""
	c.auther.challenge = challenge
	c.send(authResponse{Challenge: challenge})
}

func (c *client) Disconnect() {
	if c.isUnregistering.CompareAndSwap(false, true) {
		close(c.done)
		c.relay.unregister <- c
	}
}

// The client reads from the websocket and parses the data into the appropriate structure (e.g. [reqRequest]).
// It manages creation and cancellation of subscriptions, and sends the request to the [Relay] to be processed.
func (c *client) read() {
	defer func() {
		c.Disconnect()
		c.relay.wg.Done()
	}()

	c.conn.SetReadLimit(c.relay.maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(c.relay.pongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(c.relay.pongWait)); return nil })

	for {
		if c.invalidMessages >= 5 {
			return
		}

		messageType, reader, err := c.conn.NextReader()
		if err != nil {
			if isUnexpectedClose(err) {
				c.relay.log.Info("unexpected close error from IP %s: %v", c.ip, err)
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

			c.relay.closeSubscription(join(c.uid, close.ID))

		case "AUTH":
			auth, err := parseAuth(decoder)
			if err != nil {
				c.invalidMessages++
				c.send(okResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			if err := c.auther.Validate(auth); err != nil {
				c.send(okResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			c.auther.SetPubkey(auth.PubKey)
			c.send(okResponse{ID: auth.ID, Saved: true})
			c.relay.On.Auth(c)

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
		c.relay.When.GreedyClient(c)
	}
}

// The client writes to the websocket whatever [response] it receives in its channel.
// Periodically it writes [websocket.PingMessage]s.
func (c *client) write() {
	ticker := time.NewTicker(c.relay.pingPeriod)
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
			if err := c.writeJSON(response); err != nil {
				if isUnexpectedClose(err) {
					c.relay.log.Info("unexpected error when attemping to write to the IP %s: %v", c.ip, err)
				}
				return
			}

		case <-ticker.C:
			if err := c.writePing(); err != nil {
				if isUnexpectedClose(err) {
					c.relay.log.Info("unexpected error when attemping to ping the IP %s: %v", c.ip, err)
				}
				return
			}
		}
	}
}

func (c *client) handleEvent(e *eventRequest) *requestError {
	for _, reject := range c.relay.Reject.Event {
		if err := reject(c, e.Event); err != nil {
			return &requestError{ID: e.Event.ID, Err: err}
		}
	}

	e.client = c
	return c.relay.tryProcess(e)
}

func (c *client) handleReq(req *reqRequest) *requestError {
	for _, reject := range c.relay.Reject.Req {
		if err := reject(c, req.Filters); err != nil {
			return &requestError{ID: req.id, Err: err}
		}
	}

	sub := subscription{
		uid:     join(c.uid, req.id),
		id:      req.id,
		filters: req.Filters,
		client:  c,
	}

	req.ctx, sub.cancel = context.WithCancel(context.Background())
	req.client = c

	if err := c.relay.tryProcess(req); err != nil {
		return err
	}

	if err := c.relay.tryOpen(sub); err != nil {
		sub.cancel()
		return err
	}
	return nil
}

func (c *client) handleCount(count *countRequest) *requestError {
	if c.relay.On.Count == nil {
		// nip-45 is optional
		return &requestError{ID: count.id, Err: ErrUnsupportedNIP45}
	}

	for _, reject := range c.relay.Reject.Count {
		if err := reject(c, count.Filters); err != nil {
			return &requestError{ID: count.id, Err: err}
		}
	}

	count.client = c
	return c.relay.tryProcess(count)
}

func (c *client) writeJSON(v any) error {
	c.conn.SetWriteDeadline(time.Now().Add(c.relay.writeWait))
	return c.conn.WriteJSON(v)
}

func (c *client) writeCloseNormal() error {
	return c.conn.WriteControl(
		ws.CloseMessage,
		ws.FormatCloseMessage(ws.CloseNormalClosure, ""),
		time.Now().Add(c.relay.writeWait),
	)
}

func (c *client) writeCloseGoingAway() error {
	return c.conn.WriteControl(
		ws.CloseMessage,
		ws.FormatCloseMessage(ws.CloseGoingAway, ErrShuttingDown.Error()),
		time.Now().Add(c.relay.writeWait),
	)
}

func (c *client) writeCloseTryLater() error {
	return c.conn.WriteControl(
		ws.CloseMessage,
		ws.FormatCloseMessage(ws.CloseTryAgainLater, ErrOverloaded.Error()),
		time.Now().Add(c.relay.writeWait),
	)
}

func (c *client) writePing() error {
	return c.conn.WriteControl(
		ws.PingMessage,
		nil,
		time.Now().Add(c.relay.writeWait),
	)
}

package rely

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

const authChallengeBytes = 16

// The Client where the request comes from. All methods are safe for concurrent use.
type Client interface {
	// IP address of the client.
	IP() string

	// Subscriptions returns the currently active "REQ" subscriptions of the client.
	Subscriptions() []Subscription

	// Pubkey the client used to authenticate with NIP-42, or an empty string if it didn't.
	// To initiate the authentication, call [Client.SendAuth].
	Pubkey() string

	// SendNotice to the client, useful for greeting, warnings and other informational messages.
	SendNotice(string)

	// SendAuth sends the client a newly generated AUTH challenge.
	// This resets the authentication state: any previously authenticated pubkey is cleared,
	// and a new challenge is generated and sent.
	SendAuth()

	// Disconnect the client, closing its websocket connection.
	Disconnect()

	// DroppedResponses returns the total number of responses that were droppedResponses
	// because the client’s response channel was full. This value is monotonic
	// and it's useful for implementing backpressure or flow-control strategies.
	DroppedResponses() int

	// RemainingCapacity returns a snapshot of how many slots are currently
	// available in the client's response buffer. Useful for implementing
	// backpressure or flow-control strategies.
	RemainingCapacity() int
}

// client is a middleman between the websocket connection and the [Relay].
// It's responsible for parsing and validating the requests, and for writing the [response]s
// to all matching [Subscription]s.
type client struct {
	mu            sync.RWMutex
	ip            string
	subscriptions Subscriptions

	// NIP-42
	pubkey    string
	challenge string

	relay     *Relay
	conn      *websocket.Conn
	responses chan response

	isUnregistering  atomic.Bool
	droppedResponses atomic.Int64
}

func (c *client) IP() string                    { return c.ip }
func (c *client) Subscriptions() []Subscription { return c.subscriptions.List() }
func (c *client) DroppedResponses() int         { return int(c.droppedResponses.Load()) }
func (c *client) RemainingCapacity() int        { return cap(c.responses) - len(c.responses) }

func (c *client) setPubkey(pubkey string) {
	c.mu.Lock()
	c.pubkey = pubkey
	c.mu.Unlock()
}

func (c *client) Pubkey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pubkey
}

func (c *client) SendNotice(message string) {
	c.send(noticeResponse{Message: message})
}

func (c *client) SendAuth() {
	challenge := make([]byte, authChallengeBytes)
	rand.Read(challenge)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.pubkey = ""
	c.challenge = hex.EncodeToString(challenge)
	c.send(authResponse{Challenge: c.challenge})
}

// Disconnect the client by sending a [websocket.CloseNormalClosure]
func (c *client) Disconnect() {
	if c.isUnregistering.CompareAndSwap(false, true) {
		c.relay.unregister <- c
	}
}

// rejectEvent wraps the relay RejectEvent functions and makes them accessible to the client.
func (c *client) rejectEvent(e *eventRequest) *requestError {
	for _, reject := range c.relay.RejectEvent {
		if err := reject(c, e.Event); err != nil {
			return &requestError{ID: e.Event.ID, Err: err}
		}
	}
	return nil
}

// rejectReq wraps the relay RejectReq functions and makes them accessible to the client.
func (c *client) rejectReq(req *reqRequest) *requestError {
	for _, reject := range c.relay.RejectReq {
		if err := reject(c, req.Filters); err != nil {
			return &requestError{ID: req.id, Err: err}
		}
	}
	return nil
}

// rejectCount wraps the relay RejectCount functions and makes them accessible to the client.
// if relay.OnCount has not been set, an error is returned.
func (c *client) rejectCount(count *countRequest) *requestError {
	if c.relay.OnCount == nil {
		// nip-45 is optional
		return &requestError{ID: count.id, Err: ErrUnsupportedNIP45}
	}

	for _, reject := range c.relay.RejectCount {
		if err := reject(c, count.Filters); err != nil {
			return &requestError{ID: count.id, Err: err}
		}
	}
	return nil
}

// validateAuth returns the appropriate error if the auth is invalid, otherwise returns nil.
func (c *client) validateAuth(auth *authRequest) *requestError {
	if auth.Event.Kind != nostr.KindClientAuthentication {
		return &requestError{ID: auth.ID, Err: ErrInvalidAuthKind}
	}

	if time.Since(auth.CreatedAt.Time()).Abs() > time.Minute {
		return &requestError{ID: auth.ID, Err: ErrInvalidTimestamp}
	}

	if !strings.Contains(auth.Relay(), c.relay.domain) {
		return &requestError{ID: auth.ID, Err: ErrInvalidAuthRelay}
	}

	if err := InvalidID(c, auth.Event); err != nil {
		return &requestError{ID: auth.ID, Err: err}
	}

	if err := InvalidSignature(c, auth.Event); err != nil {
		return &requestError{ID: auth.ID, Err: err}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.challenge == "" || auth.Challenge() != c.challenge {
		return &requestError{ID: auth.ID, Err: ErrInvalidAuthChallenge}
	}

	return nil
}

// The client reads from the websocket and parses the data into the appropriate structure (e.g. [reqRequest]).
// It manages creation and cancellation of subscriptions, and sends the request to the [Relay] to be processed.
func (c *client) read() {
	defer func() {
		c.Disconnect()
		c.conn.Close()
	}()

	invalidMessages := 0
	c.conn.SetReadLimit(c.relay.maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(c.relay.pongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(c.relay.pongWait)); return nil })

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if isUnexpectedClose(err) {
				log.Printf("unexpected close error from IP %s: %v", c.ip, err)
			}
			return
		}

		label, json, err := parseJSON(data)
		if err != nil {
			invalidMessages++
			if invalidMessages > 2 {
				// disconnect abruptly
				return
			}

			c.send(noticeResponse{Message: fmt.Sprintf("%v: %v", ErrGeneric, err)})
			continue
		}

		switch label {
		case "EVENT":
			event, err := parseEvent(json)
			if err != nil {
				c.send(okResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			if err := c.rejectEvent(event); err != nil {
				c.send(okResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			event.client = c
			if err := c.relay.enqueue(event); err != nil {
				c.send(okResponse{ID: err.ID, Saved: false, Reason: err.Error()})
			}

		case "REQ":
			req, err := parseReq(json)
			if err != nil {
				c.send(closedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			if err := c.rejectReq(req); err != nil {
				c.send(closedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			req.client = c
			sub := req.Subscription()

			if err := c.relay.enqueue(req); err != nil {
				c.send(closedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			c.subscriptions.Add(sub)

		case "COUNT":
			count, err := parseCount(json)
			if err != nil {
				c.send(closedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			if err := c.rejectCount(count); err != nil {
				c.send(closedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			count.client = c
			sub := count.Subscription()

			if err := c.relay.enqueue(count); err != nil {
				c.send(closedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			c.subscriptions.Add(sub)

		case "CLOSE":
			close, err := parseClose(json)
			if err != nil {
				c.send(noticeResponse{Message: err.Error()})
				continue
			}

			c.subscriptions.Remove(close.subID)

		case "AUTH":
			auth, err := parseAuth(json)
			if err != nil {
				c.send(okResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			if err := c.validateAuth(auth); err != nil {
				c.send(okResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			c.setPubkey(auth.PubKey)
			c.send(okResponse{ID: auth.ID, Saved: true})
			c.relay.OnAuth(c)

		default:
			c.send(noticeResponse{Message: ErrUnsupportedType.Error()})
		}
	}
}

// The client writes to the websocket whatever [response] it receives in its channel.
// Periodically it writes [websocket.PingMessage]s.
func (c *client) write() {
	ticker := time.NewTicker(c.relay.pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case response, ok := <-c.responses:
			c.conn.SetWriteDeadline(time.Now().Add(c.relay.writeWait))

			if !ok {
				// the relay has closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}

			if err := c.conn.WriteJSON(response); err != nil {
				if isUnexpectedClose(err) {
					log.Printf("unexpected error when attemping to write to the IP %s: %v", c.ip, err)
				}
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(c.relay.writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				if isUnexpectedClose(err) {
					log.Printf("unexpected error when attemping to ping the IP %s: %v", c.ip, err)
				}
				return
			}
		}
	}
}

func (c *client) send(r response) {
	if c.isUnregistering.Load() {
		return
	}

	select {
	case c.responses <- r:
	default:
		c.droppedResponses.Add(1)
		c.relay.OnGreedyClient(c)
	}
}

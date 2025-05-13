package rely

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

const AuthChallengeBytes = 16

type Subscription struct {
	ID      string
	Type    string // either "REQ" or "COUNT"
	Filters nostr.Filters
	cancel  context.CancelFunc // calling it cancels the context of the associated REQ/COUNT
}

type Client interface {
	// Subscriptions returns the currently active subscriptions of the client
	Subscriptions() []Subscription

	// IP returns the IP address of the client
	IP() string

	// Pubkey returns the pubkey the client used to authenticate with NIP-42. If the client didn't auth, it returns nil.
	// To initiate the authentication, call [Client.SendAuthChallenge]
	Pubkey() *string

	// SendAuthChallenge sends the client a newly generated AUTH challenge.
	// This resets the authentication state: any previously authenticated pubkey is cleared,
	// and a new challenge is generated and sent
	SendAuthChallenge()

	// Disconnect the client, closing its websocket connection
	Disconnect()
}

// client is a middleman between the websocket connection and the [Relay].
// It's responsible for reading and validating the requests, and for writing the [Response]s
// if they satisfy at least one [Subscription].
type client struct {
	mu            sync.RWMutex
	ip            string
	subscriptions []Subscription

	// NIP-42
	pubkey    *string
	challenge string

	relay  *Relay
	conn   *websocket.Conn
	toSend chan Response

	isUnregistering atomic.Bool
}

func (c *client) IP() string { return c.ip }

func (c *client) Subscriptions() []Subscription {
	c.mu.RLock()
	defer c.mu.RUnlock()

	subs := make([]Subscription, len(c.subscriptions))
	copy(subs, c.subscriptions)
	return subs
}

func (c *client) setPubkey(pubkey string) {
	c.mu.Lock()
	c.pubkey = &pubkey
	c.mu.Unlock()
}

func (c *client) Pubkey() *string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.pubkey == nil {
		return nil
	}
	pubkey := *c.pubkey
	return &pubkey
}

func (c *client) SendAuthChallenge() {
	challenge := make([]byte, AuthChallengeBytes)
	rand.Read(challenge)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.pubkey = nil
	c.challenge = hex.EncodeToString(challenge)
	c.send(AuthResponse{Challenge: c.challenge})
}

func (c *client) Disconnect() {
	if c.isUnregistering.CompareAndSwap(false, true) {
		c.relay.unregister <- c
	}
}

// closeSubscription closes the subscription with the provided ID, if present.
func (c *client) closeSubscription(ID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, sub := range c.subscriptions {
		if sub.ID == ID {
			// cancels the context of the associated REQ/COUNT and removes the subscription from the client
			sub.cancel()
			c.subscriptions = append(c.subscriptions[:i], c.subscriptions[i+1:]...)
			return
		}
	}
}

// openSubscription registers the provided subscription with the client.
// If a subscription with the same ID already exists, the existing subscription is canceled
// and replaced with the new one.
func (c *client) openSubscription(sub Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()

	pos := slices.IndexFunc(c.subscriptions, func(s Subscription) bool {
		return s.ID == sub.ID
	})

	switch pos {
	case -1:
		// the subscription has an ID that was never seen, so we add a new subscription
		c.subscriptions = append(c.subscriptions, sub)

	default:
		// the subscription is overwriting an existing subscription, so we cancel and remove the old for the new
		c.subscriptions[pos].cancel()
		c.subscriptions[pos] = sub
	}
}

// matchesSubscription returns which (REQ) Subscription of the client matches the provided event (if any).
func (c *client) matchingSubscription(event *nostr.Event) (match bool, ID string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, sub := range c.subscriptions {
		if sub.Type == "REQ" && sub.Filters.Match(event) {
			return true, sub.ID
		}
	}
	return false, ""
}

// rejectEvent wraps the relay RejectEvent method and makes them accessible to the client.
func (c *client) rejectEvent(e *EventRequest) *RequestError {
	for _, reject := range c.relay.RejectEvent {
		if err := reject(c, e.Event); err != nil {
			return &RequestError{ID: e.Event.ID, Err: err}
		}
	}
	return nil
}

// rejectReq wraps the relay RejectReq method and makes them accessible to the client.
func (c *client) rejectReq(req *ReqRequest) *RequestError {
	for _, reject := range c.relay.RejectReq {
		if err := reject(c, req.Filters); err != nil {
			return &RequestError{ID: req.subID, Err: err}
		}
	}
	return nil
}

// rejectCount wraps the relay RejectCount method and makes them accessible to the client.
// if relay.OnCount has not been set, an error is returned.
func (c *client) rejectCount(count *CountRequest) *RequestError {
	if c.relay.OnCount == nil {
		// nip-45 is optional
		return &RequestError{ID: count.subID, Err: ErrUnsupportedNIP45}
	}

	for _, reject := range c.relay.RejectCount {
		if err := reject(c, count.Filters); err != nil {
			return &RequestError{ID: count.subID, Err: err}
		}
	}
	return nil
}

// validateAuth returns the appropriate error if the auth is invalid, otherwise returns nil.
func (c *client) validateAuth(auth *AuthRequest) *RequestError {
	if auth.Event.Kind != nostr.KindClientAuthentication {
		return &RequestError{ID: auth.ID, Err: ErrInvalidAuthKind}
	}

	if time.Since(auth.CreatedAt.Time()).Abs() > time.Minute {
		return &RequestError{ID: auth.ID, Err: ErrInvalidTimestamp}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	challenge := auth.Challenge()
	if len(challenge) != 2*AuthChallengeBytes || challenge != c.challenge {
		// the length check prevents auth attempts before the challenge is sent
		return &RequestError{ID: auth.ID, Err: ErrInvalidAuthChallenge}
	}

	relay := auth.Relay()
	if !strings.Contains(relay, c.relay.domain) {
		return &RequestError{ID: auth.ID, Err: ErrInvalidAuthRelay}
	}

	if err := InvalidID(c, auth.Event); err != nil {
		return &RequestError{ID: auth.ID, Err: err}
	}

	if err := InvalidSignature(c, auth.Event); err != nil {
		return &RequestError{ID: auth.ID, Err: err}
	}

	return nil
}

// The client reads from the websocket and parses the data into the appropriate structure (e.g. [ReqRequest]).
// It manages creation and cancellation of subscriptions, and sends the request to the [Relay] to be processed.
func (c *client) read() {
	defer func() {
		c.Disconnect()
		c.conn.Close()
	}()

	c.conn.SetReadLimit(c.relay.maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(c.relay.pongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(c.relay.pongWait)); return nil })

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if IsUnexpectedClose(err) {
				log.Printf("unexpected close error from IP %s: %v", c.ip, err)
			}
			return
		}

		label, json, err := parseJSON(data)
		if err != nil {
			// if unable to parse the message, send a generic NOTICE
			c.send(NoticeResponse{Message: fmt.Sprintf("%v: %v", ErrGeneric, err)})
			continue
		}

		switch label {
		case "EVENT":
			event, err := parseEvent(json)
			if err != nil {
				c.send(OkResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			if err := c.rejectEvent(event); err != nil {
				c.send(OkResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			event.client = c
			if err := c.relay.enqueue(event); err != nil {
				c.send(OkResponse{ID: err.ID, Saved: false, Reason: err.Error()})
			}

		case "REQ":
			req, err := parseReq(json)
			if err != nil {
				c.send(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			if err := c.rejectReq(req); err != nil {
				c.send(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			req.client = c
			sub := req.Subscription()

			if err := c.relay.enqueue(req); err != nil {
				c.send(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			c.openSubscription(sub)

		case "COUNT":
			count, err := parseCount(json)
			if err != nil {
				c.send(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			if err := c.rejectCount(count); err != nil {
				c.send(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			count.client = c
			sub := count.Subscription()

			if err := c.relay.enqueue(count); err != nil {
				c.send(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			c.openSubscription(sub)

		case "CLOSE":
			close, err := parseClose(json)
			if err != nil {
				c.send(NoticeResponse{Message: err.Error()})
				continue
			}

			c.closeSubscription(close.subID)

		case "AUTH":
			auth, err := parseAuth(json)
			if err != nil {
				c.send(OkResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			if err := c.validateAuth(auth); err != nil {
				c.send(OkResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			c.setPubkey(auth.PubKey)
			c.send(OkResponse{ID: auth.ID, Saved: true})

		default:
			c.send(NoticeResponse{Message: ErrUnsupportedType.Error()})
		}
	}
}

// The client writes to the websocket whatever [Response] it receives in its channel.
// Periodically it writes [websocket.PingMessage]s.
func (c *client) write() {
	ticker := time.NewTicker(c.relay.pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case response, ok := <-c.toSend:
			c.conn.SetWriteDeadline(time.Now().Add(c.relay.writeWait))

			if !ok {
				// the relay has closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}

			if err := c.conn.WriteJSON(response); err != nil {
				if IsUnexpectedClose(err) {
					log.Printf("unexpected error when attemping to write to the IP %s: %v", c.ip, err)
				}
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(c.relay.writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				if IsUnexpectedClose(err) {
					log.Printf("unexpected error when attemping to ping the IP %s: %v", c.ip, err)
				}
				return
			}
		}
	}
}

func (c *client) send(r Response) {
	if c.isUnregistering.Load() {
		return
	}

	select {
	case c.toSend <- r:
	default:
		log.Printf("failed to send the client with IP %s the response %v: channel is full", c.ip, r)
	}
}

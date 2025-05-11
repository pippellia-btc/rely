package rely

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	cancel  context.CancelFunc // calling it cancels the context of the associated REQ
	Filters nostr.Filters
}

// newSubscription creates the subscription associated with the provided [ReqRequest].
func newSubscription(req *ReqRequest) Subscription {
	sub := Subscription{ID: req.subID, Filters: req.Filters}
	req.ctx, sub.cancel = context.WithCancel(context.Background())
	return sub
}

// Client is a middleman between the websocket connection and the [Relay].
// It's responsible for reading and validating the requests, and for writing the responses
// if they satisfy at least one [Subscription].
type Client struct {
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

// IP returns the IP address of the client
func (c *Client) IP() string { return c.ip }

// Subscriptions returns the currently active subscriptions of the client
func (c *Client) Subscriptions() []Subscription {
	c.mu.RLock()
	defer c.mu.RUnlock()

	subs := make([]Subscription, len(c.subscriptions))
	copy(subs, c.subscriptions)
	return subs
}

func (c *Client) setPubkey(pubkey string) {
	c.mu.Lock()
	c.pubkey = &pubkey
	c.mu.Unlock()
}

// Pubkey returns the pubkey the client used to authenticate with NIP-42. If the client didn't auth, it returns nil.
// To initiate the authentication, call [Client.SendAuthChallenge].
func (c *Client) Pubkey() *string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.pubkey == nil {
		return nil
	}
	pubkey := *c.pubkey
	return &pubkey
}

// SendAuthChallenge sends the client a newly generated AUTH challenge.
// This resets the authentication state:
// any previously authenticated pubkey is cleared, and a new challenge is generated and sent.
func (c *Client) SendAuthChallenge() {
	challenge := make([]byte, AuthChallengeBytes)
	rand.Read(challenge)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.pubkey = nil
	c.challenge = hex.EncodeToString(challenge)
	c.send(AuthResponse{Challenge: c.challenge})
}

// Disconnect sends the client to the unregister queue if it's not already being unregistered.
// There, it will be removed from the active clients and its channel will be closed.
// This in turn will close the websocket connection.
func (c *Client) Disconnect() {
	if c.isUnregistering.CompareAndSwap(false, true) {
		c.relay.unregister <- c
	}
}

// closeSubscription closes the subscription with the provided ID, if present.
func (c *Client) closeSubscription(ID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, sub := range c.subscriptions {
		if sub.ID == ID {
			// cancels the context of the associated REQ and removes the subscription from the client
			sub.cancel()
			c.subscriptions = append(c.subscriptions[:i], c.subscriptions[i+1:]...)
			return
		}
	}
}

// openSubscription registers the provided subscription with the client.
// If a subscription with the same ID already exists, the existing subscription is canceled
// and replaced with the new one.
func (c *Client) openSubscription(sub Subscription) {
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

// matchesSubscription returns which subscription of the client matches the provided event (if any).
func (c *Client) matchesSubscription(event *nostr.Event) (match bool, ID string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, sub := range c.subscriptions {
		if sub.Filters.Match(event) {
			return true, sub.ID
		}
	}
	return false, ""
}

// rejectReq wraps the relay RejectFilters method and makes them accessible to the client.
func (c *Client) rejectReq(req *ReqRequest) *RequestError {
	for _, reject := range c.relay.RejectFilters {
		if err := reject(c, req.Filters); err != nil {
			return &RequestError{ID: req.subID, Err: err}
		}
	}
	return nil
}

// rejectEvent wraps the relay RejectEvent method and makes them accessible to the client.
func (c *Client) rejectEvent(e *EventRequest) *RequestError {
	for _, reject := range c.relay.RejectEvent {
		if err := reject(c, e.Event); err != nil {
			return &RequestError{ID: e.Event.ID, Err: err}
		}
	}
	return nil
}

// validateAuth returns the appropriate error if the auth is invalid, otherwise returns nil.
func (c *Client) validateAuth(auth *AuthRequest) *RequestError {
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
func (c *Client) read() {
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

		label, json, err := JSONArray(data)
		if err != nil {
			// if unable to parse the message, send a generic NOTICE
			c.send(NoticeResponse{Message: err.Error()})
			continue
		}

		switch label {
		case "EVENT":
			event, err := ParseEventRequest(json)
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
			req, err := ParseReqRequest(json)
			if err != nil {
				c.send(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			if err := c.rejectReq(req); err != nil {
				c.send(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			req.client = c
			sub := newSubscription(req)

			if err := c.relay.enqueue(req); err != nil {
				c.send(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			c.openSubscription(sub)

		case "CLOSE":
			close, err := ParseCloseRequest(json)
			if err != nil {
				c.send(NoticeResponse{Message: err.Error()})
				continue
			}

			c.closeSubscription(close.subID)

		case "AUTH":
			auth, err := ParseAuthRequest(json)
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
func (c *Client) write() {
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

func (c *Client) send(r Response) {
	if c.isUnregistering.Load() {
		return
	}

	select {
	case c.toSend <- r:
	default:
		log.Printf("failed to send the client with IP %s the response %v: channel is full", c.ip, r)
	}
}

package rely

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

type Subscription struct {
	ID      string
	cancel  context.CancelFunc // calling it cancels the context of the associated REQ
	Filters nostr.Filters
}

/*
Client is a middleman between the websocket connection and the [Relay].
It's responsible for reading and validating the requests, and for writing the responses
if they satisfy at least one [Subscription].
*/
type Client struct {
	IP            string
	Subscriptions []Subscription

	relay *Relay
	conn  *websocket.Conn
	send  chan Response
}

// Disconnect sends the client to the unregister queue, where it will be removed
// from the active clients, which will later close the websocket connection.
func (c *Client) Disconnect() {
	c.relay.unregister <- c
}

func (c *Client) closeSubscription(ID string) {
	for i, sub := range c.Subscriptions {
		if sub.ID == ID {
			// cancels the context of the associated REQ and removes the subscription from the client
			sub.cancel()
			c.Subscriptions = append(c.Subscriptions[:i], c.Subscriptions[i+1:]...)
			return
		}
	}
}

func (c *Client) newSubscription(req *ReqRequest) {
	sub := Subscription{ID: req.ID, Filters: req.Filters}
	req.ctx, sub.cancel = context.WithCancel(context.Background())
	req.client = c

	pos := slices.IndexFunc(c.Subscriptions, func(s Subscription) bool {
		return s.ID == req.ID
	})

	switch pos {
	case -1:
		// the REQ has an ID that was never seen, so we add a new subscription
		c.Subscriptions = append(c.Subscriptions, sub)

	default:
		// the REQ is overwriting an existing subscription, so we cancel and remove the old for the new
		c.Subscriptions[pos].cancel()
		c.Subscriptions[pos] = sub
	}
}

func (c *Client) matchesSubscription(event *nostr.Event) (match bool, ID string) {
	for _, sub := range c.Subscriptions {
		if sub.Filters.Match(event) {
			return true, sub.ID
		}
	}
	return false, ""
}

func (c *Client) rejectReq(req *ReqRequest) *RequestError {
	for _, reject := range c.relay.RejectFilters {
		if err := reject(req.ctx, req.client, req.Filters); err != nil {
			return &RequestError{ID: req.ID, Err: err}
		}
	}
	return nil
}

func (c *Client) rejectEvent(e *EventRequest) *RequestError {
	for _, reject := range c.relay.RejectEvent {
		if err := reject(e.client, e.Event); err != nil {
			return &RequestError{ID: e.Event.ID, Err: err}
		}
	}
	return nil
}

// The client reads from the websocket and parses the data into the appropriate structure (e.g. [ReqRequest]).
// It manages creation and cancellation of subscriptions, and sends the request to the [Relay] to be processed.
func (c *Client) read() {
	defer func() {
		c.relay.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(c.relay.MaxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(c.relay.PongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(c.relay.PongWait)); return nil })

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure) {
				log.Printf("unexpected close error from IP %s: %v", c.IP, err)
			}
			return
		}

		label, json, err := JSONArray(data)
		if err != nil {
			// disconnect since the client is not talking nostr
			return
		}

		switch label {
		case "EVENT":
			event, err := ParseEventRequest(json)
			if err != nil {
				c.send <- OkResponse{ID: err.ID, Saved: false, Reason: err.Error()}
				continue
			}

			if err := c.rejectEvent(event); err != nil {
				c.send <- OkResponse{ID: err.ID, Saved: false, Reason: err.Error()}
				continue
			}

			event.client = c
			c.relay.eventQueue <- event

		case "REQ":
			req, err := ParseReqRequest(json)
			if err != nil {
				c.send <- ClosedResponse{ID: err.ID, Reason: err.Error()}
				continue
			}

			if err := c.rejectReq(req); err != nil {
				c.send <- ClosedResponse{ID: err.ID, Reason: err.Error()}
				continue
			}

			c.newSubscription(req)
			c.relay.reqQueue <- req

		case "CLOSE":
			close, err := ParseCloseRequest(json)
			if err != nil {
				c.send <- NoticeResponse{Message: err.Error()}
				continue
			}

			c.closeSubscription(close.ID)

		default:
			c.send <- NoticeResponse{Message: ErrUnsupportedType.Error()}
		}
	}
}

// The client writes to the websocket whatever [Response] it receives in its send channel.
// Periodically it will write [websocket.PingMessage]s.
func (c *Client) write() {
	ticker := time.NewTicker(c.relay.PingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case response, ok := <-c.send:
			if !ok {
				// the relay has closed the channel, which should only happen after
				// it has sent a [ClosedResponse] for all subscriptions of the client.
				return
			}

			c.conn.SetWriteDeadline(time.Now().Add(c.relay.WriteWait))
			if err := c.conn.WriteJSON(response); err != nil {
				log.Printf("error when attempting to write: %v", err)
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(c.relay.WriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("ping failed: %v", err)
				return
			}
		}
	}
}

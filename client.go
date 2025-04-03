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

// Client is a middleman between the websocket connection and the [Relay].
// It's responsible for reading and validating the requests, and for writing the responses
// if they satisfy at least one [Subscription].
type Client struct {
	IP            string
	Subscriptions []Subscription

	relay  *Relay
	conn   *websocket.Conn
	toSend chan Response
}

// Disconnect sends the client to the unregister queue, where it will be removed
// from the active clients where its channel will be closed. This in turn will close the websocket connection.
func (c *Client) Disconnect() {
	c.relay.unregister <- c
}

// closeSubscription closes the subscription with the provided ID, if present.
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

// newSubscription creates the subscription associated with the provided [ReqRequest], and adds it to the client.
// If the REQ has the same ID as an active subscription, it replaces it with a new one.
func (c *Client) newSubscription(req *ReqRequest) {
	sub := Subscription{ID: req.subID, Filters: req.Filters}
	req.ctx, sub.cancel = context.WithCancel(context.Background())

	pos := slices.IndexFunc(c.Subscriptions, func(s Subscription) bool {
		return s.ID == req.subID
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

// matchesSubscription returns which subscription of the client matches the provided event (if any).
func (c *Client) matchesSubscription(event *nostr.Event) (match bool, ID string) {
	for _, sub := range c.Subscriptions {
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

// The client reads from the websocket and parses the data into the appropriate structure (e.g. [ReqRequest]).
// It manages creation and cancellation of subscriptions, and sends the request to the [Relay] to be processed.
func (c *Client) read() {
	defer func() {
		c.relay.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(c.relay.Websocket.MaxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(c.relay.Websocket.PongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(c.relay.Websocket.PongWait)); return nil })

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
			if err := c.relay.enqueue(req); err != nil {
				c.send(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			c.newSubscription(req)

		case "CLOSE":
			close, err := ParseCloseRequest(json)
			if err != nil {
				c.send(NoticeResponse{Message: err.Error()})
				continue
			}

			c.closeSubscription(close.subID)

		default:
			c.send(NoticeResponse{Message: ErrUnsupportedType.Error()})
		}
	}
}

// The client writes to the websocket whatever [Response] it receives in its channel.
// Periodically it writes [websocket.PingMessage]s.
func (c *Client) write() {
	ticker := time.NewTicker(c.relay.Websocket.PingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case response, ok := <-c.toSend:
			c.conn.SetWriteDeadline(time.Now().Add(c.relay.Websocket.WriteWait))

			if !ok {
				// the relay has closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}

			if err := c.conn.WriteJSON(response); err != nil {
				if websocket.IsUnexpectedCloseError(err,
					websocket.CloseNormalClosure,
					websocket.CloseGoingAway,
					websocket.CloseAbnormalClosure) {
					log.Printf("unexpected error when attemping to write to the IP %s: %v", c.IP, err)
				}
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(c.relay.Websocket.WriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				if websocket.IsUnexpectedCloseError(err,
					websocket.CloseNormalClosure,
					websocket.CloseGoingAway,
					websocket.CloseAbnormalClosure) {
					log.Printf("unexpected error when attemping to ping the IP %s: %v", c.IP, err)
				}
				return
			}
		}
	}
}

func (c *Client) send(r Response) {
	select {
	case c.toSend <- r:
	default:
		log.Printf("failed to send the client with IP %s the response %v: channel is full", c.IP, r)
	}
}

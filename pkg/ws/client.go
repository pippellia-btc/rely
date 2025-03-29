package ws

import (
	"context"
	"encoding/json"
	"log"
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
	Relay *Relay

	Conn *websocket.Conn
	Send chan Response
	Subs []Subscription
}

func (c *Client) NewSubscription(req *ReqRequest) {
	sub := Subscription{ID: req.ID, Filters: req.Filters}
	req.ctx, sub.cancel = context.WithCancel(context.Background())
	c.Subs = append(c.Subs, sub)
	req.client = c
}

func (c *Client) CloseSubscription(ID string) {
	for i, sub := range c.Subs {
		if sub.ID == ID {
			// cancels the context of the associated REQ and removes the subscription from the client
			sub.cancel()
			c.Subs = append(c.Subs[:i], c.Subs[i+1:]...)
			return
		}
	}
}

func (c *Client) MatchesSubscription(event *nostr.Event) (match bool, ID string) {
	for _, sub := range c.Subs {
		if sub.Filters.Match(event) {
			return true, sub.ID
		}
	}

	return false, ""
}

func (c *Client) RejectReq(req *ReqRequest) *RequestError {
	filters := len(req.Filters)
	for _, sub := range c.Subs {
		filters += len(sub.Filters)
	}

	if filters > c.Relay.MaxFiltersPerClient {
		return &RequestError{ID: req.ID, Err: ErrTooManyOpenFilters}
	}

	for _, filter := range req.Filters {
		for _, reject := range c.Relay.RejectFilter {
			// reject everything if any of the filters is rejected
			if err := reject(&filter); err != nil {
				return &RequestError{ID: req.ID, Err: err}
			}
		}
	}

	return nil
}

func (c *Client) RejectEvent(e *EventRequest) *RequestError {
	for _, reject := range c.Relay.RejectEvent {
		if err := reject(e.Event); err != nil {
			return &RequestError{ID: e.Event.ID, Err: err}
		}
	}

	return nil
}

type Response = json.Marshaler

type NoticeResponse struct {
	Message string
}

func (n NoticeResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"NOTICE", n.Message})
}

type OkResponse struct {
	ID     string
	Saved  bool
	Reason string
}

func (o OkResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{"OK", o.ID, o.Saved, o.Reason})
}

type ClosedResponse struct {
	ID     string
	Reason string
}

func (c ClosedResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"CLOSED", c.ID, c.Reason})
}

type EventResponse struct {
	ID    string
	Event *nostr.Event
}

func (e EventResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{"EVENT", e.ID, e.Event})
}

type EoseResponse struct {
	ID string
}

func (e EoseResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"EOSE", e.ID})
}

func (c *Client) Read() {
	defer func() {
		c.Relay.Unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(c.Relay.MaxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(c.Relay.PongWait))
	c.Conn.SetPongHandler(func(string) error { c.Conn.SetReadDeadline(time.Now().Add(c.Relay.PongWait)); return nil })

	for {
		_, data, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("unexpected close error: %v", err)
			}
			return
		}

		label, json, err := JSONArray(data)
		if err != nil {
			// disconnect since this guy is not talking nostr
			return
		}

		switch label {
		case "EVENT":
			event, err := ParseEventRequest(json)
			if err != nil {
				c.Send <- OkResponse{ID: err.ID, Saved: false, Reason: err.Error()}
				continue
			}

			if err := c.RejectEvent(event); err != nil {
				c.Send <- OkResponse{ID: err.ID, Saved: false, Reason: err.Error()}
				continue
			}

			event.client = c
			c.Relay.EventQueue <- event

		case "REQ":
			req, err := ParseReqRequest(json)
			if err != nil {
				c.Send <- ClosedResponse{ID: err.ID, Reason: err.Error()}
				continue
			}

			if err := c.RejectReq(req); err != nil {
				c.Send <- ClosedResponse{ID: err.ID, Reason: err.Error()}
				continue
			}

			c.NewSubscription(req)
			c.Relay.ReqQueue <- req

		case "CLOSE":
			close, err := ParseCloseRequest(json)
			if err != nil {
				c.Send <- NoticeResponse{Message: err.Error()}
				continue
			}

			c.CloseSubscription(close.ID)

		default:
			c.Send <- NoticeResponse{Message: ErrUnsupportedType.Error()}
		}
	}
}

func (c *Client) Write() {
	ticker := time.NewTicker(c.Relay.PingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case response, ok := <-c.Send:
			if !ok {
				// the relay has closed the channel, which should only happen after the relay has sent a [ClosedResponse].
				return
			}

			c.Conn.SetWriteDeadline(time.Now().Add(c.Relay.WriteWait))
			if err := c.Conn.WriteJSON(response); err != nil {
				log.Printf("error when attempting to write: %v", err)
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(c.Relay.WriteWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("ping failed: %v", err)
				return
			}
		}
	}
}

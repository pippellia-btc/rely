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
It's responsible for reading and parsing the requests, and for writing the responses
if they satisfy at least one [Subscription].
*/
type Client struct {
	Relay *Relay

	Conn *websocket.Conn
	Send chan []byte
	Subs []Subscription
}

func (c *Client) NewSubscription(request *ReqRequest) {
	sub := Subscription{ID: request.ID, Filters: request.Filters}
	request.ctx, sub.cancel = context.WithCancel(context.Background())
	c.Subs = append(c.Subs, sub)
}

func (c *Client) CloseSubscription(ID string) {
	for i, sub := range c.Subs {
		if sub.ID == ID {
			// cancel the context and remove the subscription from the client
			sub.cancel()
			c.Subs = append(c.Subs[:i], c.Subs[i+1:]...)
			return
		}
	}
}

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

		request, err := Parse(data)
		if err != nil {
			c.Conn.WriteJSON(NoticeResponse{Message: err.Error()})
			continue
		}

		switch request.Label {
		case "EVENT":
			event, err := request.ToEventRequest()
			if err != nil {
				c.Conn.WriteJSON(OkResponse{ID: err.ID, Saved: false, Reason: err.Error()})
				continue
			}

			c.Relay.EventQueue <- event

		case "REQ":
			req, err := request.ToReqRequest()
			if err != nil {
				c.Conn.WriteJSON(ClosedResponse{ID: err.ID, Reason: err.Error()})
				continue
			}

			c.NewSubscription(req)
			c.Relay.ReqQueue <- req

		case "CLOSE":
			close, err := request.ToCloseRequest()
			if err != nil {
				c.Conn.WriteJSON(NoticeResponse{Message: err.Error()})
				continue
			}

			c.CloseSubscription(close.ID)

		default:
			c.Conn.WriteJSON(NoticeResponse{Message: ErrUnsupportedType.Error()})
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

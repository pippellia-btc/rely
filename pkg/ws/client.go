package ws

import (
	"context"
	"log"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

type Subscription struct {
	ID      string
	cancel  context.CancelFunc // calling it cancels the context associated with the REQ
	Filters nostr.Filters
}

// Client is a middleman between the websocket connection and the [Relay].
// Each client can have multiple [Subscription]s.
type Client struct {
	Relay *Relay

	Conn *websocket.Conn
	Send chan []byte
	Subs []Subscription
}

func (c *Client) CloseSubscription(ID string) bool {
	for i, sub := range c.Subs {
		if sub.ID == ID {
			// cancel the context and remove the subscription from the client
			sub.cancel()
			c.Subs = append(c.Subs[:i], c.Subs[i+1:]...)
			return true
		}
	}

	return false
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
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}

		c.Relay.Queue <- message
	}
}

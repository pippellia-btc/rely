package ws

import (
	"context"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

const (
	DefaultWriteWait      = 10 * time.Second
	DefaultPongWait       = 60 * time.Second
	DefaultPingPeriod     = 45 * time.Second
	DefaultMaxMessageSize = 512000

	DefaultMaxSubscriptions = 50
)

type Subscription struct {
	ID      string
	cancel  context.CancelFunc // calling it cancels the context associated with the REQ
	Filters nostr.Filters
}

type Options struct {
	// websocket options
	WriteWait      time.Duration
	PongWait       time.Duration
	PingPeriod     time.Duration
	MaxMessageSize int

	// client options
	MaxSubscriptions int
}

func NewOptions() Options {
	return Options{
		WriteWait:        DefaultWriteWait,
		PongWait:         DefaultPongWait,
		PingPeriod:       DefaultPingPeriod,
		MaxMessageSize:   DefaultMaxMessageSize,
		MaxSubscriptions: DefaultMaxSubscriptions,
	}
}

// Client is a middleman between the websocket connection and the [Relay].
// Each client can have multiple [Subscription]s.
type Client struct {
	Conn *websocket.Conn
	Send chan []byte
	Subs []Subscription
	Options
}

func NewClient(conn *websocket.Conn) *Client {
	return NewClientWithOptions(conn, NewOptions())
}

func NewClientWithOptions(conn *websocket.Conn, opt Options) *Client {
	return &Client{
		Conn:    conn,
		Send:    make(chan []byte, 256),
		Subs:    make([]Subscription, 5),
		Options: opt,
	}
}

func (c *Client) CloseSubscription(ID string) {
	for i, sub := range c.Subs {
		if sub.ID == ID {
			// cancel the context and remove the subscription from the client
			sub.cancel()
			c.Subs = append(c.Subs[:i], c.Subs[i+1:]...)
			break
		}
	}
}

// var upgrader = websocket.Upgrader{
// 	ReadBufferSize:  1024,
// 	WriteBufferSize: 1024,
// }

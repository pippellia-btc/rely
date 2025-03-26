package ws

import (
	"time"

	"github.com/gorilla/websocket"
)

const (
	DefaultWriteWait                 time.Duration = 10 * time.Second
	DefaultPongWait                  time.Duration = 60 * time.Second
	DefaultPingPeriod                time.Duration = 45 * time.Second
	DefaultMaxMessageSize            int64         = 512000
	DefaultMaxSubscriptionsPerClient int           = 50
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

type Relay struct {
	Queue chan []byte

	Clients    map[*Client]bool
	Register   chan *Client
	Unregister chan *Client

	ClientLimits
}

type ClientLimits struct {
	// websocket options
	WriteWait      time.Duration
	PongWait       time.Duration
	PingPeriod     time.Duration
	MaxMessageSize int64

	// client options
	MaxSubscriptionsPerClient int
}

func NewClientLimits() ClientLimits {
	return ClientLimits{
		WriteWait:                 DefaultWriteWait,
		PongWait:                  DefaultPongWait,
		PingPeriod:                DefaultPingPeriod,
		MaxMessageSize:            DefaultMaxMessageSize,
		MaxSubscriptionsPerClient: DefaultMaxSubscriptionsPerClient,
	}
}

func NewRelay() *Relay {
	return NewRelayWithOptions(NewClientLimits())
}

func NewRelayWithOptions(limits ClientLimits) *Relay {
	return &Relay{
		Queue:        make(chan []byte, 10000),
		Clients:      make(map[*Client]bool, 100),
		Register:     make(chan *Client, 10),
		Unregister:   make(chan *Client, 10),
		ClientLimits: limits,
	}
}

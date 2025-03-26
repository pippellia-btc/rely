package ws

import "github.com/gorilla/websocket"

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

type Relay struct {
	Queue chan []byte

	Clients    map[*Client]bool
	Register   chan *Client
	Unregister chan *Client
}

func NewRelay() *Relay {
	return &Relay{
		Queue:      make(chan []byte, 10000),
		Clients:    make(map[*Client]bool, 100),
		Register:   make(chan *Client, 10),
		Unregister: make(chan *Client, 10),
	}
}

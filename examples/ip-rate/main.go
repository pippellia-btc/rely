package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/pippellia-btc/rely"
	"github.com/puzpuzpuz/xsync/v3"
)

/*
This relay performs IP-based rate-limiting.
When the same IP address tries to connect too many times, the relay rejects the
http request before upgrading to websocket.
*/

var counter = xsync.NewMapOf[string, int]()

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	go rely.HandleSignals(cancel)

	relay := rely.NewRelay()
	relay.RejectConnection = append(relay.RejectConnection, BadIP)

	addr := "localhost:3334"
	log.Printf("running relay on %s", addr)

	if err := relay.StartAndServe(ctx, addr); err != nil {
		panic(err)
	}
}

func BadIP(r rely.Stats, req *http.Request) error {
	IP := rely.IP(req)
	n, ok := counter.Load(IP)
	if !ok {
		// this is a new IP
		counter.Store(IP, 1)
	}

	if n > 100 {
		// too much for me
		return fmt.Errorf("rate-limited: slow there down chief")
	}

	return nil
}

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/pippellia-btc/rely"
)

/*
This relay performs IP-based rate-limiting.
When the same IP address tries to connect too many times, the relay rejects the
http request before upgrading to websocket.
*/

var counter = make(map[string]*atomic.Int32)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	go rely.HandleSignals(cancel)

	relay := rely.NewRelay()
	relay.RejectConnection = append(relay.RejectConnection, BadIP)
	relay.OnConnect = PrintIP

	addr := "localhost:3334"
	log.Printf("running relay on %s", addr)

	if err := relay.StartAndServe(ctx, addr); err != nil {
		panic(err)
	}
}

func BadIP(s rely.Stats, req *http.Request) error {
	IP := rely.IP(req)
	if _, ok := counter[IP]; !ok {
		// this is a new IP
		counter[IP] = &atomic.Int32{}
		counter[IP].Add(1)
		return nil
	}

	counter[IP].Add(1)
	if counter[IP].Load() > 5 {
		// too much for me
		return fmt.Errorf("rate-limited: slow there down chief")
	}

	return nil
}

func PrintIP(c *rely.Client) error {
	log.Printf("registered client with IP %s", c.IP)
	return nil
}

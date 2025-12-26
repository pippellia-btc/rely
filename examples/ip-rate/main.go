package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	relay := rely.NewRelay()
	relay.Reject.Connection.Prepend(BadIP)
	relay.On.Connect = PrintIP

	if err := relay.StartAndServe(ctx, "localhost:3334"); err != nil {
		panic(err)
	}
}

func BadIP(s rely.Stats, req *http.Request) error {
	IP := rely.GetIP(req).Group()
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

func PrintIP(c rely.Client) {
	log.Printf("registered client with IP %s", c.IP())
}

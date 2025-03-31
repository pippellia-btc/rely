package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"

	"github.com/pippellia-btc/rely"
)

var blackList = []string{"IP_123", "IP_abc"}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	go rely.HandleSignals(cancel)

	relay := rely.NewRelay()
	relay.RejectConnection = append(relay.RejectConnection, BadIP)

	log.Printf("running relay on %s", relay.Address)
	if err := relay.StartAndServe(ctx); err != nil {
		panic(err)
	}
}

func BadIP(r *http.Request) error {
	IP := rely.IP(r)
	if slices.Contains(blackList, IP) {
		return fmt.Errorf("IP is blacklisted")
	}

	blackList = append(blackList, IP)
	return nil
}

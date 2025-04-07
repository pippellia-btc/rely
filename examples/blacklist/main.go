package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
This relay performs IP-based blacklisting, triggered by certain events.
When a client sends a certain event kind, the relay disconnects it and adds its
IP address to a blacklist.
*/

var blacklist []string

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	go rely.HandleSignals(cancel)

	relay := rely.NewRelay()
	relay.RejectConnection = append(relay.RejectConnection, BadIP)
	relay.RejectEvent = append(relay.RejectEvent, Kind666)

	addr := "localhost:3334"
	log.Printf("running relay on %s", addr)

	if err := relay.StartAndServe(ctx, addr); err != nil {
		panic(err)
	}
}

func BadIP(s rely.Stats, req *http.Request) error {
	IP := rely.IP(req)
	if slices.Contains(blacklist, IP) {
		return fmt.Errorf("you are not welcome here")
	}
	return nil
}

func Kind666(client *rely.Client, event *nostr.Event) error {
	if event.Kind == 666 {
		// disconnect the client and return an error
		blacklist = append(blacklist, client.IP())
		client.Disconnect()
		return errors.New("not today, Satan. Not today")
	}
	return nil
}

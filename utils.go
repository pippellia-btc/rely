package rely

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nbd-wtf/go-nostr"
)

// BadID returns an error if the event's ID is invalid
func BadID(c *Client, e *nostr.Event) error {
	if !e.CheckID() {
		return ErrInvalidEventID
	}
	return nil
}

// BadSignature returns an error if the event's signature is invalid.
func BadSignature(c *Client, e *nostr.Event) error {
	match, err := e.CheckSignature()
	if !match {
		if err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidEventSignature, err.Error())
		}
		return ErrInvalidEventSignature
	}

	return nil
}

// Extracts the IP address from the http request.
func IP(r *http.Request) string {
	if IP := r.Header.Get("X-Real-IP"); IP != "" {
		return IP
	}

	if IPs := r.Header.Get("X-Forwarded-For"); IPs != "" {
		first := strings.Split(IPs, ",")[0]
		return strings.TrimSpace(first)
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // fallback: return as-is
	}

	return host
}

func logEvent(c *Client, e *nostr.Event) error {
	log.Printf("received eventID %s from IP %s", e.ID, c.IP)
	return nil
}

func logFilters(ctx context.Context, c *Client, f nostr.Filters) ([]nostr.Event, error) {
	log.Printf("received %d filters from IP %s", len(f), c.IP)
	return nil, nil
}

// HandleSignals listens to os signals, and then fires the cancel() function.
// This cancels the associated context, propagating the signal to the rest of the program.
func HandleSignals(cancel context.CancelFunc) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan
	cancel()
}

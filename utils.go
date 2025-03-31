package rely

import (
	"fmt"
	"net"
	"net/http"
	"strings"

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

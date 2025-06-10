package rely

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

// InvalidID returns an error if the event's ID is invalid
func InvalidID(c Client, e *nostr.Event) error {
	if !e.CheckID() {
		return ErrInvalidEventID
	}
	return nil
}

// InvalidSignature returns an error if the event's signature is invalid.
func InvalidSignature(c Client, e *nostr.Event) error {
	match, err := e.CheckSignature()
	if !match {
		if err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidEventSignature, err.Error())
		}
		return ErrInvalidEventSignature
	}

	return nil
}

// RecentFailure returns an error if a client registration has recently failed.
func RecentFailure(s Stats, r *http.Request) error {
	if time.Since(s.LastRegistrationFail()) < 2*time.Second {
		return ErrOverloaded
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

// Display important statistics of the relay while it's running.
// Example usage: go rely.DisplayStats(ctx, relay)
func DisplayStats(ctx context.Context, r *Relay) {
	const statsLines = 9
	var first = true

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			if !first {
				// clear stats, then print
				fmt.Printf("\033[%dA", statsLines)
				fmt.Print("\033[J")
			}

			r.PrintStats()
			first = false
		}
	}
}

// Print important stats of the relay while it's running.
func (r *Relay) PrintStats() {
	goroutines := runtime.NumGoroutine()
	memStats := new(runtime.MemStats)
	runtime.ReadMemStats(memStats)

	fmt.Println("---------------- stats ----------------")
	fmt.Printf("memory: %.2f MB\n", float64(memStats.Alloc)/(1024*1024))
	fmt.Printf("goroutines: %d\n", goroutines)
	fmt.Printf("active clients: %d\n", r.clientsCount.Load())
	fmt.Printf("processing queue: %d/%d\n", len(r.queue), cap(r.queue))
	fmt.Printf("broadcast queue: %d/%d\n", len(r.broadcast), cap(r.broadcast))
	fmt.Printf("register channel: %d/%d\n", len(r.register), cap(r.register))
	fmt.Printf("unregister channel: %d/%d\n", len(r.unregister), cap(r.unregister))
	fmt.Println("---------------------------------------")
}

// HandleSignals listens to os signals, and then fires the cancel() function.
// This cancels the associated context, propagating the signal to the rest of the program.
func HandleSignals(cancel context.CancelFunc) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan
	cancel()
}

func isUnexpectedClose(err error) bool {
	return websocket.IsUnexpectedCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseAbnormalClosure)
}

func logEvent(c Client, e *nostr.Event) error {
	log.Printf("received eventID %s from IP %s", e.ID, c.IP())
	return nil
}

func logFilters(ctx context.Context, c Client, f nostr.Filters) ([]nostr.Event, error) {
	log.Printf("received %d filters from IP %s", len(f), c.IP())
	return nil, nil
}

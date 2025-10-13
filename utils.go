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
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidEventSignature, err.Error())
	}

	if !match {
		return ErrInvalidEventSignature
	}
	return nil
}

// RegistrationFailWithin returns a RejectConnection function that errs
// if a client registration has failed within the given duration.
func RegistrationFailWithin(d time.Duration) func(Stats, *http.Request) error {
	return func(s Stats, r *http.Request) error {
		if time.Since(s.LastRegistrationFail()) < d {
			return ErrOverloaded
		}
		return nil
	}
}

func DisconnectOnDrops(maxDropped int) func(c Client) {
	return func(c Client) {
		if c.DroppedResponses() > maxDropped {
			c.SendNotice("too many dropped responses, disconnecting")
			c.Disconnect()
		}
	}
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

// ApplyBudget adjusts the Limit of each filter in-place so that the total does not exceed the given budget.
// Filters with limits <= budget / len(filters) are preserved, while larger ones are scaled down proportionally.
// It panics if budget is negative.
func ApplyBudget(budget int, filters ...nostr.Filter) {
	if budget < 0 {
		panic("rely.ApplyBudget: budget should not be negative")
	}

	var used int
	for i := range filters {
		if filters[i].LimitZero {
			filters[i].Limit = 0 // ensure consistency
		}

		if !filters[i].LimitZero && filters[i].Limit < 1 {
			filters[i].Limit = budget // limit is unspecified (or negative), so we set it equal to the budget
		}

		used += filters[i].Limit
	}

	if used > budget {
		// modify filters based on whether they have a limit lower or higher than budget / len(filters).
		// 	- lowers: do nothing
		//	- highers: linearly scale their limit

		fair := budget / len(filters)
		var sumHighers int
		var highers []int

		for i := range filters {
			limit := filters[i].Limit
			if limit > fair {
				highers = append(highers, i)
				sumHighers += limit
			} else {
				budget -= limit
			}
		}

		scalingFactor := float64(budget) / float64(sumHighers)
		for _, idx := range highers {
			limit := float64(filters[idx].Limit)
			filters[idx].Limit = int(scalingFactor*limit + 0.5)
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
	fmt.Printf("active clients: %d\n", r.Clients())
	fmt.Printf("active subscriptions: %d\n", r.Subscriptions())
	fmt.Printf("active filters: %d\n", r.Filters())
	fmt.Printf("processing queue: %d/%d\n", len(r.queue), cap(r.queue))
	fmt.Printf("broadcast event channel: %d/%d\n", len(r.broadcastEvent), cap(r.broadcastEvent))
	fmt.Printf("register client channel: %d/%d\n", len(r.registerClient), cap(r.registerClient))
	fmt.Printf("unregister client channel: %d/%d\n", len(r.unregisterClient), cap(r.unregisterClient))
	fmt.Printf("open subscription channel: %d/%d\n", len(r.openSubscription), cap(r.openSubscription))
	fmt.Printf("close subscription channel: %d/%d\n", len(r.closeSubscription), cap(r.closeSubscription))
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
		websocket.CloseNoStatusReceived,
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

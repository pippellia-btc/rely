package tests

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
	. "github.com/pippellia-btc/rely"
)

var (
	rg               *rand.Rand
	httpRequests     atomic.Int32
	abnormalClosures atomic.Int32
	clients          atomic.Int32
	events           atomic.Int32
	reqs             atomic.Int32
	counts           atomic.Int32
)

const (
	clientDisconnectProbability float32 = 0.01
	clientFailProbability       float32 = 0.01
	relayFailProbability        float32 = 0.01

	TestDuration = 500 * time.Second
)

func TestRandom(t *testing.T) {
	var addr = "localhost:3334"
	var errChan = make(chan error, 10)

	seed := uint64(time.Now().Unix())
	rg = rand.New(rand.NewPCG(0, seed))

	t.Run(fmt.Sprintf("seed__PCG(0,%d)", seed), func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), TestDuration)
		defer cancel()

		relay := NewRelay(
			WithQueueCapacity(10000),
			WithMaxProcessors(10),
			WithoutPressureLogs(),
		)

		relay.OnConnect = dummyOnConnect
		relay.OnEvent = dummyOnEvent
		relay.OnReq = dummyOnReq
		relay.OnCount = dummyOnCount

		go displayStats(ctx, relay)
		go clientMadness(ctx, errChan, addr)
		go func() { errChan <- relay.StartAndServe(ctx, addr) }()

		select {
		case err := <-errChan:
			t.Fatal(err)

		case <-ctx.Done():
			// test passed
		}
	})
}

func dummyOnConnect(c rely.Client) {
	clients.Add(1)
	if rg.Float32() < relayFailProbability {
		c.Disconnect()
		return
	}

	fibonacci(25) // simulate some work
}

func dummyOnEvent(c rely.Client, e *nostr.Event) error {
	events.Add(1)
	if rg.Float32() < relayFailProbability {
		c.Disconnect()
		return fmt.Errorf("failed")
	}

	fibonacci(25) // simulate some work
	return nil
}

func dummyOnReq(ctx context.Context, c rely.Client, f nostr.Filters) ([]nostr.Event, error) {
	reqs.Add(1)
	if rg.Float32() < relayFailProbability {
		c.Disconnect()
		return nil, fmt.Errorf("failed")
	}

	fibonacci(25) // simulate some work
	return randomSlice(100, randomEvent), nil
}
func dummyOnCount(ctx context.Context, c rely.Client, f nostr.Filters) (int64, bool, error) {
	counts.Add(1)
	if rg.Float32() < relayFailProbability {
		c.Disconnect()
		return 0, false, fmt.Errorf("failed")
	}

	fibonacci(25) // simulate some work
	return rg.Int64(), true, nil
}

type client struct {
	conn    *websocket.Conn
	errChan chan error

	generateRequest  func() ([]byte, error)
	validateResponse func([]byte) error
}

func newClient(conn *websocket.Conn, errChan chan error) *client {
	switch rg.IntN(3) {
	case 0:
		// client that generates EVENTs
		return &client{
			conn:             conn,
			errChan:          errChan,
			generateRequest:  randomeventRequest,
			validateResponse: validateLabel([]string{"OK"}),
		}

	case 1:
		// client that generates REQs
		return &client{
			conn:             conn,
			errChan:          errChan,
			generateRequest:  randomReqRequest,
			validateResponse: validateLabel([]string{"EOSE", "CLOSED", "EVENT"}),
		}

	default:
		// client that generates COUNTs
		return &client{
			conn:             conn,
			errChan:          errChan,
			generateRequest:  randomCountRequest,
			validateResponse: validateLabel([]string{"CLOSED", "COUNT"}),
		}
	}
}

func clientMadness(
	ctx context.Context,
	errChan chan error,
	URL string) {

	// decrease timeout to trigger mass disconnections at the end of the test
	ctx, cancel := context.WithTimeout(ctx, TestDuration-5*time.Second)
	defer cancel()

	if !strings.HasPrefix(URL, "ws://") {
		URL = "ws://" + URL
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			httpRequests.Add(1)

			conn, resp, err := websocket.DefaultDialer.Dial(URL, nil)
			if err != nil {
				if resp != nil {
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()

					message := strings.TrimSpace(string(body))
					if message == rely.ErrOverloaded.Error() {
						// the server is rejecting http requests because it's overloaded
						continue
					}
				}

				errChan <- fmt.Errorf("failed to connect with websocket: %w", err)
				return
			}

			clientCtx, cancel := context.WithCancel(ctx)
			client := newClient(conn, errChan)
			go client.write(clientCtx, cancel)
			go client.read(clientCtx, cancel)
		}
	}
}

func (c *client) write(
	ctx context.Context,
	cancel context.CancelFunc) {

	pingTicker := time.NewTicker(rely.DefaultPingPeriod)
	writeTicker := time.NewTicker(time.Second)

	defer func() {
		cancel()
		c.conn.Close()
		pingTicker.Stop()
		writeTicker.Stop()
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case <-writeTicker.C:
			if rg.Float32() < clientDisconnectProbability {
				// randomly disconnect
				return
			}

			data, err := c.generateRequest()
			if err != nil {
				c.errChan <- err
				return
			}

			c.conn.SetWriteDeadline(time.Now().Add(rely.DefaultWriteWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				if IsBadError(err) {
					c.errChan <- fmt.Errorf("failed to write: %w", err)
				}
				return
			}

		case <-pingTicker.C:
			c.conn.SetWriteDeadline(time.Now().Add(rely.DefaultWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				if IsBadError(err) {
					c.errChan <- fmt.Errorf("failed to ping: %w", err)
				}
				return
			}
		}
	}
}

func (c *client) read(
	ctx context.Context,
	cancel context.CancelFunc) {

	defer func() {
		cancel()
		c.conn.Close()
	}()

	c.conn.SetReadLimit(rely.DefaultMaxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(rely.DefaultPongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(rely.DefaultPongWait)); return nil })

	for {
		select {
		case <-ctx.Done():
			return

		default:
			_, data, err := c.conn.ReadMessage()
			if err != nil {
				if IsBadError(err) {
					c.errChan <- fmt.Errorf("failed to read: %w", err)
				}
				return
			}

			if err := c.validateResponse(data); err != nil {
				c.errChan <- err
				return
			}
		}
	}
}

func displayStats(ctx context.Context, r *rely.Relay) {
	const statsLines = 16
	var first = true

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:

			if !first {
				// clear stats
				fmt.Printf("\033[%dA", statsLines)
				fmt.Print("\033[J")
			}

			fmt.Println("---------------- test -----------------")
			fmt.Printf("total http requests: %d\n", httpRequests.Load())
			fmt.Printf("abnormal closures: %d\n", abnormalClosures.Load())
			fmt.Printf("total clients: %d\n", clients.Load())
			fmt.Printf("processed events: %d\n", events.Load())
			fmt.Printf("processed reqs: %d\n", reqs.Load())
			fmt.Printf("processed counts: %d\n", counts.Load())
			r.PrintStats()
			first = false
		}
	}
}

func IsBadError(err error) bool {
	switch {
	case websocket.IsCloseError(err, websocket.CloseAbnormalClosure):
		abnormalClosures.Add(1)
		return false

	case websocket.IsUnexpectedCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseTryAgainLater,
		websocket.CloseAbnormalClosure):
		return true

	default:
		return false
	}
}

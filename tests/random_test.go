package tests

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

var (
	rg            *rand.Rand
	clientCounter atomic.Int32
	eventCounter  atomic.Int32
	filterCounter atomic.Int32
)

const (
	clientDisconnectProbability float32 = 0.01
	clientFailProbability       float32 = 0.01
	relayFailProbability        float32 = 0.01
)

func TestRandom(t *testing.T) {
	const duration = 100 * time.Second
	//var seed uint32 = time.Now().Unix()
	var addr = "localhost:3334"
	var seed uint64 = 1743533255
	rg = rand.New(rand.NewPCG(0, seed))

	t.Run(fmt.Sprintf("seed__PCG(0,%d)", seed), func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), duration)
		defer cancel()

		relay := rely.NewRelay()
		relay.OnConnect = dummyOnConnect
		relay.OnEvent = dummyOnEvent
		relay.OnFilters = dummyOnFilters

		errChan := make(chan error, 10)
		go displayStats(ctx, relay)
		go clientMadness(ctx, errChan, addr)

		go func() {
			errChan <- relay.StartAndServe(ctx, addr)
		}()

		select {
		case err := <-errChan:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			// test passed
		}
	})
}

func dummyOnConnect(c *rely.Client) error {
	clientCounter.Add(1)

	if rg.Float32() < relayFailProbability {
		c.Disconnect()
		return nil
	}

	_ = fibonacci(30) // simulate some work
	return nil
}

func dummyOnEvent(c *rely.Client, e *nostr.Event) error {
	eventCounter.Add(1)

	if rg.Float32() < relayFailProbability {
		return fmt.Errorf("failed")
	}

	_ = fibonacci(30) // simulate some work
	return nil
}

func dummyOnFilters(ctx context.Context, c *rely.Client, f nostr.Filters) ([]nostr.Event, error) {
	filterCounter.Add(int32(len(f)))

	if rg.Float32() < relayFailProbability {
		return nil, fmt.Errorf("failed")
	}

	_ = fibonacci(30) // simulate some work
	return randomSlice(100, randomEvent), nil
}

func clientMadness(
	ctx context.Context,
	errChan chan error,
	URL string) {

	time.Sleep(1 * time.Second) // to give time to StartAndServe

	if !strings.HasPrefix(URL, "ws://") {
		URL = "ws://" + URL
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			conn, _, err := websocket.DefaultDialer.Dial(URL, nil)
			if err != nil {
				errChan <- fmt.Errorf("failed to connect with websocket: %v", err)
				return
			}

			clientCtx, cancel := context.WithCancel(ctx)
			client := newClient(conn, errChan)
			go client.write(clientCtx, cancel)
			go client.read(clientCtx, cancel)
		}
	}
}

type client struct {
	conn    *websocket.Conn
	errChan chan error

	generateRequest  func() ([]byte, error)
	validateResponse func([]byte) error
}

func newClient(conn *websocket.Conn, errChan chan error) *client {
	switch {
	case rg.Float32() < 0.5:
		// client that generates EVENTs
		return &client{
			conn:             conn,
			errChan:          errChan,
			generateRequest:  randomEventRequest,
			validateResponse: validateResponseAfterEvent,
		}

	default:
		// client that generates REQs
		return &client{
			conn:             conn,
			errChan:          errChan,
			generateRequest:  randomReqRequest,
			validateResponse: validateResponseAfterReq,
		}
	}
}

func (c *client) write(
	ctx context.Context,
	cancel context.CancelFunc) {

	pingTicker := time.NewTicker(rely.DefaultPingPeriod)
	writeTicker := time.NewTicker(500 * time.Millisecond)

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
			data, err := c.generateRequest()
			if err != nil {
				c.errChan <- err
				return
			}

			c.conn.SetWriteDeadline(time.Now().Add(rely.DefaultWriteWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				c.errChan <- fmt.Errorf("failed to write: %w", err)
				return
			}

		case <-pingTicker.C:
			if rg.Float32() < clientDisconnectProbability {
				// randomly disconnect
				return
			}

			c.conn.SetWriteDeadline(time.Now().Add(rely.DefaultWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.errChan <- fmt.Errorf("ping failed: %v", err)
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
				if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure) {
					c.errChan <- err
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

func validateResponseAfterEvent(data []byte) error {
	label, _, err := rely.JSONArray(data)
	if err != nil {
		return fmt.Errorf("%w: data '%v'", err, string(data))
	}

	if label != "OK" {
		return fmt.Errorf("label is not the expected 'OK': %v", string(data))
	}

	return nil
}

func validateResponseAfterReq(data []byte) error {
	label, _, err := rely.JSONArray(data)
	if err != nil {
		return fmt.Errorf("%w: data '%v'", err, string(data))
	}

	expected := []string{"EOSE", "CLOSED", "EVENT"}
	if !slices.Contains(expected, label) {
		return fmt.Errorf("label is not among the expected labels %v: data %v", expected, string(data))
	}

	return nil
}

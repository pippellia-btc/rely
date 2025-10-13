package tests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"runtime"
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
	start            time.Time
	httpRequests     atomic.Int64
	nostrRequests    atomic.Int64
	dataSent         atomic.Int64
	clients          atomic.Int64
	processed        atomic.Int64
	abnormalClosures atomic.Int64
)

const (
	testDuration        time.Duration = 500 * time.Second
	relayDuration       time.Duration = testDuration * 98 / 10
	attackDuration      time.Duration = testDuration * 96 / 10
	connectionFrequency time.Duration = 2 * time.Millisecond

	relayFailProbability        float32 = 0.05
	relayDisconnectProbability  float32 = 0.01
	clientDisconnectProbability float32 = 0.01
)

func TestRandom(t *testing.T) {
	start = time.Now()
	addr := "localhost:3334"
	errChan := make(chan error, 10)

	t.Run(fmt.Sprintf("seed__PCG(0,%d)", seed), func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testDuration)
		defer cancel()

		relay := rely.NewRelay(
			rely.WithoutPressureLogs(),
		)

		relay.On.Connect = dummyOnConnect
		relay.On.Event = dummyOnEvent
		relay.On.Req = dummyOnReq
		relay.On.Count = dummyOnCount

		go func() { http.ListenAndServe(":6060", nil) }()

		go func() {
			ctx, cancel := context.WithTimeout(ctx, relayDuration)
			defer cancel()
			go displayStats(ctx, relay)
			if err := relay.StartAndServe(ctx, addr); err != nil {
				errChan <- err
			}
		}()

		go func() {
			ctx, cancel := context.WithTimeout(ctx, attackDuration)
			defer cancel()
			clientMadness(ctx, errChan, addr)
		}()

		select {
		case err := <-errChan:
			t.Fatal(err)

		case <-ctx.Done():
			// test passed, print stats last time
			clearScreen()
			printStats(relay)
		}
	})
}

func dummyOnConnect(c rely.Client) {
	clients.Add(1)
	if rg.Float32() < relayDisconnectProbability {
		c.Disconnect()
		return
	}
}

func dummyOnEvent(c rely.Client, e *nostr.Event) error {
	processed.Add(1)
	if rg.Float32() < relayFailProbability {
		return errors.New("failed")
	}

	if rg.Float32() < relayDisconnectProbability {
		c.Disconnect()
	}
	return nil
}

func dummyOnReq(ctx context.Context, c rely.Client, f nostr.Filters) ([]nostr.Event, error) {
	processed.Add(1)
	if rg.Float32() < relayFailProbability {
		return nil, errors.New("failed")
	}

	if rg.Float32() < relayDisconnectProbability {
		c.Disconnect()
	}
	return nil, nil
}
func dummyOnCount(ctx context.Context, c rely.Client, f nostr.Filters) (int64, bool, error) {
	processed.Add(1)
	if rg.Float32() < relayFailProbability {
		return 0, false, errors.New("failed")
	}

	if rg.Float32() < relayDisconnectProbability {
		c.Disconnect()
	}
	return rg.Int64(), true, nil
}

type client struct {
	conn    *websocket.Conn
	errChan chan error

	generateRequest  func() []byte
	validateResponse func(*json.Decoder) error
}

func newClient(conn *websocket.Conn, errChan chan error) *client {
	switch rg.IntN(3) {
	case 0:
		// client that generates EVENTs
		return &client{
			conn:             conn,
			errChan:          errChan,
			generateRequest:  quickEventRequest,
			validateResponse: validateLabel([]string{"OK", "NOTICE"}),
		}

	case 1:
		// client that generates REQs
		return &client{
			conn:             conn,
			errChan:          errChan,
			generateRequest:  quickReqRequest,
			validateResponse: validateLabel([]string{"EOSE", "CLOSED", "EVENT", "NOTICE"}),
		}

	default:
		// client that generates COUNTs
		return &client{
			conn:             conn,
			errChan:          errChan,
			generateRequest:  quickCountRequest,
			validateResponse: validateLabel([]string{"CLOSED", "COUNT", "NOTICE"}),
		}
	}
}

func clientMadness(
	ctx context.Context,
	errChan chan error,
	URL string) {

	if !strings.HasPrefix(URL, "ws://") {
		URL = "ws://" + URL
	}

	ticker := time.NewTicker(connectionFrequency)
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

func (c *client) write(ctx context.Context, cancel context.CancelFunc) {
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

			data := c.generateRequest()
			size := int64(len(data))

			c.conn.SetWriteDeadline(time.Now().Add(rely.DefaultWriteWait))
			err := c.conn.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				if IsBadError(err) {
					c.errChan <- fmt.Errorf("failed to write: %w", err)
				}
				return
			}

			nostrRequests.Add(1)
			dataSent.Add(size)

		case <-pingTicker.C:
			c.conn.SetWriteDeadline(time.Now().Add(rely.DefaultWriteWait))
			err := c.conn.WriteMessage(websocket.PingMessage, nil)
			if err != nil {
				if IsBadError(err) {
					c.errChan <- fmt.Errorf("failed to ping: %w", err)
				}
				return
			}
		}
	}
}

func (c *client) read(ctx context.Context, cancel context.CancelFunc) {
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
			_, reader, err := c.conn.NextReader()
			if err != nil {
				if IsBadError(err) {
					c.errChan <- fmt.Errorf("failed to read: %w", err)
				}
				return
			}

			decoder := json.NewDecoder(reader)
			if err := c.validateResponse(decoder); err != nil {
				c.errChan <- err
				return
			}
		}
	}
}

func validateLabel(labels []string) func(d *json.Decoder) error {
	return func(d *json.Decoder) error {
		label, err := parseLabel(d)
		if err != nil {
			return err
		}

		if !slices.Contains(labels, label) {
			return fmt.Errorf("label is not among the expected labels %v: %s", labels, label)
		}
		return nil
	}
}

func parseLabel(d *json.Decoder) (string, error) {
	token, err := d.Token()
	if err != nil {
		return "", fmt.Errorf("expected start of array '[', got: %w", err)
	}

	if token != json.Delim('[') {
		return "", fmt.Errorf("expected start of array '[', got: %v", token)
	}

	var label string
	if err := d.Decode(&label); err != nil {
		return "", fmt.Errorf("failed to read label: %w", err)
	}

	return label, nil
}

const statsLines = 23

func displayStats(ctx context.Context, r *rely.Relay) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			clearScreen()
			printStats(r)
		}
	}
}

func printStats(r *rely.Relay) {
	fmt.Println("---------------- test -----------------")
	fmt.Printf("test time: %v\n", time.Since(start))
	fmt.Printf("test seed: %v\n", seed)
	fmt.Println("---------------------------------------")
	fmt.Printf("total http requests: %d\n", httpRequests.Load())
	fmt.Printf("total data sent: %.2f MB \n", float64(dataSent.Load())/(1024*1024))
	fmt.Printf("total nostr requests: %d\n", nostrRequests.Load())
	fmt.Printf("processed nostr requests: %d\n", processed.Load())
	fmt.Printf("abnormal closures: %d\n", abnormalClosures.Load())
	fmt.Printf("total clients: %d\n", clients.Load())
	r.PrintStats()
}

func clearScreen() {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "cls")
	default:
		// Linux, macOS, etc..
		cmd = exec.Command("clear")
	}

	cmd.Stdout = os.Stdout
	cmd.Run()
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

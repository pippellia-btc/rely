package rely

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"

	ws "github.com/gorilla/websocket"
	"fiatjaf.com/nostr/nip11"
)

const (
	writeWait      time.Duration = 10 * time.Second
	pongWait       time.Duration = 60 * time.Second
	pingPeriod     time.Duration = 45 * time.Second
	maxMessageSize int64         = 500000 // 0.5MB
	bufferSize     int           = 1024   // 1KB
)

type Option func(*Relay)

// WithDomain sets the relay's official domain name (e.g., "example.com").
// This is mandatory for validating NIP-42 authentication.
// If this is unset, NIP-42 authentication will fail, and a warning will be logged.
func WithDomain(d string) Option {
	return func(r *Relay) { r.domain = strings.TrimSpace(d) }
}

// WithInfo sets a custom NIP-11 (Relay Information Document) JSON body returned
// when a request includes `Accept: application/nostr+json`.
// If not set, a default document is used.
func WithInfo(info nip11.RelayInformationDocument) Option {
	return func(r *Relay) {
		json, err := json.Marshal(info)
		if err != nil {
			panic("failed to marshal NIP-11 document: " + err.Error())
		}

		r.info = json
	}
}

// WithLogger sets the structured logger (*slog.Logger) used by the relay for all logging operations.
// If not set, a default logger will be used.
func WithLogger(l *slog.Logger) Option {
	return func(r *Relay) { r.log = l }
}

// WithQueueCapacity sets the capacity of the internal channel used to queue incoming requests
// before they are processed by the worker pool. A larger capacity can help smooth out bursts
// of client activity.
func WithQueueCapacity(c int) Option {
	return func(r *Relay) { r.processor.queue = make(chan request, c) }
}

// WithMaxProcessors sets the maximum number of concurrent workers (goroutines) used
// to process incoming client requests (EVENTs, REQs, COUNTs) from the internal queue.
// Must be greater than 0.
func WithMaxProcessors(n int) Option {
	return func(r *Relay) { r.processor.maxWorkers = n }
}

// WithClientResponseLimit sets the maximum number of responses that can be buffered and sent
// to a single client connection before backpressure is applied. Must be greater than 0.
//
// For each REQ, the framework dynamically adjusts the "limit" field across all filters
// to be less than the remaining capacity of the client's response channel:
//
//	sum filter's limit <= responseLimit - len(client.responses)
//
// This ensures that the total number of events returned never exceeds what can be buffered
// and sent to the client, enforcing per-client backpressure and preventing overproduction of responses.
func WithClientResponseLimit(n int) Option {
	return func(r *Relay) { r.responseLimit = n }
}

// WithReadBufferSize sets the read buffer size (in bytes) for the underlying websocket connection upgrader.
func WithReadBufferSize(s int) Option {
	return func(r *Relay) { r.upgrader.ReadBufferSize = s }
}

// WithWriteBufferSize sets the write buffer size (in bytes) for the underlying websocket connection upgrader.
func WithWriteBufferSize(s int) Option {
	return func(r *Relay) { r.upgrader.WriteBufferSize = s }
}

// WithWriteWait sets the maximum duration to wait for a websocket write operation (including control messages)
// to complete before timing out and closing the connection. Must be greater than 1s.
func WithWriteWait(d time.Duration) Option {
	return func(r *Relay) { r.writeWait = d }
}

// WithPongWait sets the read deadline for waiting for the next Pong message from the client
// after a Ping is sent. Must be greater than the ping period.
func WithPongWait(d time.Duration) Option {
	return func(r *Relay) { r.pongWait = d }
}

// WithPingPeriod sets the interval at which the relay sends Ping messages to the client
// to keep the connection alive. Must be less than the pong wait and greater than 1s.
func WithPingPeriod(d time.Duration) Option {
	return func(r *Relay) { r.pingPeriod = d }
}

// WithMaxMessageSize sets the maximum size (in bytes) of a single incoming websocket message
// (e.g., a Nostr EVENT or REQ). Messages larger than this will be rejected. Must be > 512 bytes.
func WithMaxMessageSize(s int64) Option {
	return func(r *Relay) { r.maxMessageSize = s }
}

type systemSettings struct {
	// the maximum number of responses sent to a client at once.
	// To specify it, use [WithClientResponseLimit].
	//
	// For each REQ, the framework dynamically adjusts the combined budget across all filters
	// to match the remaining capacity of the client's response channel:
	//
	//     responseLimit - len(client.responses)
	//
	// This ensures that the total number of events returned never exceeds what can be buffered
	// and sent to the client, enforcing per-client backpressure and preventing overproduction of responses.
	responseLimit int

	// the relay domain name (e.g., "example.com") used to validate the NIP-42 "relay" tag.
	// It should be explicitly set with [WithDomain]; if unset, a warning will be logged and NIP-42 will fail.
	domain string

	// the NIP-11 relay info document json. To specify it, use [WithInfo].
	info []byte
}

func newSystemSettings() systemSettings {
	return systemSettings{
		responseLimit: 1000,
		info:          newRelayInfo(),
	}
}

func newRelayInfo() []byte {
	info := nip11.RelayInformationDocument{
		Software:      "https://github.com/pippellia-btc/rely",
		SupportedNIPs: []any{1, 11, 42},
	}

	json, err := json.Marshal(info)
	if err != nil {
		panic("failed to marshal default NIP-11 document: " + err.Error())
	}

	return json
}

type websocketSettings struct {
	upgrader       ws.Upgrader
	writeWait      time.Duration
	pongWait       time.Duration
	pingPeriod     time.Duration
	maxMessageSize int64
}

func newWebsocketSettings() websocketSettings {
	return websocketSettings{
		upgrader: ws.Upgrader{
			ReadBufferSize:  bufferSize,
			WriteBufferSize: bufferSize,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		writeWait:      writeWait,
		pongWait:       pongWait,
		pingPeriod:     pingPeriod,
		maxMessageSize: maxMessageSize,
	}
}

// validate panics if structural parameters are invalid, and logs warnings
// for non-fatal but potentially misconfigured settings (e.g., missing domain).
func (r *Relay) validate() {
	if r.pingPeriod < 1*time.Second {
		panic("ping period must be greater than 1s to function reliably")
	}

	if r.pongWait <= r.pingPeriod {
		panic("pong wait must be greater than ping period to function reliably")
	}

	if r.writeWait < 1*time.Second {
		panic("write wait must be greater than 1s to function reliably")
	}

	if r.maxMessageSize < 512 {
		panic("max message size must be greater than 512 bytes to accept nostr events")
	}

	if r.processor.maxWorkers < 1 {
		panic("max processors must be greater than 1 to correctly process from the queue")
	}

	if r.responseLimit < 1 {
		panic("client response limit must be greater than 1 to allow responses to be sent")
	}

	if r.domain == "" {
		r.log.Warn("you must set the relay's domain to validate NIP-42 auth")
	}
}

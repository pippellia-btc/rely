package rely

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr/nip11"
)

const (
	DefaultWriteWait      time.Duration = 10 * time.Second
	DefaultPongWait       time.Duration = 60 * time.Second
	DefaultPingPeriod     time.Duration = 45 * time.Second
	DefaultMaxMessageSize int64         = 500000 // 0.5MB
	DefaultBufferSize     int           = 1024   // 1KB
)

type Option func(*Relay)

type systemOptions struct {
	// the maximum number of concurrent processors consuming from the [Relay.queue].
	// To specify it, use [WithMaxProcessors]
	maxProcessors int

	// the relay domain name (e.g., "example.com") used to validate the NIP-42 "relay" tag.
	// It should be explicitly set with [WithDomain]; if unset, a warning will be logged and NIP-42 will fail.
	domain string

	// logOverload non-fatal internal conditions such as dropped events or failed client
	// registrations due to full channels. Set it to true with [WithOverloadLogs].
	logOverload bool
}

func newSystemOptions() systemOptions {
	return systemOptions{maxProcessors: 4}
}

func WithMaxProcessors(n int) Option { return func(r *Relay) { r.maxProcessors = n } }
func WithDomain(d string) Option     { return func(r *Relay) { r.domain = strings.TrimSpace(d) } }
func WithOverloadLogs() Option       { return func(r *Relay) { r.logOverload = true } }
func WithQueueCapacity(c int) Option { return func(r *Relay) { r.queue = make(chan request, c) } }

type websocketOptions struct {
	upgrader       websocket.Upgrader
	writeWait      time.Duration
	pongWait       time.Duration
	pingPeriod     time.Duration
	maxMessageSize int64
}

func newWebsocketOptions() websocketOptions {
	return websocketOptions{
		upgrader: websocket.Upgrader{
			ReadBufferSize:  DefaultBufferSize,
			WriteBufferSize: DefaultBufferSize,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		writeWait:      DefaultWriteWait,
		pongWait:       DefaultPongWait,
		pingPeriod:     DefaultPingPeriod,
		maxMessageSize: DefaultMaxMessageSize,
	}
}

func WithReadBufferSize(s int) Option       { return func(r *Relay) { r.upgrader.ReadBufferSize = s } }
func WithWriteBufferSize(s int) Option      { return func(r *Relay) { r.upgrader.WriteBufferSize = s } }
func WithWriteWait(d time.Duration) Option  { return func(r *Relay) { r.writeWait = d } }
func WithPongWait(d time.Duration) Option   { return func(r *Relay) { r.pongWait = d } }
func WithPingPeriod(d time.Duration) Option { return func(r *Relay) { r.pingPeriod = d } }
func WithMaxMessageSize(s int64) Option     { return func(r *Relay) { r.maxMessageSize = s } }

func newRelayInfo() []byte {
	info := nip11.RelayInformationDocument{
		Software:      "https://github.com/pippellia-btc/rely",
		SupportedNIPs: []any{1, 11, 42},
	}

	json, err := json.Marshal(info)
	if err != nil {
		panic("failed to marshal NIP-11 document: " + err.Error())
	}
	return json
}

func WithInfo(info nip11.RelayInformationDocument) Option {
	return func(r *Relay) {
		json, err := json.Marshal(info)
		if err != nil {
			panic("failed to marshal NIP-11 document: " + err.Error())
		}

		r.info = json
	}
}

// validate panics if structural parameters are invalid, and logs warnings
// for non-fatal but potentially misconfigured settings (e.g., missing domain).
func (r *Relay) validate() {
	if r.pingPeriod < 1*time.Second {
		panic("ping period must be greater than 1s")
	}

	if r.pongWait <= r.pingPeriod {
		panic("pong wait must be greater than ping period")
	}

	if r.writeWait < 1*time.Second {
		panic("write wait must be greater than 1s")
	}

	if r.maxMessageSize < 512 {
		panic("max message size must be greater than 512 bytes to accept nostr events")
	}

	if r.maxProcessors < 1 {
		panic("max processors must be greater than 1 to correctly process from the queue")
	}

	if r.domain == "" {
		log.Println("WARN: you must set the relay's domain to validate NIP-42 auth")
	}
}

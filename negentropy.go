package rely

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip77/negentropy"
	"github.com/nbd-wtf/go-nostr/nip77/negentropy/storage/vector"
)

// NegentropyItem is the metadata needed to reconcile a stored event.
type NegentropyItem struct {
	ID        string
	Timestamp nostr.Timestamp
}

// NegentropyConfig bounds the resources used by NIP-77 responder sessions.
// These limits do not enable NIP-77; set On.Negentropy to supply stored items.
type NegentropyConfig struct {
	MaxSessions    int           // concurrent sessions per connection
	MaxItems       int           // maximum items in each snapshot
	Timeout        time.Duration // maximum session lifetime, including snapshot loading
	FrameSizeLimit int           // binary response size; hex encoding doubles this on the wire
}

func DefaultNegentropyConfig() NegentropyConfig {
	return NegentropyConfig{
		MaxSessions: 4, MaxItems: 100_000, Timeout: time.Minute, FrameSizeLimit: 4096,
	}
}

// WithNegentropyConfig configures NIP-77 resource limits.
func WithNegentropyConfig(config NegentropyConfig) Option {
	return func(r *Relay) { r.settings.Negentropy = config }
}

func (c NegentropyConfig) validate() {
	if c.MaxSessions < 1 || c.MaxItems < 1 || c.Timeout <= 0 || c.FrameSizeLimit < 4096 {
		panic("invalid negentropy config: positive session/item limits and timeout, and frame size >= 4096 required")
	}
}

type negSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	engine *negentropy.Negentropy // used by one processor at a time
	busy   bool                   // guarded by client.mu
}

type negRequest struct {
	client  *client
	id      string
	session *negSession
	filter  nostr.Filter
	message string
	open    bool
}

func (r negRequest) UID() string     { return join(r.client.uid, r.id) }
func (r negRequest) ID() string      { return r.id }
func (r negRequest) IsExpired() bool { return r.session.ctx.Err() != nil }

type negResponse struct {
	label, id, payload string
}

func (r negResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{r.label, r.id, r.payload})
}

// parseNegentropy requires the exact NIP-77 envelope, including its closing
// bracket and end of input. NEG-OPEN accepts exactly one filter.
func parseNegentropy(label string, d *json.Decoder) (negRequest, error) {
	var r negRequest
	if err := d.Decode(&r.id); err != nil || len(r.id) == 0 || len(r.id) > 64 {
		return r, ErrInvalidRequestID
	}
	r.open = label == "NEG-OPEN"
	if r.open {
		var filter *nostr.Filter
		if err := d.Decode(&filter); err != nil || filter == nil {
			return r, errors.New("invalid filter")
		}
		r.filter = *filter
	}
	if label != "NEG-CLOSE" {
		if err := d.Decode(&r.message); err != nil {
			return r, errors.New("invalid reconciliation message")
		}
	}
	token, err := d.Token()
	if err != nil || token != json.Delim(']') {
		return r, errors.New("invalid message length")
	}
	if _, err := d.Token(); err != io.EOF {
		return r, errors.New("unexpected trailing data")
	}
	if label != "NEG-CLOSE" {
		if err := validateNegMessage(r.message); err != nil {
			return r, err
		}
		r.message = strings.ToLower(r.message)
	}
	return r, nil
}

func (c *client) handleNegentropy(label string, d *json.Decoder) {
	r, err := parseNegentropy(label, d)
	if err != nil {
		c.invalidMessages++
		c.failNegentropy(r.id, "invalid: "+err.Error())
		return
	}
	if label == "NEG-CLOSE" {
		c.mu.Lock()
		c.removeNegSession(r.id)
		c.mu.Unlock()
		return
	}
	if r.open {
		// Replacement closes the previous session even when the new query fails.
		c.mu.Lock()
		c.removeNegSession(r.id)
		c.mu.Unlock()
		if c.relay.On.Negentropy == nil {
			c.failNegentropy(r.id, "unsupported: NIP-77 is not enabled")
			return
		}
		for _, reject := range c.relay.Reject.Negentropy {
			if err := reject(c, r.id, r.filter); err != nil {
				c.failNegentropy(r.id, negReason(err))
				return
			}
		}
	}

	c.mu.Lock()
	if c.isUnregistering.Load() {
		c.mu.Unlock()
		return
	}
	if r.open {
		config := c.relay.settings.Negentropy
		if len(c.negSessions) >= config.MaxSessions {
			c.mu.Unlock()
			c.failNegentropy(r.id, "blocked: too many reconciliation sessions")
			return
		}
		if c.negSessions == nil {
			c.negSessions = make(map[string]*negSession)
		}
		ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
		r.session = &negSession{ctx: ctx, cancel: cancel}
		c.negSessions[r.id] = r.session
		context.AfterFunc(ctx, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.negSessions[r.id] == r.session {
				c.removeNegSession(r.id)
				c.sendNegentropy(negResponse{"NEG-ERR", r.id, "closed: reconciliation session timed out"})
			}
		})
	} else {
		r.session = c.negSessions[r.id]
	}
	if r.session == nil || r.session.busy {
		c.removeNegSession(r.id)
		c.sendNegentropy(negResponse{"NEG-ERR", r.id, "closed: session is missing or still processing"})
		c.mu.Unlock()
		return
	}
	r.session.busy = true
	r.client = c
	c.mu.Unlock()

	if err := c.relay.tryProcess(r); err != nil {
		c.failNegentropy(r.id, "blocked: "+err.Error())
	}
}

// removeNegSession requires c.mu. Cancellation does not wait for a storage hook.
func (c *client) removeNegSession(id string) {
	if s := c.negSessions[id]; s != nil {
		delete(c.negSessions, id)
		s.cancel()
	}
}

func (c *client) closeNegSessions() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.negSessions {
		c.removeNegSession(id)
	}
}

func (c *client) failNegentropy(id, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeNegSession(id)
	c.sendNegentropy(negResponse{"NEG-ERR", id, reason})
}

// Reconciliation cannot recover from a silently dropped response. Close the
// socket on overflow; the reader performs cleanup after the caller releases mu.
func (c *client) sendNegentropy(r negResponse) {
	if c.isUnregistering.Load() {
		return
	}
	select {
	case c.responses <- r:
	default:
		c.droppedResponses.Add(1)
		// Closing the socket wakes the reader, which performs normal cleanup.
		c.conn.Close()
	}
}

func (p *processor) processNegentropy(r negRequest) {
	if r.IsExpired() {
		return
	}
	s := r.session
	var err error
	if r.open {
		var items []NegentropyItem
		items, err = p.relay.On.Negentropy(s.ctx, r.client, r.id, r.filter)
		if err == nil && s.ctx.Err() == nil {
			s.engine, err = newNegEngine(items, p.relay.settings.Negentropy)
		}
	}
	if s.ctx.Err() != nil {
		return
	}
	var output string
	if err == nil {
		output, err = s.engine.Reconcile(r.message)
	}
	c := r.client
	c.mu.Lock()
	defer c.mu.Unlock()
	// A close or replacement can race both database loading and reconciliation.
	if c.negSessions[r.id] != s || s.ctx.Err() != nil {
		return
	}
	if err != nil {
		c.removeNegSession(r.id)
		c.sendNegentropy(negResponse{"NEG-ERR", r.id, negReason(err)})
		return
	}
	s.busy = false
	c.sendNegentropy(negResponse{"NEG-MSG", r.id, output})
}

func negReason(err error) string {
	// Preserve NIP-01 machine-readable prefixes supplied by downstream hooks.
	prefix, _, found := strings.Cut(err.Error(), ":")
	if found && prefix != "" && !strings.ContainsAny(prefix, " \t\r\n") {
		return err.Error()
	}
	return "error: " + err.Error()
}

func newNegEngine(items []NegentropyItem, config NegentropyConfig) (*negentropy.Negentropy, error) {
	if len(items) > config.MaxItems {
		return nil, errors.New("blocked: too many records")
	}
	v := vector.New()
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		id, err := hex.DecodeString(item.ID)
		// The pinned engine's encoder supports at most eight base-128 digits.
		if err != nil || len(id) != 32 || item.Timestamp < 0 || item.Timestamp >= maxNegTimestamp {
			return nil, errors.New("error: invalid reconciliation snapshot item")
		}
		key := strings.ToLower(item.ID)
		if _, exists := seen[key]; exists {
			return nil, errors.New("error: duplicate reconciliation snapshot ID")
		}
		seen[key] = struct{}{}
		v.Insert(item.Timestamp, key)
	}
	v.Seal()
	// The pinned engine compares only the first 16 bytes when constructing
	// shortened bounds. Refuse the rare ambiguous case rather than mis-sync.
	var previous negentropy.Item
	for i, item := range v.Range(0, v.Size()) {
		if i > 0 && previous.Timestamp == item.Timestamp && previous.ID[:34] == item.ID[:34] {
			return nil, errors.New("error: snapshot IDs exceed supported common prefix")
		}
		previous = item
	}
	return negentropy.New(v, config.FrameSizeLimit), nil
}

const (
	maxNegMessageSize = 64 * 1024 // decoded binary bytes
	maxNegTimestamp   = 1<<56 - 1
)

// validateNegMessage bounds wire-controlled allocations and arithmetic before
// calling the dependency's decoder. It also rejects backwards range bounds.
func validateNegMessage(message string) error {
	invalid := errors.New("invalid reconciliation payload")
	if len(message) == 0 || len(message) > 2*maxNegMessageSize {
		return invalid
	}
	b, err := hex.DecodeString(message)
	if err != nil {
		return invalid
	}
	if b[0] != 0x61 {
		// The engine replies with its supported version, as required by NIP-77.
		return nil
	}
	b = b[1:]
	read := func() (uint64, bool) {
		var value uint64
		for i := 0; i < 8 && len(b) > 0; i++ {
			next := b[0]
			b = b[1:]
			value = value<<7 | uint64(next&127)
			if next&128 == 0 {
				return value, true
			}
		}
		return 0, false
	}
	var timestamp uint64
	var previousID [32]byte
	infinite := false
	for len(b) > 0 {
		delta, ok := read()
		if !ok || infinite {
			return invalid
		}
		nextTimestamp := uint64(maxNegTimestamp)
		if delta != 0 {
			if delta-1 >= maxNegTimestamp-timestamp {
				return invalid
			}
			nextTimestamp = timestamp + delta - 1
		}
		length, ok := read()
		if !ok || length > 32 || length > uint64(len(b)) {
			return invalid
		}
		var id [32]byte
		copy(id[:], b[:int(length)])
		b = b[int(length):]
		if nextTimestamp == timestamp && slices.Compare(id[:], previousID[:]) < 0 {
			return invalid
		}
		timestamp, previousID, infinite = nextTimestamp, id, delta == 0
		mode, ok := read()
		if !ok {
			return invalid
		}
		switch mode {
		case 0:
		case 1:
			if len(b) < 16 {
				return invalid
			}
			b = b[16:]
		case 2:
			count, ok := read()
			if !ok || count > uint64(len(b)/32) {
				return invalid
			}
			b = b[int(count)*32:]
		default:
			return fmt.Errorf("unknown reconciliation mode %d", mode)
		}
	}
	return nil
}

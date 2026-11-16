package rely

import (
	"context"
	"errors"
	"fmt"

	"github.com/goccy/go-json"

	"github.com/nbd-wtf/go-nostr"
)

var (
	ErrGeneric         = errors.New(`the message must be a JSON array`)
	ErrUnsupportedType = errors.New(`the message type must be one between 'EVENT', 'REQ', 'CLOSE', 'COUNT', 'AUTH', 'NEG-OPEN', 'NEG-MSG' and 'NEG-CLOSE'`)

	ErrInvalidEvent    = errors.New(`an EVENT request must follow this format: ['EVENT', {event_JSON}]`)
	ErrInvalidEventID  = errors.New(`invalid event ID`)
	ErrInvalidEventSig = errors.New(`invalid event signature`)

	ErrInvalidReq       = errors.New(`a REQ request must follow this format: ['REQ', {id}, {filter1}, {filter2}, ...]`)
	ErrInvalidCount     = errors.New(`a COUNT request must follow this format: ['COUNT', {id}, {filter1}, {filter2}, ...]`)
	ErrInvalidRequestID = errors.New(`invalid request ID`)
)

// Request is the abstraction that represents a client request that must be sent to the relay for processing.
type request interface {
	// UID is the unique subscription identifier that combines the [Client.UID]
	// with the user-provided request ID <Client.UID>:<request.ID>
	UID() string

	// ID is a unique identifier within the scope of its client.
	ID() string

	// IsExpired reports whether the request should be skipped,
	// due to client unregistration or context cancellation.
	IsExpired() bool
}

type eventRequest struct {
	client *client
	Event  *nostr.Event
}

func (e eventRequest) UID() string     { return join(e.client.uid, e.Event.ID) }
func (e eventRequest) ID() string      { return e.Event.ID }
func (e eventRequest) IsExpired() bool { return e.client.isUnregistering.Load() }

type reqRequest struct {
	id  string
	ctx context.Context // will be cancelled when the subscription is closed

	client  *client
	Filters nostr.Filters
}

func (r reqRequest) UID() string     { return join(r.client.uid, r.id) }
func (r reqRequest) ID() string      { return r.id }
func (r reqRequest) IsExpired() bool { return r.ctx.Err() != nil || r.client.isUnregistering.Load() }

type countRequest struct {
	id      string
	client  *client
	Filters nostr.Filters
}

func (c countRequest) UID() string     { return join(c.client.uid, c.id) }
func (c countRequest) ID() string      { return c.id }
func (c countRequest) IsExpired() bool { return c.client.isUnregistering.Load() }

type closeRequest struct {
	ID string
}

type requestError struct {
	ID  string
	Err error
}

func (e *requestError) Error() string { return e.Err.Error() }

func (e *requestError) Is(target error) bool {
	if e == nil {
		return target == nil
	}

	t, ok := target.(*requestError)
	if !ok || t == nil {
		return false
	}

	return t.ID == e.ID && errors.Is(e.Err, t.Err)
}

func parseLabel(d *json.Decoder) (string, error) {
	token, err := d.Token()
	if err != nil {
		return "", fmt.Errorf("failed to read next JSON token: %w", err)
	}

	if token != json.Delim('[') {
		return "", fmt.Errorf("expected JSON array start '[' but got %v", token)
	}

	var label string
	if err := d.Decode(&label); err != nil {
		return "", fmt.Errorf("failed to read label: %w", err)
	}

	return label, nil
}

// parseEvent parses the
func parseEvent(d *json.Decoder) (eventRequest, *requestError) {
	event := eventRequest{Event: new(nostr.Event)}
	if err := d.Decode(event.Event); err != nil {
		return eventRequest{}, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidEvent, err)}
	}
	return event, nil
}

func parseReq(d *json.Decoder) (reqRequest, *requestError) {
	req := reqRequest{}
	err := d.Decode(&req.id)
	if err != nil {
		return reqRequest{}, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidRequestID, err)}
	}

	if len(req.id) < 1 || len(req.id) > 64 {
		return reqRequest{}, &requestError{ID: req.id, Err: ErrInvalidRequestID}
	}

	req.Filters, err = parseFilters(d)
	if err != nil {
		return reqRequest{}, &requestError{ID: req.id, Err: err}
	}

	if len(req.Filters) == 0 {
		return reqRequest{}, &requestError{ID: req.id, Err: ErrInvalidReq}
	}
	return req, nil
}

func parseCount(d *json.Decoder) (countRequest, *requestError) {
	count := countRequest{}
	err := d.Decode(&count.id)
	if err != nil {
		return countRequest{}, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidRequestID, err)}
	}

	if len(count.id) < 1 || len(count.id) > 64 {
		return countRequest{}, &requestError{ID: count.id, Err: ErrInvalidRequestID}
	}

	count.Filters, err = parseFilters(d)
	if err != nil {
		return countRequest{}, &requestError{ID: count.id, Err: err}
	}

	if len(count.Filters) == 0 {
		return countRequest{}, &requestError{ID: count.id, Err: ErrInvalidCount}
	}
	return count, nil
}

func parseClose(d *json.Decoder) (closeRequest, *requestError) {
	close := closeRequest{}
	if err := d.Decode(&close.ID); err != nil {
		return closeRequest{}, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidRequestID, err)}
	}

	if len(close.ID) < 1 || len(close.ID) > 64 {
		return closeRequest{}, &requestError{ID: close.ID, Err: ErrInvalidRequestID}
	}
	return close, nil
}

func parseFilters(d *json.Decoder) (nostr.Filters, error) {
	filters := make(nostr.Filters, 0, 3)
	filter := nostr.Filter{}

	for d.More() {
		if err := d.Decode(&filter); err != nil {
			return nil, fmt.Errorf("failed to decode filter: %w", err)
		}

		if filter.LimitZero || filter.Limit < 0 {
			filter.Limit = 0
		}

		filters = append(filters, filter)
		filter = nostr.Filter{} // reinitialize
	}

	return filters, nil
}

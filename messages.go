package rely

import (
	"context"
	"errors"
	"fmt"

	"github.com/goccy/go-json"

	"github.com/nbd-wtf/go-nostr"
)

var (
	ErrGeneric         = errors.New(`the request must be a JSON array`)
	ErrUnsupportedType = errors.New(`the request type must be one between 'EVENT', 'REQ', 'CLOSE', 'COUNT' and 'AUTH'`)

	ErrInvalidEventRequest   = errors.New(`an EVENT request must follow this format: ['EVENT', {event_JSON}]`)
	ErrInvalidEventID        = errors.New(`invalid event ID`)
	ErrInvalidEventSignature = errors.New(`invalid event signature`)

	ErrInvalidReqRequest     = errors.New(`a REQ request must follow this format: ['REQ', {subscription_id}, {filter1}, {filter2}, ...]`)
	ErrInvalidCountRequest   = errors.New(`a COUNT request must follow this format: ['COUNT', {subscription_id}, {filter1}, {filter2}, ...]`)
	ErrInvalidSubscriptionID = errors.New(`invalid subscription ID`)
)

// join multiple strings into one, separated by ":".
func join[T ~string](str T, strs ...T) T {
	for _, s := range strs {
		str += ":" + s
	}
	return str
}

// remove the first appearance of str from the list of strs, if present.
func remove[T ~string](strs []T, str T) []T {
	for i, v := range strs {
		if v == str {
			// release memory
			last := len(strs) - 1
			strs[i], strs[last] = strs[last], ""
			return strs[:last]
		}
	}
	return strs
}

type request interface {
	// UID is the unique request identifier that combines relay, client, and user-provided
	// request ID <relay.uid>:<clientNumber>:<request.ID>
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

func (e *eventRequest) UID() string     { return join(e.client.UID(), e.Event.ID) }
func (e *eventRequest) ID() string      { return e.Event.ID }
func (e *eventRequest) IsExpired() bool { return e.client.isUnregistering.Load() }

type reqRequest struct {
	id  string
	ctx context.Context // will be cancelled when the subscription is closed

	client  *client
	Filters nostr.Filters
}

func (r *reqRequest) UID() string     { return join(r.client.UID(), r.id) }
func (r *reqRequest) ID() string      { return r.id }
func (r *reqRequest) IsExpired() bool { return r.ctx.Err() != nil || r.client.isUnregistering.Load() }

// Subscription creates the subscription associated with the [reqRequest].
func (r *reqRequest) Subscription() Subscription {
	sub := Subscription{
		ID:      r.id,
		typ:     "REQ",
		Filters: r.Filters,
		client:  r.client,
	}
	r.ctx, sub.cancel = context.WithCancel(context.Background())
	return sub
}

type countRequest struct {
	id  string
	ctx context.Context // will be cancelled when the subscription is closed

	client  *client
	Filters nostr.Filters
}

func (c *countRequest) UID() string     { return join(c.client.UID(), c.id) }
func (c *countRequest) ID() string      { return c.id }
func (c *countRequest) IsExpired() bool { return c.ctx.Err() != nil || c.client.isUnregistering.Load() }

// Subscription creates the subscription associated with the [countRequest].
func (c *countRequest) Subscription() Subscription {
	sub := Subscription{
		ID:      c.id,
		typ:     "COUNT",
		Filters: c.Filters,
		client:  c.client,
	}
	c.ctx, sub.cancel = context.WithCancel(context.Background())
	return sub
}

type closeRequest struct {
	ID string
}

type authRequest struct {
	*nostr.Event
}

func (a *authRequest) Challenge() string {
	for _, tag := range a.Tags {
		if len(tag) > 1 && tag[0] == "challenge" {
			return tag[1]
		}
	}
	return ""
}

func (a *authRequest) Relay() string {
	for _, tag := range a.Tags {
		if len(tag) > 1 && tag[0] == "relay" {
			return tag[1]
		}
	}
	return ""
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
func parseEvent(d *json.Decoder) (*eventRequest, *requestError) {
	event := &eventRequest{Event: new(nostr.Event)}
	if err := d.Decode(event.Event); err != nil {
		return nil, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidEventRequest, err)}
	}
	return event, nil
}

// parseAuth parses the json array into an [authRequest].
func parseAuth(d *json.Decoder) (*authRequest, *requestError) {
	auth := &authRequest{Event: new(nostr.Event)}
	if err := d.Decode(auth.Event); err != nil {
		return nil, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidAuthRequest, err)}
	}
	return auth, nil
}

func parseReq(d *json.Decoder) (*reqRequest, *requestError) {
	req := &reqRequest{}
	err := d.Decode(&req.id)
	if err != nil {
		return nil, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidSubscriptionID, err)}
	}

	if len(req.id) < 1 || len(req.id) > 64 {
		return nil, &requestError{ID: req.id, Err: ErrInvalidSubscriptionID}
	}

	req.Filters, err = parseFilters(d)
	if err != nil {
		return nil, &requestError{ID: req.id, Err: err}
	}

	if len(req.Filters) == 0 {
		return nil, &requestError{ID: req.id, Err: ErrInvalidReqRequest}
	}
	return req, nil
}

func parseCount(d *json.Decoder) (*countRequest, *requestError) {
	count := &countRequest{}
	err := d.Decode(&count.id)
	if err != nil {
		return nil, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidSubscriptionID, err)}
	}

	if len(count.id) < 1 || len(count.id) > 64 {
		return nil, &requestError{ID: count.id, Err: ErrInvalidSubscriptionID}
	}

	count.Filters, err = parseFilters(d)
	if err != nil {
		return nil, &requestError{ID: count.id, Err: err}
	}

	if len(count.Filters) == 0 {
		return nil, &requestError{ID: count.id, Err: ErrInvalidCountRequest}
	}
	return count, nil
}

func parseClose(d *json.Decoder) (*closeRequest, *requestError) {
	close := &closeRequest{}
	if err := d.Decode(&close.ID); err != nil {
		return nil, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidSubscriptionID, err)}
	}

	if len(close.ID) < 1 || len(close.ID) > 64 {
		return nil, &requestError{ID: close.ID, Err: ErrInvalidSubscriptionID}
	}
	return close, nil
}

func parseFilters(d *json.Decoder) (nostr.Filters, error) {
	filters := make(nostr.Filters, 0, 3)
	filter := nostr.Filter{}

	for d.More() {
		if err := d.Decode(&filter); err != nil {
			return nil, fmt.Errorf("%w: failed to decode filter: %w", ErrInvalidReqRequest, err)
		}

		if filter.LimitZero || filter.Limit < 0 {
			filter.Limit = 0
		}

		filters = append(filters, filter)
		filter = nostr.Filter{} // reinitialize
	}

	return filters, nil
}

type response = json.Marshaler

type okResponse struct {
	ID     string
	Saved  bool
	Reason string
}

func (o okResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{"OK", o.ID, o.Saved, o.Reason})
}

type closedResponse struct {
	ID     string
	Reason string
}

func (c closedResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"CLOSED", c.ID, c.Reason})
}

type eventResponse struct {
	ID    string
	Event *nostr.Event
}

func (e eventResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{"EVENT", e.ID, e.Event})
}

type eoseResponse struct {
	ID string
}

func (e eoseResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"EOSE", e.ID})
}

type noticeResponse struct {
	Message string
}

func (n noticeResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"NOTICE", n.Message})
}

type authResponse struct {
	Challenge string
}

func (a authResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"AUTH", a.Challenge})
}

type countResponse struct {
	ID     string
	Count  int64
	Approx bool
}

func (c countResponse) MarshalJSON() ([]byte, error) {
	type payload struct {
		Count  int64 `json:"count"`
		Approx bool  `json:"approximate,omitempty"`
	}

	return json.Marshal([]any{"COUNT", c.ID, payload{Count: c.Count, Approx: c.Approx}})
}

package rely

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
)

var (
	ErrGeneric         = errors.New(`the request must be a JSON array with a length greater than two`)
	ErrUnsupportedType = errors.New(`the request type must be one between 'EVENT', 'REQ', 'CLOSE', 'COUNT' and 'AUTH'`)

	ErrInvalideventRequest   = errors.New(`an EVENT request must follow this format: ['EVENT', {event_JSON}]`)
	ErrInvalidEventID        = errors.New(`invalid event ID`)
	ErrInvalidEventSignature = errors.New(`invalid event signature`)

	ErrInvalidReqRequest     = errors.New(`a REQ request must follow this format: ['REQ', {subscription_id}, {filter1}, {filter2}, ...]`)
	ErrInvalidCountRequest   = errors.New(`a COUNT request must follow this format: ['COUNT', {subscription_id}, {filter1}, {filter2}, ...]`)
	ErrInvalidSubscriptionID = errors.New(`invalid subscription ID`)

	ErrInvalidAuthRequest   = errors.New(`an AUTH request must follow this format: ['AUTH', {event_JSON}]`)
	ErrInvalidTimestamp     = errors.New(`created_at must be within one minute from the current time`)
	ErrInvalidAuthKind      = errors.New(`invalid AUTH kind`)
	ErrInvalidAuthChallenge = errors.New(`invalid AUTH challenge`)
	ErrInvalidAuthRelay     = errors.New(`invalid AUTH relay`)
)

type request interface {
	// ID returns a unique identifier for the request within the scope of its client.
	// IDs are not globally unique across different clients.
	ID() string

	// IsExpired reports whether the request should be skipped,
	// due to client unregistration or context cancellation.
	IsExpired() bool
}

type eventRequest struct {
	client *client
	Event  *nostr.Event
}

func (e *eventRequest) ID() string      { return e.Event.ID }
func (e *eventRequest) IsExpired() bool { return e.client.isUnregistering.Load() }

type reqRequest struct {
	id  string
	ctx context.Context // will be cancelled when the subscription is closed

	client  *client
	Filters nostr.Filters
}

func (r *reqRequest) ID() string      { return r.id }
func (r *reqRequest) IsExpired() bool { return r.ctx.Err() != nil || r.client.isUnregistering.Load() }

// Subscription creates the subscription associated with the [reqRequest].
func (r *reqRequest) Subscription() Subscription {
	sub := Subscription{typ: "REQ", ID: r.id, Filters: r.Filters}
	r.ctx, sub.cancel = context.WithCancel(context.Background())
	return sub
}

type countRequest struct {
	id  string
	ctx context.Context // will be cancelled when the subscription is closed

	client  *client
	Filters nostr.Filters
}

func (c *countRequest) ID() string      { return c.id }
func (c *countRequest) IsExpired() bool { return c.ctx.Err() != nil || c.client.isUnregistering.Load() }

// Subscription creates the subscription associated with the [countRequest].
func (c *countRequest) Subscription() Subscription {
	sub := Subscription{typ: "COUNT", ID: c.id, Filters: c.Filters}
	c.ctx, sub.cancel = context.WithCancel(context.Background())
	return sub
}

type closeRequest struct {
	subID string
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

// parseJSON decodes the message received from the websocket into a label and json array.
// Based on this label (e.g. "EVENT"), the caller can parse the json into its own structure (e.g. via [parseEvent])
func parseJSON(data []byte) (label string, array []json.RawMessage, err error) {
	if err := json.Unmarshal(data, &array); err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrGeneric, err)
	}

	if len(array) < 2 {
		return "", nil, ErrGeneric
	}

	if err := json.Unmarshal(array[0], &label); err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrGeneric, err)
	}

	return label, array[1:], nil
}

// parseEvent parses the json array into an [eventRequest].
func parseEvent(array []json.RawMessage) (*eventRequest, *requestError) {
	var event nostr.Event
	if err := json.Unmarshal(array[0], &event); err != nil {
		return nil, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalideventRequest, err)}
	}

	return &eventRequest{Event: &event}, nil
}

// parseAuth parses the json array into an [authRequest].
func parseAuth(array []json.RawMessage) (*authRequest, *requestError) {
	var auth nostr.Event
	if err := json.Unmarshal(array[0], &auth); err != nil {
		return nil, &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidAuthRequest, err)}
	}

	return &authRequest{Event: &auth}, nil
}

// parseReq parses the json array into an [reqRequest], validating the subscription ID.
func parseReq(array []json.RawMessage) (*reqRequest, *requestError) {
	ID, err1 := parseID(array[0])
	if err1 != nil {
		return nil, err1
	}

	if len(array) < 2 {
		return nil, &requestError{ID: ID, Err: ErrInvalidReqRequest}
	}

	filters, err2 := parseFilters(array[1:])
	if err2 != nil {
		return nil, &requestError{ID: ID, Err: err2}
	}

	return &reqRequest{id: ID, Filters: filters}, nil
}

// parseCount parses the json array into an [countRequest], validating the subscription ID.
func parseCount(array []json.RawMessage) (*countRequest, *requestError) {
	ID, err1 := parseID(array[0])
	if err1 != nil {
		return nil, err1
	}

	if len(array) < 2 {
		return nil, &requestError{ID: ID, Err: ErrInvalidCountRequest}
	}

	filters, err2 := parseFilters(array[1:])
	if err2 != nil {
		return nil, &requestError{ID: ID, Err: err2}
	}

	return &countRequest{id: ID, Filters: filters}, nil
}

// parseClose parses the json array into an [closeRequest], validating the subscription ID.
func parseClose(array []json.RawMessage) (*closeRequest, *requestError) {
	ID, err := parseID(array[0])
	if err != nil {
		return nil, err
	}
	return &closeRequest{subID: ID}, nil
}

func parseID(data json.RawMessage) (string, *requestError) {
	var ID string
	if err := json.Unmarshal(data, &ID); err != nil {
		return "", &requestError{Err: fmt.Errorf("%w: %w", ErrInvalidSubscriptionID, err)}
	}

	if len(ID) < 1 || len(ID) > 64 {
		return "", &requestError{ID: ID, Err: ErrInvalidSubscriptionID}
	}

	return ID, nil
}

func parseFilters(array []json.RawMessage) (filters nostr.Filters, err error) {
	filters = make(nostr.Filters, len(array))
	for i, data := range array {
		var filter nostr.Filter
		if err = json.Unmarshal(data, &filter); err != nil {
			return nil, fmt.Errorf("%w: failed to decode filter at index %d: %s", ErrInvalidReqRequest, i, err)
		}

		if filter.LimitZero || filter.Limit < 0 {
			filter.Limit = 0
		}

		filters[i] = filter
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

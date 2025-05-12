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

	ErrInvalidEventRequest   = errors.New(`an EVENT request must follow this format: ['EVENT', {event_JSON}]`)
	ErrInvalidEventID        = errors.New(`invalid event ID`)
	ErrInvalidEventSignature = errors.New(`invalid event signature`)

	ErrInvalidReqRequest     = errors.New(`a REQ request must follow this format: ['REQ', {subscription_id}, {filter1}, {filter2}, ...]`)
	ErrInvalidCountRequest   = errors.New(`a COUNT request must follow this format: ['COUNT', {subscription_id}, {filter1}, {filter2}, ...]`)
	ErrInvalidSubscriptionID = errors.New(`invalid subscription ID`)

	ErrInvalidAuthRequest   = errors.New(`an AUTH request must follow this format: ['AUTH', {event_JSON}]`)
	ErrInvalidTimestamp     = errors.New(`createdAt must be within one minute from the current time`)
	ErrInvalidAuthKind      = errors.New(`invalid AUTH kind`)
	ErrInvalidAuthChallenge = errors.New(`invalid AUTH challenge`)
	ErrInvalidAuthRelay     = errors.New(`invalid AUTH relay`)
)

// Request is a minimal interface that must be fullfilled by all requests that
// should be processed in the [Relay.start].
type Request interface {
	ID() string
	From() *Client
}

type EventRequest struct {
	client *Client // the client where the request come from
	Event  *nostr.Event
}

func (e *EventRequest) ID() string    { return e.Event.ID }
func (e *EventRequest) From() *Client { return e.client }

type ReqRequest struct {
	subID string          // the subscription ID
	ctx   context.Context // will be cancelled when the subscription is closed

	client  *Client // the client where the request come from
	Filters nostr.Filters
}

func (r *ReqRequest) ID() string    { return r.subID }
func (r *ReqRequest) From() *Client { return r.client }

// Subscription creates the subscription associated with the [ReqRequest].
func (r *ReqRequest) Subscription() Subscription {
	sub := Subscription{Type: "REQ", ID: r.subID, Filters: r.Filters}
	r.ctx, sub.cancel = context.WithCancel(context.Background())
	return sub
}

type CountRequest struct {
	subID string          // the subscription ID
	ctx   context.Context // will be cancelled when the subscription is closed

	client  *Client // the client where the request come from
	Filters nostr.Filters
}

func (c *CountRequest) ID() string    { return c.subID }
func (c *CountRequest) From() *Client { return c.client }

// Subscription creates the subscription associated with the [CountRequest].
func (c *CountRequest) Subscription() Subscription {
	sub := Subscription{Type: "COUNT", ID: c.subID, Filters: c.Filters}
	c.ctx, sub.cancel = context.WithCancel(context.Background())
	return sub
}

type CloseRequest struct {
	subID string // the subscription ID
}

type AuthRequest struct {
	*nostr.Event
}

func (a *AuthRequest) Challenge() string {
	for _, tag := range a.Tags {
		if len(tag) > 1 && tag[0] == "challenge" {
			return tag[1]
		}
	}
	return ""
}

func (a *AuthRequest) Relay() string {
	for _, tag := range a.Tags {
		if len(tag) > 1 && tag[0] == "relay" {
			return tag[1]
		}
	}
	return ""
}

type RequestError struct {
	ID  string
	Err error
}

func (e *RequestError) Error() string { return e.Err.Error() }

func (e *RequestError) Is(target error) bool {
	if e == nil {
		return target == nil
	}

	t, ok := target.(*RequestError)
	if !ok {
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

// parseEvent parses the json array into an [EventRequest].
func parseEvent(array []json.RawMessage) (*EventRequest, *RequestError) {
	var event nostr.Event
	if err := json.Unmarshal(array[0], &event); err != nil {
		return nil, &RequestError{Err: fmt.Errorf("%w: %w", ErrInvalidEventRequest, err)}
	}

	return &EventRequest{Event: &event}, nil
}

// parseAuth parses the json array into an [AuthRequest].
func parseAuth(array []json.RawMessage) (*AuthRequest, *RequestError) {
	var auth nostr.Event
	if err := json.Unmarshal(array[0], &auth); err != nil {
		return nil, &RequestError{Err: fmt.Errorf("%w: %w", ErrInvalidAuthRequest, err)}
	}

	return &AuthRequest{Event: &auth}, nil
}

// parseReq parses the json array into an [ReqRequest], validating the subscription ID.
func parseReq(array []json.RawMessage) (*ReqRequest, *RequestError) {
	ID, err1 := parseID(array[0])
	if err1 != nil {
		return nil, err1
	}

	if len(array) < 2 {
		return nil, &RequestError{ID: ID, Err: ErrInvalidReqRequest}
	}

	filters, err2 := parseFilters(array[1:])
	if err2 != nil {
		return nil, &RequestError{ID: ID, Err: err2}
	}

	return &ReqRequest{subID: ID, Filters: filters}, nil
}

// parseCount parses the json array into an [CountRequest], validating the subscription ID.
func parseCount(array []json.RawMessage) (*CountRequest, *RequestError) {
	ID, err1 := parseID(array[0])
	if err1 != nil {
		return nil, err1
	}

	if len(array) < 2 {
		return nil, &RequestError{ID: ID, Err: ErrInvalidCountRequest}
	}

	filters, err2 := parseFilters(array[1:])
	if err2 != nil {
		return nil, &RequestError{ID: ID, Err: err2}
	}

	return &CountRequest{subID: ID, Filters: filters}, nil
}

// parseClose parses the json array into an [CloseRequest], validating the subscription ID.
func parseClose(array []json.RawMessage) (*CloseRequest, *RequestError) {
	ID, err := parseID(array[0])
	if err != nil {
		return nil, err
	}
	return &CloseRequest{subID: ID}, nil
}

func parseID(data json.RawMessage) (string, *RequestError) {
	var ID string
	if err := json.Unmarshal(data, &ID); err != nil {
		return "", &RequestError{Err: fmt.Errorf("%w: %w", ErrInvalidSubscriptionID, err)}
	}

	if len(ID) < 1 || len(ID) > 64 {
		return "", &RequestError{ID: ID, Err: ErrInvalidSubscriptionID}
	}

	return ID, nil
}

func parseFilters(array []json.RawMessage) (filters nostr.Filters, err error) {
	filters = make(nostr.Filters, len(array))
	for i, filter := range array {
		if err := json.Unmarshal(filter, &filters[i]); err != nil {
			return nil, fmt.Errorf("%w: failed to decode filter at index %d: %s", ErrInvalidReqRequest, i, err)
		}
	}

	return filters, nil
}

type Response = json.Marshaler

type OkResponse struct {
	ID     string
	Saved  bool
	Reason string
}

func (o OkResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{"OK", o.ID, o.Saved, o.Reason})
}

type ClosedResponse struct {
	ID     string
	Reason string
}

func (c ClosedResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"CLOSED", c.ID, c.Reason})
}

type EventResponse struct {
	ID    string
	Event *nostr.Event
}

func (e EventResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{"EVENT", e.ID, e.Event})
}

type EoseResponse struct {
	ID string
}

func (e EoseResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"EOSE", e.ID})
}

type NoticeResponse struct {
	Message string
}

func (n NoticeResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"NOTICE", n.Message})
}

type AuthResponse struct {
	Challenge string
}

func (a AuthResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{"AUTH", a.Challenge})
}

type CountResponse struct {
	ID string
	countPayload
}

type countPayload struct {
	Count  int64 `json:"count"`
	Approx bool  `json:"approximate,omitempty"`
}

func (c CountResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{"COUNT", c.ID, c.countPayload})
}

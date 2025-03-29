package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
)

var (
	ErrGeneric         = errors.New("the request must be a JSON array with a length greater than two")
	ErrUnsupportedType = errors.New("the request type must be one between 'EVENT', 'REQ' and 'CLOSE'")

	ErrInvalidEventRequest   = errors.New("an EVENT request must follow this format: ['EVENT', <event JSON>]")
	ErrInvalidEventID        = errors.New("invalid event ID")
	ErrInvalidEventSignature = errors.New("invalid event signature")

	ErrInvalidReqRequest     = errors.New("a REQ request must follow this format: ['REQ', <subscription_id>, <filter1>, <filter2>, ...]")
	ErrInvalidSubscriptionID = errors.New("invalid subscription ID")

	ErrTooManyOpenFilters = errors.New("too many open filters, please close some subscriptions")
)

type request struct {
	Label string
	array []json.RawMessage
}

type EventRequest struct {
	client *Client // the client the request come from
	Event  nostr.Event
}

type ReqRequest struct {
	ID  string          // the subscription ID
	ctx context.Context // will be cancelled when the subscription is closed

	client *Client // the client the request come from
	nostr.Filters
}

type CloseRequest struct {
	ID string // the subscription ID
}

type RequestError struct {
	ID  string
	Err error
}

func (e *RequestError) Error() string { return e.Err.Error() }

func (e *RequestError) Is(target error) bool {
	t, ok := target.(*RequestError)
	if !ok {
		return false
	}

	return t.ID == e.ID && errors.Is(e.Err, t.Err)
}

// Parse decodes the JSON array message from the websocket connection into a [request].
// The request must then be converted using the appropriate To<type's name> method e.g. [request.ToEventRequest].
func Parse(data []byte) (request, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err != nil {
		return request{}, fmt.Errorf("%w: %w", ErrGeneric, err)
	}

	if len(arr) < 2 {
		return request{}, ErrGeneric
	}

	var label string
	if err := json.Unmarshal(arr[0], &label); err != nil {
		return request{}, fmt.Errorf("%w: %w", ErrGeneric, err)
	}

	return request{Label: label, array: arr[1:]}, nil
}

// ToEventRequest converts the request into an [EventRequest], validating the ID and signature of the event.
func (r request) ToEventRequest() (*EventRequest, *RequestError) {
	var event nostr.Event
	if err := json.Unmarshal(r.array[0], &event); err != nil {
		return nil, &RequestError{Err: fmt.Errorf("%w: %w", ErrInvalidEventRequest, err)}
	}

	if !event.CheckID() {
		return nil, &RequestError{ID: event.ID, Err: ErrInvalidEventID}
	}

	match, err := event.CheckSignature()
	if !match {
		if err != nil {
			return nil, &RequestError{ID: event.ID, Err: fmt.Errorf("%w: %s", ErrInvalidEventSignature, err.Error())}
		}
		return nil, &RequestError{ID: event.ID, Err: ErrInvalidEventSignature}
	}

	return &EventRequest{Event: event}, nil
}

// ToReqRequest converts the request into an [ReqRequest], validating the subscription ID.
func (r request) ToReqRequest() (*ReqRequest, *RequestError) {
	ID, err := parseID(r.array[0])
	if err != nil {
		return nil, err
	}

	if len(r.array) < 2 {
		return nil, &RequestError{ID: ID, Err: ErrInvalidReqRequest}
	}

	filters := make(nostr.Filters, len(r.array)-1)
	for i, filter := range r.array[1:] {
		if err := json.Unmarshal(filter, &filters[i]); err != nil {
			return nil, &RequestError{ID: ID, Err: fmt.Errorf("%w: failed to decode filter at index %d: %s", ErrInvalidReqRequest, i, err)}
		}
	}

	return &ReqRequest{ID: ID, Filters: filters}, nil
}

// ToCloseRequest converts the request into an [CloseRequest], validating the subscription ID.
func (r request) ToCloseRequest() (*CloseRequest, *RequestError) {
	ID, err := parseID(r.array[0])
	if err != nil {
		return nil, err
	}
	return &CloseRequest{ID: ID}, nil
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

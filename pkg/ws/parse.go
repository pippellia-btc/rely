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

type EventRequest struct {
	client *Client // the client where the request come from
	Event  *nostr.Event
}

type ReqRequest struct {
	ID  string          // the subscription ID
	ctx context.Context // will be cancelled when the subscription is closed

	client *Client // the client where the request come from
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
	if e == nil {
		return target == nil
	}

	t, ok := target.(*RequestError)
	if !ok {
		return false
	}

	return t.ID == e.ID && errors.Is(e.Err, t.Err)
}

/*
JSONArray decodes the message received from the websocket into a label and json array.
Based on this label (e.g. "EVENT"), the caller can parse the json into its own structure (e.g. via [ParseEventRequest])
*/
func JSONArray(data []byte) (label string, array []json.RawMessage, err error) {
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

// ParseEventRequest parses the json array into an [EventRequest].
func ParseEventRequest(array []json.RawMessage) (*EventRequest, *RequestError) {
	var event nostr.Event
	if err := json.Unmarshal(array[0], &event); err != nil {
		return nil, &RequestError{Err: fmt.Errorf("%w: %w", ErrInvalidEventRequest, err)}
	}

	return &EventRequest{Event: &event}, nil
}

// ParseReqRequest parses the json array into an [ReqRequest], validating the subscription ID.
func ParseReqRequest(array []json.RawMessage) (*ReqRequest, *RequestError) {
	ID, err := parseID(array[0])
	if err != nil {
		return nil, err
	}

	if len(array) < 2 {
		return nil, &RequestError{ID: ID, Err: ErrInvalidReqRequest}
	}

	filters := make(nostr.Filters, len(array)-1)
	for i, filter := range array[1:] {
		if err := json.Unmarshal(filter, &filters[i]); err != nil {
			return nil, &RequestError{ID: ID, Err: fmt.Errorf("%w: failed to decode filter at index %d: %s", ErrInvalidReqRequest, i, err)}
		}
	}

	return &ReqRequest{ID: ID, Filters: filters}, nil
}

// ParseCloseRequest parses the json array into an [CloseRequest], validating the subscription ID.
func ParseCloseRequest(array []json.RawMessage) (*CloseRequest, *RequestError) {
	ID, err := parseID(array[0])
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

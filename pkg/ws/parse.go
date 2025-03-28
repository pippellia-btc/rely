package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
)

var (
	ErrGeneric               = errors.New("the request must be a JSON array with a length greater than two")
	ErrInvalidEventRequest   = errors.New(`an EVENT request must follow this format: ["EVENT", <event JSON>]`)
	ErrInvalidReqRequest     = errors.New(`a REQ request must follow this format: ["REQ", <subscription_id>, <filter1>, <filter2>, ...]`)
	ErrInvalidEventID        = errors.New("invalid event ID")
	ErrInvalidSubscriptionID = errors.New("invalid subscription ID")
	ErrInvalidEventSignature = errors.New("invalid event signature")

	ErrUnsupportedType = errors.New("the request type must be one between 'EVENT', 'REQ' and 'CLOSE'")
)

type request struct {
	Label string
	array []json.RawMessage
}

type EventRequest struct {
	ctx   context.Context
	Event nostr.Event
}

type ReqRequest struct {
	ID  string // the subscription ID
	ctx context.Context
	nostr.Filters
}

type CloseRequest struct {
	ID string // the subscription ID
}

// Parse decodes the JSON array message from the websocket connection into a [request],
// performing minimal checks and extracting the label.
// The request must then be converted using the appropriate To<type's name> method e.g. [ToEventRequest].
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

func (r request) ToEventRequest() (*EventRequest, error) {
	var event nostr.Event
	if err := json.Unmarshal(r.array[0], &event); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEventRequest, err)
	}

	if !event.CheckID() {
		return nil, ErrInvalidEventID
	}

	match, err := event.CheckSignature()
	if !match {
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidEventSignature, err.Error())
		}
		return nil, ErrInvalidEventSignature
	}

	return &EventRequest{Event: event}, nil
}

func (r request) ToReqRequest() (*ReqRequest, error) {
	if len(r.array) < 2 {
		return nil, ErrInvalidReqRequest
	}

	ID, err := parseID(r.array[0])
	if err != nil {
		return nil, err
	}

	filters := make(nostr.Filters, len(r.array)-1)
	for i, filter := range r.array[1:] {
		if err := json.Unmarshal(filter, &filters[i]); err != nil {
			return nil, fmt.Errorf("%w: failed to decode filter at index %d: %s", ErrInvalidReqRequest, i, err)
		}
	}

	return &ReqRequest{ID: ID, Filters: filters}, nil
}

func (r request) ToCloseRequest() (*CloseRequest, error) {
	ID, err := parseID(r.array[0])
	if err != nil {
		return nil, err
	}
	return &CloseRequest{ID: ID}, nil
}

func parseID(data json.RawMessage) (string, error) {
	var ID string
	if err := json.Unmarshal(data, &ID); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidSubscriptionID, err)
	}

	if len(ID) < 1 || len(ID) > 64 {
		return "", ErrInvalidSubscriptionID
	}

	return ID, nil
}

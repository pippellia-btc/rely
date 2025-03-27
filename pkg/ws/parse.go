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
	ErrUnsupportedType       = errors.New("the request type must be one between 'EVENT', 'REQ' and 'CLOSE'")
	ErrInvalidSubscriptionID = errors.New("invalid subscription ID")
	ErrInvalidEvent          = errors.New(`an EVENT request must follow this format: ["EVENT", <event JSON>]`)
	ErrInvalidReq            = errors.New(`a REQ request must follow this format: ["REQ", <subscription_id>, <filter1>, <filter2>, ...]`)
)

type Request interface {
	Label() string
}

type EventRequest struct {
	ctx context.Context
	nostr.Event
}

func (e EventRequest) Label() string { return "EVENT" }

type ReqRequest struct {
	ctx context.Context
	ID  string // the subscription ID
	nostr.Filters
}

func (r ReqRequest) Label() string { return "REQ" }

type CloseRequest struct {
	ID string // the subscription ID
}

func (c CloseRequest) Label() string { return "CLOSE" }

// Parse decodes the JSON array message from the websocket connection into the appropriate [Request] type.
func Parse(data []byte) (Request, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGeneric, err)
	}

	if len(arr) < 2 {
		return nil, ErrGeneric
	}

	var label string
	if err := json.Unmarshal(arr[0], &label); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGeneric, err)
	}

	switch label {
	case "EVENT":
		var event nostr.Event
		if err := json.Unmarshal([]byte(arr[1]), &event); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
		}
		return EventRequest{Event: event}, nil

	case "CLOSE":
		ID, err := parseID(arr[1])
		if err != nil {
			return nil, err
		}
		return CloseRequest{ID: ID}, nil

	case "REQ":
		if len(arr) < 3 {
			return nil, ErrInvalidReq
		}

		ID, err := parseID(arr[1])
		if err != nil {
			return nil, err
		}

		filters := make(nostr.Filters, len(arr)-2)
		for i, filter := range arr[2:] {
			if err := json.Unmarshal(filter, &filters[i]); err != nil {
				return nil, fmt.Errorf("%w: failed to decode filter at index %d: %s", ErrInvalidReq, i, err)
			}
		}

		return ReqRequest{ID: ID, Filters: filters}, nil

	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, label)
	}
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

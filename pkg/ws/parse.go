package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
)

type Request interface {
	Label() string
	//UnmarshalJSON(data []byte) error
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
	ID string
}

func (c CloseRequest) Label() string { return "CLOSE" }

func Parse(data []byte) (Request, error) {
	comma := bytes.Index(data, []byte{','})
	if comma == -1 {
		return nil, ErrFailedToDecode
	}

	label := string(data[0:comma])
	var request Request
	switch label {
	case "EVENT":
		request = &EventRequest{}

	case "REQ":
		request = &ReqRequest{}

	case "CLOSE":
		request = &CloseRequest{}

	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, label)
	}

	if err := json.Unmarshal(data[comma:], request); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFailedToDecode, err)
	}

	return request, nil
}

var ErrFailedToDecode = errors.New("failed to decode the request")
var ErrUnsupportedType = errors.New("message type must be one between 'EVENT', 'REQ' and 'CLOSE'")

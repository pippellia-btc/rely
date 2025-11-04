package rely

import (
	"bytes"
	"fmt"

	"github.com/goccy/go-json"

	"github.com/nbd-wtf/go-nostr"
)

var (
	openArray  byte = '['
	closeArray byte = ']'
	comma      byte = ','

	eventLabel = []byte(`"EVENT"`)
)

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
	id, err := json.Marshal(e.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ID: %w", err)
	}

	event, err := e.Event.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal event: %w", err)
	}

	capacity := len(eventLabel) + len(id) + len(event) + 4
	buf := bytes.Buffer{}
	buf.Grow(capacity)

	buf.WriteByte(openArray)
	buf.Write(eventLabel)
	buf.WriteByte(comma)
	buf.Write(id)
	buf.WriteByte(comma)
	buf.Write(event)
	buf.WriteByte(closeArray)
	return buf.Bytes(), nil
}

// rawEventResponse represent the same message [eventResponse], but holds the Event
// as [json.RawMessage]. This is especially useful when broadcasting, as we can marshals the
// event only once instead of once per matching subscription.
type rawEventResponse struct {
	ID    string
	Event json.RawMessage
}

func (e rawEventResponse) MarshalJSON() ([]byte, error) {
	id, err := json.Marshal(e.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ID: %w", err)
	}

	capacity := len(eventLabel) + len(id) + len(e.Event) + 4
	buf := bytes.Buffer{}
	buf.Grow(capacity)

	buf.WriteByte(openArray)
	buf.Write(eventLabel)
	buf.WriteByte(comma)
	buf.Write(id)
	buf.WriteByte(comma)
	buf.Write(e.Event)
	buf.WriteByte(closeArray)
	return buf.Bytes(), nil
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

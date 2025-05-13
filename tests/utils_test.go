package tests

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

func randomeventRequest() ([]byte, error) {
	request := []any{"EVENT", randomEvent()}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to generate event request: %w", err)
	}
	return data, nil
}

func randomReqRequest() ([]byte, error) {
	ID := randomString(rg.IntN(70))
	request := []any{"REQ", ID}

	filters := rg.IntN(5)
	for i := 0; i < filters; i++ {
		request = append(request, randomFilter())
	}

	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to generate event request: %w", err)
	}
	return data, nil
}

func randomCountRequest() ([]byte, error) {
	ID := randomString(rg.IntN(70))
	request := []any{"COUNT", ID}

	filters := rg.IntN(5)
	for i := 0; i < filters; i++ {
		request = append(request, randomFilter())
	}

	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to generate event request: %w", err)
	}
	return data, nil
}

func randomEvent() nostr.Event {
	event := nostr.Event{
		CreatedAt: nostr.Timestamp(rg.Int64()),
		Kind:      rg.Int(),
		Tags:      randomSlice(100, randomTag),
	}

	if rg.Float32() < clientFailProbability {
		// return unsigned event
		return event
	}

	sk := nostr.GeneratePrivateKey()
	if err := event.Sign(sk); err != nil {
		return nostr.Event{}
	}
	return event
}

func randomFilter() nostr.Filter {
	since := nostr.Timestamp(rg.Int64())
	until := nostr.Timestamp(rg.Int64())

	return nostr.Filter{
		IDs:     randomSlice(100, randomString64),
		Authors: randomSlice(100, randomString64),
		Kinds:   randomSlice(100, rg.Int),
		Tags:    randomTagMap(100),
		Since:   &since,
		Until:   &until,
		Limit:   rg.IntN(1000),
		Search:  randomString(45),
	}
}

func randomTagMap(max int) nostr.TagMap {
	size := rg.IntN(max)
	m := make(nostr.TagMap, size)
	for i := 0; i < size; i++ {
		m[randomString(3)] = randomTag()
	}
	return m
}

func randomSlice[T any](max int, genFunc func() T) []T {
	n := rg.IntN(max)
	slice := make([]T, n)
	for i := range slice {
		slice[i] = genFunc()
	}
	return slice
}

func randomTag() nostr.Tag {
	l := rg.IntN(8)
	tag := make(nostr.Tag, l)
	for i := 0; i < l; i++ {
		length := rg.IntN(10)
		tag[i] = randomString(length)
	}
	return tag
}

const symbols = `abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ123456789`

func randomString(l int) string {
	b := make([]byte, l)
	for i := range b {
		b[i] = symbols[rg.IntN(len(symbols))]
	}
	return string(b)
}

func randomString64() string { return randomString(64) }

// fibonacci returns the n-th fibonacci number. It's used to simulate some meaningful work.
func fibonacci(n int) int {
	switch {
	case n < 1:
		return 0

	case n == 1:
		return 1

	default:
		return fibonacci(n-1) + fibonacci(n-2)
	}
}

func BenchmarkFibonacci(b *testing.B) {
	for _, n := range []int{5, 10, 15, 20, 25, 30} {
		b.Run(fmt.Sprintf("n = %d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				fibonacci(n)
			}
		})
	}
}

func validateLabel(labels []string) func([]byte) error {
	return func(data []byte) error {
		label, err := parseLabel(data)
		if err != nil {
			return fmt.Errorf("%w: data '%v'", err, string(data))
		}

		if !slices.Contains(labels, label) {
			return fmt.Errorf("label is not among the expected labels %v: data %v", labels, string(data))
		}

		return nil
	}
}

func parseLabel(data []byte) (string, error) {
	var array []json.RawMessage
	if err := json.Unmarshal(data, &array); err != nil {
		return "", fmt.Errorf("%w: %w", rely.ErrGeneric, err)
	}

	var label string
	if err := json.Unmarshal(array[0], &label); err != nil {
		return "", fmt.Errorf("%w: %w", rely.ErrGeneric, err)
	}

	return label, nil
}

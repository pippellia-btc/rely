package tests

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/goccy/go-json"

	"github.com/nbd-wtf/go-nostr"
)

const (
	randomSamples            int     = 1000
	unsignedEventProbability float32 = 0.05
)

var (
	// Manually change the seed to reproduce a specific situation / error
	seed = uint64(time.Now().Unix())
	rg   = rand.New(rand.NewPCG(0, seed))

	eventTemplates = make([][]byte, randomSamples)
	reqTemplates   = make([][]byte, randomSamples)
	countTemplates = make([][]byte, randomSamples)
)

func init() {
	for i := range randomSamples {
		reqTemplates[i] = randomReqRequest()
		eventTemplates[i] = randomEventRequest()
		countTemplates[i] = randomCountRequest()
	}
}

func quickEventRequest() []byte {
	i := rg.IntN(randomSamples)
	template := eventTemplates[i]
	event := make([]byte, len(template))
	copy(event, template)

	if rg.Float32() < unsignedEventProbability {
		// this will inevitably invalidate the signature
		modifyBytes(event, 5)
	}
	return event
}

func quickReqRequest() []byte {
	i := rg.IntN(randomSamples)
	template := reqTemplates[i]
	req := make([]byte, len(template))
	copy(req, template)
	modifyBytes(req, 5)
	return req
}

func quickCountRequest() []byte {
	i := rg.IntN(randomSamples)
	template := countTemplates[i]
	count := make([]byte, len(template))
	copy(count, template)
	modifyBytes(count, 5)
	return count
}

func modifyBytes(buf []byte, locations int) {
	l := len(buf)
	if l == 0 {
		return
	}

	for range locations {
		idx := rg.IntN(l)
		buf[idx] = byte(rg.IntN(256))
	}
}

func randomEventRequest() []byte {
	event := []any{"EVENT", randomEvent()}
	data, err := json.Marshal(event)
	if err != nil {
		panic(fmt.Errorf("failed to marshal event %v: %w", event, err))
	}
	return data
}

func randomReqRequest() []byte {
	ID := randomString()
	req := []any{"REQ", ID}

	filters := rg.IntN(10)
	for range filters {
		req = append(req, randomFilter())
	}

	data, err := json.Marshal(req)
	if err != nil {
		panic(fmt.Errorf("failed to marshal req %v: %w", req, err))
	}
	return data
}

func randomCountRequest() []byte {
	ID := randomString()
	count := []any{"COUNT", ID}

	filters := rg.IntN(10)
	for range filters {
		count = append(count, randomFilter())
	}

	data, err := json.Marshal(count)
	if err != nil {
		panic(fmt.Errorf("failed to marshal count %v: %w", count, err))
	}
	return data
}

func randomEvent() nostr.Event {
	event := nostr.Event{
		CreatedAt: nostr.Timestamp(rg.Int64()),
		Kind:      rg.Int(),
		Tags:      randomSlice(randomTag),
		Content:   randomString(),
	}

	sk := nostr.GeneratePrivateKey()
	if err := event.Sign(sk); err != nil {
		panic(fmt.Errorf("failed to sign event: %w", err))
	}
	return event
}

func randomFilter() nostr.Filter {
	return nostr.Filter{
		Since:   randomTimestamp(),
		Until:   randomTimestamp(),
		IDs:     randomSlice(randomString),
		Authors: randomSlice(randomString),
		Kinds:   randomSlice(rg.Int),
		Tags:    randomTagMap(),
		Limit:   rg.Int(),
		Search:  randomString(),
	}
}

func randomTagMap() nostr.TagMap {
	size := rg.IntN(100)
	m := make(nostr.TagMap, size)
	for range size {
		m[randomString()] = randomTag()
	}
	return m
}

func randomSlice[T any](genFunc func() T) []T {
	n := rg.IntN(100)
	slice := make([]T, n)
	for i := range n {
		slice[i] = genFunc()
	}
	return slice
}

func randomTag() nostr.Tag {
	l := rg.IntN(15)
	tag := make(nostr.Tag, l)
	for i := range l {
		tag[i] = randomString()
	}
	return tag
}

const symbols = `abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ123456789`

func randomString() string {
	l := rg.IntN(70)
	s := make([]byte, l)
	for i := range l {
		s[i] = symbols[rg.IntN(len(symbols))]
	}
	return string(s)
}

func randomTimestamp() *nostr.Timestamp {
	timestamp := nostr.Timestamp(rg.Int64())
	return &timestamp
}

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
			for range b.N {
				fibonacci(n)
			}
		})
	}
}

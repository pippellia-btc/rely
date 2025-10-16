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

	reqTemplates   = make([][]byte, randomSamples)
	countTemplates = make([][]byte, randomSamples)
	eventTemplates = make([][]byte, randomSamples)
	closeTemplates = make([][]byte, randomSamples)
)

func generateTemplates() {
	for i := range randomSamples {
		reqTemplates[i] = randomReqRequest()
		countTemplates[i] = randomCountRequest()
		eventTemplates[i] = randomEventRequest()
		closeTemplates[i] = randomCloseRequest()
	}
}

func quickReq() []byte {
	i := rg.IntN(randomSamples)
	template := reqTemplates[i]
	req := make([]byte, len(template))
	copy(req, template)
	modifyBytes(req, 5)
	return req
}

func quickCount() []byte {
	i := rg.IntN(randomSamples)
	template := countTemplates[i]
	count := make([]byte, len(template))
	copy(count, template)
	modifyBytes(count, 5)
	return count
}

func quickEvent() []byte {
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

func quickClose() []byte {
	i := rg.IntN(randomSamples)
	template := closeTemplates[i]
	close := make([]byte, len(template))
	copy(close, template)
	modifyBytes(close, 5)
	return close
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

func randomReqRequest() []byte {
	req := []any{"REQ", RandomString()}
	filters := rg.IntN(10)
	for range filters {
		req = append(req, RandomFilter())
	}

	data, err := json.Marshal(req)
	if err != nil {
		panic(fmt.Errorf("failed to marshal req %v: %w", req, err))
	}
	return data
}

func randomCountRequest() []byte {
	count := []any{"COUNT", RandomString()}
	filters := rg.IntN(10)
	for range filters {
		count = append(count, RandomFilter())
	}

	data, err := json.Marshal(count)
	if err != nil {
		panic(fmt.Errorf("failed to marshal count %v: %w", count, err))
	}
	return data
}

func randomEventRequest() []byte {
	event := []any{"EVENT", RandomEvent()}
	data, err := json.Marshal(event)
	if err != nil {
		panic(fmt.Errorf("failed to marshal event %v: %w", event, err))
	}
	return data
}

func randomCloseRequest() []byte {
	close := []any{"CLOSE", RandomString()}
	data, err := json.Marshal(close)
	if err != nil {
		panic(fmt.Errorf("failed to marshal close %v: %w", close, err))
	}
	return data
}

func RandomEvents() []nostr.Event {
	return RandomSlice(RandomEvent)
}

func RandomEvent() nostr.Event {
	event := nostr.Event{
		CreatedAt: nostr.Timestamp(rg.Int64()),
		Kind:      rg.Int(),
		Tags:      RandomSlice(randomTag),
		Content:   RandomString(),
	}

	sk := nostr.GeneratePrivateKey()
	if err := event.Sign(sk); err != nil {
		panic(fmt.Errorf("failed to sign event: %w", err))
	}
	return event
}

func RandomFilters() nostr.Filters {
	return RandomSlice(RandomFilter)
}

func RandomFilter() nostr.Filter {
	f := nostr.Filter{}
	if rg.Float32() < 0.1 {
		f.IDs = RandomSlice(RandomString)
	}
	if rg.Float32() < 0.1 {
		f.Kinds = RandomSlice(rg.Int)
	}
	if rg.Float32() < 0.1 {
		f.Authors = RandomSlice(RandomString)
	}
	if rg.Float32() < 0.1 {
		f.Tags = RandomTagMap()
	}
	if rg.Float32() < 0.1 {
		f.Since = RandomTimestamp()
	}
	if rg.Float32() < 0.1 {
		f.Until = RandomTimestamp()
	}
	if rg.Float32() < 0.1 {
		f.Limit = rg.Int()
	}
	if rg.Float32() < 0.1 {
		f.Search = RandomString()
	}
	return f
}

func RandomTagMap() nostr.TagMap {
	size := rg.IntN(100)
	m := make(nostr.TagMap, size)
	for range size {
		m[RandomString()] = randomTag()
	}
	return m
}

func RandomSliceN[T any](n int, genFunc func() T) []T {
	slice := make([]T, n)
	for i := range n {
		slice[i] = genFunc()
	}
	return slice
}

func RandomSlice[T any](genFunc func() T) []T {
	n := int(rg.ExpFloat64() * 30)
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
		tag[i] = RandomString()
	}
	return tag
}

const symbols = `abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ123456789`

func RandomString() string {
	l := rg.IntN(70)
	s := make([]byte, l)
	for i := range l {
		s[i] = symbols[rg.IntN(len(symbols))]
	}
	return string(s)
}

func RandomTimestamp() *nostr.Timestamp {
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

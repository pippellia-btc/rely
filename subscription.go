package rely

import (
	"context"
	"sync"

	"github.com/nbd-wtf/go-nostr"
)

type Subscription struct {
	ID      string
	typ     string // either "REQ" or "COUNT"
	Filters nostr.Filters

	cancel context.CancelFunc // calling it cancels the context of the associated REQ/COUNT
}

type Subscriptions struct {
	mu   sync.RWMutex
	list []Subscription // normally < 100 subs, so a slice is more efficient than a map
}

func (s *Subscriptions) Add(new Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.list {
		if s.list[i].ID == new.ID {
			// overwriting an existing subscription
			s.list[i].cancel()
			s.list[i] = new
			return
		}
	}

	s.list = append(s.list, new)
}

func (s *Subscriptions) Remove(ID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.list {
		if s.list[i].ID == ID {
			s.list[i].cancel()

			// release memory (TODO: probably doing it every time is too costly)
			last := len(s.list) - 1
			s.list[i], s.list[last] = s.list[last], Subscription{}
			s.list = s.list[:last]
			return
		}
	}
}

func (s *Subscriptions) Matching(event *nostr.Event) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var IDs []string
	for _, sub := range s.list {
		if sub.typ == "REQ" && sub.Filters.Match(event) {
			IDs = append(IDs, sub.ID)
		}
	}
	return IDs
}

func (s *Subscriptions) List() []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]Subscription, 0, len(s.list))
	for _, sub := range s.list {
		list = append(list, sub)
	}
	return list
}

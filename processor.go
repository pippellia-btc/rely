package rely

import "github.com/nbd-wtf/go-nostr"

type processor struct {
	maxWorkers int
	queue      chan request

	// pointer to parent relay, which must only be used for:
	//	- reading settings/hooks
	//	- sending to channels
	// 	- incrementing atomic counters
	relay *Relay
}

func newProcessor(relay *Relay) *processor {
	return &processor{
		maxWorkers: 4,
		queue:      make(chan request, 1024),
		relay:      relay,
	}
}

// Run spawn no more than [processor.maxWorkers] concurrent workers,
// each processing the requests by appliying the user defined [Hooks].
func (p *processor) Run() {
	defer p.relay.wg.Done()

	semaphore := make(chan struct{}, p.maxWorkers)

	for {
		select {
		case <-p.relay.done:
			return

		case request := <-p.queue:
			if request.IsExpired() {
				continue
			}

			semaphore <- struct{}{}
			p.relay.wg.Add(1)

			go func() {
				defer func() {
					<-semaphore
					p.relay.wg.Done()
				}()

				p.Process(request)
			}()
		}
	}
}

// Process a single request based on its type, by applying the user defined [Hooks].
func (p *processor) Process(r request) {
	switch r := r.(type) {
	case negRequest:
		p.processNegentropy(r)

	case eventRequest:
		res := p.relay.On.Event(r.client, r.Event)
		r.client.send(okResponse{ID: r.Event.ID, Accepted: !res.failed, Reason: res.reason})

		if !res.noBroadcast {
			p.relay.Broadcast(r.Event)
		}

	case reqRequest:
		budget := r.client.RemainingCapacity()
		ApplyBudget(budget, r.Filters...)

		events, err := p.relay.On.Req(r.ctx, r.client, r.id, r.Filters)
		if err != nil {
			if r.ctx.Err() == nil {
				// error not caused by the user's CLOSE, so we must close the subscription
				r.client.CloseSubWithReason(r.id, err.Error())
			}
			return
		}

		for i := range events {
			r.client.send(eventResponse{ID: r.id, Event: &events[i]})
		}
		r.client.send(eoseResponse{ID: r.id})

	case countRequest:
		count, approx, err := p.relay.On.Count(r.client, r.id, r.Filters)
		if err != nil {
			r.client.send(closedResponse{ID: r.id, Reason: err.Error()})
			return
		}

		r.client.send(countResponse{ID: r.id, Count: count, Approx: approx})
	}
}

// ApplyBudget adjusts the Limit of each filter in-place so that the total does not exceed the given budget.
// Filters with limits <= budget / len(filters) are preserved, while larger ones are scaled down proportionally.
// It panics if budget is negative.
func ApplyBudget(budget int, filters ...nostr.Filter) {
	if budget < 0 {
		panic("rely.ApplyBudget: budget should not be negative")
	}

	if len(filters) == 0 {
		return
	}

	if budget < len(filters) {
		// give 1 to as many filters as possible, set the rest to zero
		for i := range filters {
			if i < budget {
				filters[i].Limit = 1
			} else {
				filters[i].Limit = 0
				filters[i].LimitZero = true
			}
		}
		return
	}

	used := 0
	for i := range filters {
		if filters[i].LimitZero {
			// ensure consistency
			filters[i].Limit = 0
		}

		if !filters[i].LimitZero && filters[i].Limit < 1 {
			// limit is unspecified (or negative), so we set it equal to the budget
			filters[i].Limit = budget
		}

		used += filters[i].Limit
	}

	if used > budget {
		// modify filters based on whether they have a limit lower or higher than budget / len(filters).
		// 	- lowers: do nothing
		//	- highers: linearly scale their limit

		fair := budget / len(filters)
		var sumHighers int
		var highers []int

		for i := range filters {
			limit := filters[i].Limit
			if limit > fair {
				highers = append(highers, i)
				sumHighers += limit
			} else {
				budget -= limit
			}
		}

		scalingFactor := float64(budget) / float64(sumHighers)
		for _, idx := range highers {
			limit := float64(filters[idx].Limit)
			newLimit := int(scalingFactor * limit)

			if newLimit == 0 {
				filters[idx].Limit = 0
				filters[idx].LimitZero = true
			} else {
				filters[idx].Limit = newLimit
			}
		}
	}
}

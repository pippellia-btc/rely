package rely

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

// Run process the requests with [processor.maxWorkers], by appliying the user defined [Hooks].
func (p *processor) Run() {
	defer p.relay.wg.Done()

	sem := make(chan struct{}, p.maxWorkers)

	for {
		select {
		case <-p.relay.done:
			return

		case request := <-p.queue:
			if request.IsExpired() {
				continue
			}

			sem <- struct{}{}
			go func() {
				p.Process(request)
				<-sem
			}()
		}
	}
}

// Process a single request based on its type, by applying the user defined [Hooks].
func (p *processor) Process(request request) {
	ID := request.ID()
	switch request := request.(type) {
	case *eventRequest:
		err := p.relay.On.Event(request.client, request.Event)
		if err != nil {
			request.client.send(okResponse{ID: ID, Saved: false, Reason: err.Error()})
			return
		}

		request.client.send(okResponse{ID: ID, Saved: true})
		p.relay.Broadcast(request.Event)

	case *reqRequest:
		budget := request.client.RemainingCapacity()
		ApplyBudget(budget, request.Filters...)

		events, err := p.relay.On.Req(request.ctx, request.client, request.Filters)
		if err != nil {
			if request.ctx.Err() == nil {
				// error not caused by the user's CLOSE, so we must close the subscription
				request.client.send(closedResponse{ID: ID, Reason: err.Error()})
				p.relay.closeSubscription(request.UID())
			}
			return
		}

		for i := range events {
			request.client.send(eventResponse{ID: ID, Event: &events[i]})
		}
		request.client.send(eoseResponse{ID: ID})

	case *countRequest:
		count, approx, err := p.relay.On.Count(request.client, request.Filters)
		if err != nil {
			request.client.send(closedResponse{ID: ID, Reason: err.Error()})
			return
		}

		request.client.send(countResponse{ID: ID, Count: count, Approx: approx})
		p.relay.closeSubscription(request.UID())
	}
}

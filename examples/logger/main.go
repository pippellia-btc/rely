package main

import (
	"context"
	"log/slog"
	"os"

	"fiatjaf.com/nostr"
	"github.com/pippellia-btc/rely"
)

/*
An example showcasing how to pass a custom logger for the relay to use.
*/

var logger *slog.Logger

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rely.HandleSignals(cancel)

	// creating a structured JSON logger
	logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

	relay := rely.NewRelay(
		rely.WithLogger(logger),
	)

	relay.On.Event = Save
	relay.On.Req = Query

	if err := relay.StartAndServe(ctx, "localhost:3334"); err != nil {
		panic(err)
	}
}

func Save(c rely.Client, e *nostr.Event) error {
	logger.Info("received event", "event", e)
	return nil
}

func Query(ctx context.Context, c rely.Client, f []nostr.Filter) ([]nostr.Event, error) {
	logger.Info("received filters", "filters", f)
	return nil, nil
}

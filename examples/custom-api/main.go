package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely"
)

/*
Integrates custom API endpoints into a relay.

This solution uses [http.ServeMux] composition to delegate non-API traffic
to the fixed `relay.ServeHTTP` handler, bypassing the framework's
StartAndServe method to implement a custom, context-aware graceful shutdown.
*/

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// create your own custom relay, and start it without serving it
	relay := rely.NewRelay()
	relay.On.Event = Save
	relay.On.Req = Query
	relay.Start(ctx)

	// create a router with its own custom handlers
	router := http.NewServeMux()
	router.HandleFunc("GET /welcome", Welcome)
	router.HandleFunc("GET /welcome/{name}", Welcome)

	// register relay's methods with the router in the "catch-all" handler,
	// which will be called unless the request matches more specific handlers.
	router.Handle("/", relay)

	// create a http server that uses our custom router
	server := &http.Server{Addr: "localhost:3334", Handler: router}
	exitErr := make(chan error, 1)

	// start the server in a separate goroutine, which returns when server.Shutdown is called,
	// or in case of errors.
	go func() {
		err := server.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			exitErr <- err
		}
	}()

	// block and wait for either a non nil-exit error, or the context to be cancelled,
	// which will trigger the shutdown of the server.
	select {
	case <-ctx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := server.Shutdown(ctx)

		// wait for the relay to close all open websocket connections,
		// with standard websocket close going away.
		relay.Wait()
		if err != nil {
			panic(err)
		}

	case err := <-exitErr:
		panic(err)
	}
}

func Welcome(w http.ResponseWriter, r *http.Request) {
	response := "welcome"
	name := r.PathValue("name")
	if name != "" {
		response += " " + name
	}
	w.Write([]byte(response))
}

func Save(c rely.Client, e *nostr.Event) error {
	log.Printf("received event: %v", e)
	return nil
}

func Query(ctx context.Context, c rely.Client, f nostr.Filters) ([]nostr.Event, error) {
	log.Printf("received filters %v", f)
	return nil, nil
}

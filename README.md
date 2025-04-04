# rely
A framework for building super custom relays you can *rely* on.

## Why another framework
I started this new framework inspired by [khatru](https://github.com/fiatjaf/khatru) but also frustrated by it.
Despite its initial simplicity, achieving deep customization means dealing with (and understanding) the khatru relay structure.
For the brave among you, here is the [khatru relay](https://github.com/fiatjaf/khatru/blob/master/relay.go#L54), and for the even braver, here is the almighty [HandleWebsocket](https://github.com/fiatjaf/khatru/blob/master/handlers.go#L54).

As a [grug brain dev](https://grugbrain.dev/) myself, I believe that complexity kills good software.
Instead, I've built a simple architecture that doesn't introduce unnecessary abstractions:
There is a relay, there are clients connecting to it, each with their own subscriptions. That's it.

Also, rely is [properly tested]().

## Simple to use
Like khatru, rely remains simple to use. Check out the `/example/` directory for more.

```golang
func main() {
	ctx, cancel := context.WithCancel(context.Background())
	go rely.HandleSignals(cancel)   // when pressing ctrl+c, the context is cancelled

	relay := rely.NewRelay()
	relay.OnEvent = Save
	relay.OnFilters = Query

	addr := "localhost:3334"
	log.Printf("running relay on %s", addr)

	if err := relay.StartAndServe(ctx, addr); err != nil {
		panic(err)
	}
}

func Save(c *rely.Client, e *nostr.Event) error {
	log.Printf("received event: %v", e)
	return nil
}

func Query(ctx context.Context, c *rely.Client, f nostr.Filters) ([]nostr.Event, error) {
	log.Printf("received filters %v", f)
	return nil, nil
}
```

## Well tested
How do you test a relay framework?
You bombard [a dummy implementation](https://github.com/pippellia-btc/rely/blob/main/tests/random_test.go) with thousands of connections, random events, random filters, random disconnects, and see what breaks. Then you fix it and repeat.

Here is a video showing rely handling up to 4000 concurrent clients, each sending one EVENT/REQ/s, all while handling 100 http requests/s.

I still expect there to be bugs, so please open a well-written issue and I'll fix it.

## FAQs
- Wen AUTH?
    - it's coming, yes.

- Why did you chose for `relay.OnFilters` instead of `relay.OnFilter`? After all it's easier to deal with one filter at the time.

    - Because I don't want to hide the fact that a REQ can contain multiple filters, and I want the user of the framework to deal with it. For example, he/she can decide to reject REQs that contain too many filters, or doing something like the following
        ```golang
        // TooMany rejects the REQ if the client has too many open filters.
        func TooMany(client *rely.Client, filters nostr.Filters) error {
            total := len(filters)
            for _, sub := range client.Subscriptions {
                total += len(sub.Filters)
            }

            if total > 10 {
                client.Disconnect()
                return errors.New("rate-limited: too many open filters")
            }

            return nil
        }
        ```

- Wen `<insert feature you like>`?
    - Open a well written issue and make a case for why it should be added. Keep in mind that, being a [grug brain dev](https://grugbrain.dev/), I believe:
    > best weapon against complexity spirit demon is magic word: "no"
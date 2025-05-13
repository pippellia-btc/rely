# rely
A framework for building super custom relays you can *rely* on.
Designed to be simple and stable. About 1000 lines of code.

## Installation
```
go get github.com/pippellia-btc/rely
```

## Simple and Customizable
Getting started is easy, and deep customization is just as straightforward.

```golang
relay := NewRelay()
if err := relay.StartAndServe(ctx, "localhost:3334"); err != nil {
	panic(err)
}
```

### Structural Customization
Fine-tune core parameters using functional options:

```golang
relay := NewRelay(
    WithDomain("example.com"),       // required for proper NIP-42 validation
    WithQueueCapacity(5000),         // increases capacity for traffic bursts
    WithPingPeriod(30 * time.Second),
)
```

### Behavioral Customization

Define behavior by simply defining `RelayFunctions`:
```golang
func main() {
	// ...
	relay.RejectConnection = append(relay.RejectConnection, BadIP)
	relay.RejectEvent = append(relay.RejectEvent, RejectSatan)
	relay.OnEvent = Save
}

func BadIP(s Stats, req *http.Request) error {
	if slices.Contains(blacklist, IP(req)) {
		return fmt.Errorf("you are not welcome here")
	}
	return nil
}

func RejectSatan(client *rely.Client, event *nostr.Event) error {
	if event.Kind == 666 {
		blacklist = append(blacklist, client.IP())
		client.Disconnect()
		return errors.New("not today, Satan. Not today")
	}
	return nil
}
```

## Why another framework
I started this new framework inspired by [khatru](https://github.com/fiatjaf/khatru) but also frustrated by it.
Despite its initial simplicity, achieving deep customization means dealing with (and understanding) the khatru relay structure.
For the brave among you, here is the [khatru relay struct](https://github.com/fiatjaf/khatru/blob/master/relay.go#L54), and for the even braver, here is the almighty [HandleWebsocket method](https://github.com/fiatjaf/khatru/blob/master/handlers.go#L54).

As a [grug brain dev](https://grugbrain.dev/), I believe that complexity kills good software.
Instead, I've built a simple architecture (see `/docs`) that doesn't introduce unnecessary abstractions:
There is a relay, there are clients connecting to it, each with their own subscriptions. That's it.

Also, rely is [properly tested](#well-tested).

## Well tested
How do you test a relay framework?

You bombard [a dummy implementation](https://github.com/pippellia-btc/rely/blob/main/tests/random_test.go) with thousands of connections, random events, random filters, random disconnects, and see what breaks. Then you fix it and repeat. I still expect bugs, so please open a well-written issue and I'll fix it.

[Here is a video](https://m.primal.net/QECM.mp4) showing rely handling up to 3500 concurrent clients, each sending one EVENT/REQ/s, all while handling 100 new http requests/s.

## FAQs

<details>
<summary>Why does `relay.OnReq` accept multiple filters?</summary>

Because I don't want to hide the fact that a REQ can contain multiple filters, and I want the user of the framework to deal with it.  
For example, he/she can decide to reject REQs that contain too many filters, or doing something like the following

```golang
func TooMany(client *rely.Client, filters nostr.Filters) error {
    total := len(filters)
    for _, sub := range client.Subscriptions() {
        total += len(sub.Filters)
    }

    if total > 10 {
        client.Disconnect()
        return errors.New("rate-limited: too many open filters")
    }

    return nil
}
```
</details>

<details>
<summary>When [feature you like] ?</summary>

Open a well written issue and make a case for why it should be added. Keep in mind that, being a [grug brain dev](https://grugbrain.dev/), I believe:

> best weapon against complexity spirit demon is magic word: "no"

</details>

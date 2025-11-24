package rely

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/smallset"
)

const (
	authChallengeBytes = 16
	authTimeTolerance  = time.Minute
)

var (
	ErrInvalidAuthRequest   = errors.New(`an AUTH request must follow this format: ['AUTH', {event_JSON}]`)
	ErrInvalidTimestamp     = errors.New(`created_at must be within one minute from the current time`)
	ErrInvalidAuthKind      = errors.New(`invalid AUTH kind`)
	ErrInvalidAuthChallenge = errors.New(`invalid AUTH challenge`)
	ErrInvalidAuthRelay     = errors.New(`invalid AUTH relay`)
	ErrTooManyAuthed        = errors.New("trying to auth with too many pubkeys")
)

// AuthState contains the authentication state of a client. As per NIP-42, a client
// can be authenticated with one or more pubkeys.
type authState struct {
	mu         sync.Mutex
	pubkeys    *smallset.Ordered[string]
	maxPubkeys int
	challenge  string
	domain     string
}

func (a *authState) Pubkeys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pubkeys.Items()
}

func (a *authState) IsAuthed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pubkeys.Size() > 0
}

func (a *authState) Add(pk string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.pubkeys.Size() == a.maxPubkeys {
		return fmt.Errorf("%w: max is %d", ErrTooManyAuthed, a.maxPubkeys)
	}

	a.pubkeys.Add(pk)
	return nil
}

// ValidateAuth returns the appropriate error if the auth is invalid, otherwise returns nil.
func (a *authState) Validate(auth authRequest) error {
	if auth.Event.Kind != nostr.KindClientAuthentication {
		return ErrInvalidAuthKind
	}

	if time.Since(auth.CreatedAt.Time()).Abs() > authTimeTolerance {
		return ErrInvalidTimestamp
	}

	if !auth.Event.CheckID() {
		return ErrInvalidEventID
	}

	match, err := auth.Event.CheckSignature()
	if err != nil || !match {
		return ErrInvalidEventSignature
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.domain == "" || normalizeURL(auth.Relay()) != a.domain {
		return ErrInvalidAuthRelay
	}

	if a.challenge == "" || auth.Challenge() != a.challenge {
		return ErrInvalidAuthChallenge
	}
	return nil
}

func (c *client) SendAuth() {
	bytes := make([]byte, authChallengeBytes)
	rand.Read(bytes)
	challenge := hex.EncodeToString(bytes)

	// hold the lock while sending the auth challenge to ensure syncronization.
	// Without it, multiple calls to SendAuth might send the auth challenge in the wrong order.
	c.auth.mu.Lock()
	defer c.auth.mu.Unlock()

	c.auth.pubkeys.Clear()
	c.auth.challenge = challenge
	c.send(authResponse{Challenge: challenge})
}

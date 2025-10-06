package rely

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
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
)

type auther struct {
	mu        sync.RWMutex
	pubkey    string
	challenge string

	domain string
}

func (a *auther) SetPubkey(pubkey string) {
	a.mu.Lock()
	a.pubkey = pubkey
	a.mu.Unlock()
}

func (a *auther) Pubkey() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.pubkey
}

// validateAuth returns the appropriate error if the auth is invalid, otherwise returns nil.
func (a *auther) Validate(auth *authRequest) *requestError {
	if auth.Event.Kind != nostr.KindClientAuthentication {
		return &requestError{ID: auth.ID, Err: ErrInvalidAuthKind}
	}

	if time.Since(auth.CreatedAt.Time()).Abs() > authTimeTolerance {
		return &requestError{ID: auth.ID, Err: ErrInvalidTimestamp}
	}

	if !strings.Contains(auth.Relay(), a.domain) {
		return &requestError{ID: auth.ID, Err: ErrInvalidAuthRelay}
	}

	if !auth.Event.CheckID() {
		return &requestError{ID: auth.ID, Err: ErrInvalidEventID}
	}

	match, err := auth.Event.CheckSignature()
	if err != nil || !match {
		return &requestError{ID: auth.ID, Err: ErrInvalidEventSignature}
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.challenge == "" || auth.Challenge() != a.challenge {
		return &requestError{ID: auth.ID, Err: ErrInvalidAuthChallenge}
	}
	return nil
}

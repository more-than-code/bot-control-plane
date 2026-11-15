package github

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"golang.org/x/oauth2"
)

// State limits (api-v1 v1.29 item 2, as Google sign-in states).
const (
	StateTTL = 10 * time.Minute
	stateCap = 10000
	// PerUser is the most outstanding states one user holds; a new start
	// drops that user's oldest (api-v1 v1.29 item 2).
	PerUser    = 5
	sweepEvery = time.Minute
)

var (
	// ErrState is a missing, used, expired or foreign state.
	ErrState = errors.New("github state")
	// ErrFull means the state map is at its cap.
	ErrFull = errors.New("github states full")
)

type pending struct {
	user     uuid.UUID
	project  uuid.UUID
	verifier string
	expires  time.Time
	// seq orders states made at the same instant, so "oldest" is exact.
	seq uint64
}

// States holds the single-use Connect GitHub states in memory, bound to a
// user and a project.
type States struct {
	mu        sync.Mutex
	byID      map[string]pending
	lastSweep time.Time
	seq       uint64
}

// NewStates returns an empty state map.
func NewStates() *States {
	return &States{byID: map[string]pending{}}
}

// Put makes a new state (32 random bytes, base64url) and a PKCE verifier for
// user and project.
func (s *States) Put(user, project uuid.UUID, now time.Time) (state, verifier string, err error) {
	plain, _, err := secret.NewToken()
	if err != nil {
		return "", "", err
	}
	verifier = oauth2.GenerateVerifier()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now, false)
	if len(s.byID) >= stateCap {
		// At the cap, expired states go now rather than at the next minute.
		s.sweepLocked(now, true)
		if len(s.byID) >= stateCap {
			return "", "", ErrFull
		}
	}
	s.dropOldestLocked(user, now)
	s.seq++
	s.byID[plain] = pending{user: user, project: project, verifier: verifier, expires: now.Add(StateTTL), seq: s.seq}
	return plain, verifier, nil
}

// Take uses state once. A state that is unknown, used, expired, or made for
// another user is ErrState; it is spent either way.
func (s *States) Take(state string, user uuid.UUID, now time.Time) (project uuid.UUID, verifier string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[state]
	if !ok {
		return uuid.Nil, "", ErrState
	}
	delete(s.byID, state)
	if !now.Before(p.expires) || p.user != user {
		return uuid.Nil, "", ErrState
	}
	return p.project, p.verifier, nil
}

// Len is the number of held states (tests).
func (s *States) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

// dropOldestLocked removes user's oldest live states until fewer than
// PerUser remain.
func (s *States) dropOldestLocked(user uuid.UUID, now time.Time) {
	for {
		count := 0
		oldest := ""
		var oldestSeq uint64
		for key, p := range s.byID {
			if p.user != user || !now.Before(p.expires) {
				continue
			}
			count++
			if oldest == "" || p.seq < oldestSeq {
				oldest, oldestSeq = key, p.seq
			}
		}
		if count < PerUser {
			return
		}
		delete(s.byID, oldest)
	}
}

// sweepLocked drops expired states, at most once a minute unless forced.
func (s *States) sweepLocked(now time.Time, force bool) {
	if !force && !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < sweepEvery {
		return
	}
	s.lastSweep = now
	for key, p := range s.byID {
		if !now.Before(p.expires) {
			delete(s.byID, key)
		}
	}
}

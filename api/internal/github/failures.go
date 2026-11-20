package github

import (
	"sync"
	"time"
)

// Connect failure reasons (api-v1 v1.39 item 2).
const (
	ReasonExpiredSignIn = "expired_sign_in"
	ReasonScopeMissing  = "scope_missing"
	ReasonGitHubError   = "github_error"
	ReasonForbidden     = "forbidden"
	ReasonSaveFailed    = "save_failed"
)

// failureCap bounds the failures held; a full map records nothing more
// until a sweep frees room.
const failureCap = 10000

// Failure is the last failed Connect GitHub attempt for a connect request.
type Failure struct {
	Reason string
	At     time.Time
}

type failure struct {
	Failure
	expires time.Time
}

// Failures holds the last failure per connect request in memory (api-v1
// v1.39 item 2), keyed by the request's stored hash. An entry lives until
// its request expires; a restart clears them all. A nil *Failures records
// nothing and reads nothing.
type Failures struct {
	mu        sync.Mutex
	byHash    map[string]failure
	lastSweep time.Time
}

// NewFailures returns an empty failure map.
func NewFailures() *Failures {
	return &Failures{byHash: map[string]failure{}}
}

// Record keeps reason as the last failure of the request with hash, until
// expires.
func (f *Failures) Record(hash []byte, reason string, at, expires time.Time) {
	if f == nil || !at.Before(expires) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweepLocked(at, false)
	key := string(hash)
	if _, ok := f.byHash[key]; !ok && len(f.byHash) >= failureCap {
		f.sweepLocked(at, true)
		if len(f.byHash) >= failureCap {
			return
		}
	}
	f.byHash[key] = failure{Failure: Failure{Reason: reason, At: at}, expires: expires}
}

// Last is the last failure of the request with hash at now, or nil.
func (f *Failures) Last(hash []byte, now time.Time) *Failure {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.byHash[string(hash)]
	if !ok || !now.Before(e.expires) {
		return nil
	}
	out := e.Failure
	return &out
}

// Clear drops the failure of the request with hash (a later success).
func (f *Failures) Clear(hash []byte) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byHash, string(hash))
}

// Len is the number of held failures (tests); 0 for a nil *Failures.
func (f *Failures) Len() int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byHash)
}

// sweepLocked drops expired failures, at most once a minute unless forced.
func (f *Failures) sweepLocked(now time.Time, force bool) {
	if !force && !f.lastSweep.IsZero() && now.Sub(f.lastSweep) < sweepEvery {
		return
	}
	f.lastSweep = now
	for key, e := range f.byHash {
		if !now.Before(e.expires) {
			delete(f.byHash, key)
		}
	}
}

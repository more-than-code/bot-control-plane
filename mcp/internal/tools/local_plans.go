package tools

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// localPlans holds confirmations this MCP server keeps itself, for writes
// the API accepts directly from a delegated session: upgrade_agent (api-v1
// v1.48, pendingUpgrades). A plan is bound to the caller (the SHA-256 of the
// caller's token, from auth.CallerKeyFromContext) and the idempotency key,
// waits the store's TTL for confirm, and is used once. A restart of the MCP
// server drops pending plans; the API holds no state for them.

const (
	// maxLocalPlans caps one store's pending plans.
	maxLocalPlans = 1000
	// maxLocalPlansPerCaller caps one caller's pending plans in one store.
	maxLocalPlansPerCaller = 10
)

type localPlan[T any] struct {
	caller  [32]byte
	key     string
	expires time.Time
	value   T
}

type localPlans[T any] struct {
	mu    sync.Mutex
	plans map[string]localPlan[T]
	now   func() time.Time
	// ttl is how long a plan waits for confirm.
	ttl time.Duration
	// noun names the plans in the refusals ("upgrade " gives "too many
	// pending upgrade confirmations").
	noun string
}

func newLocalPlans[T any](ttl time.Duration, noun string) *localPlans[T] {
	return &localPlans[T]{plans: map[string]localPlan[T]{}, now: time.Now, ttl: ttl, noun: noun}
}

// add stores value for caller and key under a new 43-character id (the
// confirmation_id pattern) and returns it with the expiry. Expired plans
// are dropped first; a full store, or a caller with
// maxLocalPlansPerCaller pending plans, refuses.
func (s *localPlans[T]) add(caller [32]byte, key string, value T) (string, time.Time, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, err
	}
	id := base64.RawURLEncoding.EncodeToString(raw[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	mine := 0
	for k, v := range s.plans {
		if !now.Before(v.expires) {
			delete(s.plans, k)
			continue
		}
		if v.caller == caller {
			mine++
		}
	}
	if len(s.plans) >= maxLocalPlans {
		return "", time.Time{}, fmt.Errorf("too many pending %sconfirmations; try again later", s.noun)
	}
	if mine >= maxLocalPlansPerCaller {
		return "", time.Time{}, fmt.Errorf("too many pending %sconfirmations for you; confirm or let some expire (%d minutes)", s.noun, int(s.ttl/time.Minute))
	}
	expires := now.Add(s.ttl)
	s.plans[id] = localPlan[T]{caller: caller, key: key, expires: expires, value: value}
	return id, expires, nil
}

// take returns the plan for id. found is false for an id this store did
// not issue and for another caller's plan, which is left alone. A key
// mismatch keeps the plan; an expired plan is dropped.
func (s *localPlans[T]) take(caller [32]byte, id, key string) (value T, found bool, fail *mcp.CallToolResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[id]
	if !ok || p.caller != caller {
		return value, false, nil
	}
	if p.key != key {
		return value, true, toolError("conflict", "idempotency_key does not match the request that issued this confirmation")
	}
	delete(s.plans, id)
	if !s.now().Before(p.expires) {
		return value, true, toolError("conflict", "this confirmation expired; call the tool again for a new one")
	}
	return p.value, true, nil
}

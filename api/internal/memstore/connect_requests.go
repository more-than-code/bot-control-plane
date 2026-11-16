package memstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Connect requests (api-v1 v1.31). Every method holds m.mu, so a connect
// and a decline of one request never both succeed.

func (m *Mem) CreateConnectRequest(_ context.Context, hash []byte, in store.ConnectRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.projectAliveLocked(in.ProjectID) {
		return store.ErrNotFound
	}
	pruneRequestsLocked(m.connectRequests, in.CreatedAt)
	// At most one open request per user and project: the one this replaces
	// is not counted, and expires only if the create succeeds.
	open, replaced := openRequestsLocked(m.connectRequests, in.UserID, in.CreatedAt, func(r store.ConnectRequest) bool {
		return r.ProjectID == in.ProjectID
	})
	if open >= store.MaxOpenRequests {
		return store.ErrTooManyConnectRequests
	}
	if m.connectRequests == nil {
		m.connectRequests = map[string]store.ConnectRequest{}
	}
	if _, dup := m.connectRequests[hashKey(hash)]; dup {
		return store.ErrConflict
	}
	if replaced != "" {
		r := m.connectRequests[replaced]
		r.ExpiresAt = in.CreatedAt
		m.connectRequests[replaced] = r
	}
	in.State = store.RequestPending
	in.DoneAt = nil
	m.connectRequests[hashKey(hash)] = in
	return nil
}

func (m *Mem) GetConnectRequest(_ context.Context, hash []byte) (store.ConnectRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.connectRequests[hashKey(hash)]
	if !ok || !m.projectAliveLocked(r.ProjectID) {
		return store.ConnectRequest{}, store.ErrNotFound
	}
	return r, nil
}

func (m *Mem) DeclineConnectRequest(_ context.Context, hash []byte, userID uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, found := m.connectRequests[hashKey(hash)]
	if err := m.answerableLocked(req.RequestMeta, found, userID, at); err != nil {
		return err
	}
	done := at
	req.State = store.RequestDeclined
	req.DoneAt = &done
	m.connectRequests[hashKey(hash)] = req
	return nil
}

func (m *Mem) ConnectRegistry(_ context.Context, projectID uuid.UUID, host string, in store.RegistryWrite, actor uuid.UUID, at time.Time, requestHash []byte) (store.Registry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reg, err := m.putRegistryLocked(projectID, host, in, actor, at)
	if err != nil {
		return store.Registry{}, err
	}
	req, found := m.connectRequests[hashKey(requestHash)]
	if m.answerableLocked(req.RequestMeta, found, actor, at) != nil || req.ProjectID != projectID {
		return reg, nil
	}
	done := at
	req.State = store.RequestConnected
	req.DoneAt = &done
	m.connectRequests[hashKey(requestHash)] = req
	return reg, nil
}

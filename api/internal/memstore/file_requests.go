package memstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// File requests (api-v1 v1.30). Every method holds m.mu, so a fill, a
// decline and a file put or delete of the same project never interleave.

func (m *Mem) CreateFileRequest(_ context.Context, hash []byte, in store.FileRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !catalog.ValidFileName(in.Name) {
		return store.ErrInvalid
	}
	if !m.projectAliveLocked(in.ProjectID) {
		return store.ErrNotFound
	}
	pruneRequestsLocked(m.fileRequests, in.CreatedAt)
	if _, exists := m.projectFiles[envKey(in.ProjectID, in.Name)]; exists {
		return store.ErrFileExists
	}
	if m.fileCountLocked(in.ProjectID) >= store.MaxProjectFiles {
		return store.ErrTooManyFiles
	}
	// At most one open request per user and name: the one this replaces is
	// not counted, and expires only if the create succeeds.
	open, replaced := openRequestsLocked(m.fileRequests, in.UserID, in.CreatedAt, func(r store.FileRequest) bool {
		return r.ProjectID == in.ProjectID && r.Name == in.Name
	})
	if open >= store.MaxOpenRequests {
		return store.ErrTooManyFileRequests
	}
	if m.fileRequests == nil {
		m.fileRequests = map[string]store.FileRequest{}
	}
	if _, dup := m.fileRequests[hashKey(hash)]; dup {
		return store.ErrConflict
	}
	if replaced != "" {
		r := m.fileRequests[replaced]
		r.ExpiresAt = in.CreatedAt
		m.fileRequests[replaced] = r
	}
	in.State = store.RequestPending
	in.DoneAt = nil
	m.fileRequests[hashKey(hash)] = in
	return nil
}

func (m *Mem) GetFileRequest(_ context.Context, hash []byte) (store.FileRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.fileRequests[hashKey(hash)]
	if !ok || !m.projectAliveLocked(r.ProjectID) {
		return store.FileRequest{}, store.ErrNotFound
	}
	return r, nil
}

func (m *Mem) FillFileRequest(_ context.Context, hash []byte, userID uuid.UUID, at time.Time, in store.FileWrite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, err := m.openFileRequestLocked(hash, userID, at)
	if err != nil {
		return err
	}
	if !fileWriteOK(req.Name, in) {
		return store.ErrInvalid
	}
	key := envKey(req.ProjectID, req.Name)
	if _, exists := m.projectFiles[key]; exists {
		return store.ErrFileExists
	}
	if m.fileCountLocked(req.ProjectID) >= store.MaxProjectFiles {
		return store.ErrTooManyFiles
	}
	rec := fileRec{
		ProjectID: req.ProjectID, Name: req.Name, IsSecret: in.IsSecret, Plain: in.Plain,
		Nonce: cloneBytes(in.Nonce), Ciphertext: cloneBytes(in.Ciphertext),
		KeyVersion: in.KeyVersion, SHA256: in.SHA256, Size: in.Size,
		UpdatedAt: at, UpdatedBy: userID,
	}
	if rec.IsSecret {
		rec.Plain = ""
	}
	m.projectFiles[key] = rec
	done := at
	req.State = store.RequestFilled
	req.DoneAt = &done
	m.fileRequests[hashKey(hash)] = req
	return nil
}

func (m *Mem) DeclineFileRequest(_ context.Context, hash []byte, userID uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, err := m.openFileRequestLocked(hash, userID, at)
	if err != nil {
		return err
	}
	done := at
	req.State = store.RequestDeclined
	req.DoneAt = &done
	m.fileRequests[hashKey(hash)] = req
	return nil
}

// openFileRequestLocked is a request of userID, of a live project, that is
// pending and unexpired.
func (m *Mem) openFileRequestLocked(hash []byte, userID uuid.UUID, at time.Time) (store.FileRequest, error) {
	req, found := m.fileRequests[hashKey(hash)]
	if err := m.answerableLocked(req.RequestMeta, found, userID, at); err != nil {
		return store.FileRequest{}, err
	}
	return req, nil
}

// ProjectFileExists reports whether the project has the file, reading no content.
func (m *Mem) ProjectFileExists(_ context.Context, projectID uuid.UUID, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.projectAliveLocked(projectID) {
		return false, store.ErrNotFound
	}
	_, ok := m.projectFiles[envKey(projectID, name)]
	return ok, nil
}

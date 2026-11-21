package memstore

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Host config files (api-v1 v1.44), as db/host_files.go.

type hostFileData struct {
	versions map[string][]store.HostFileVersion // key host|name, oldest first
	applies  map[string][]store.HostFileApply   // key host|name, oldest first
	status   map[uuid.UUID]store.HostFilesStatus
}

func hostFileKey(hostID uuid.UUID, name string) string { return hostID.String() + "|" + name }

func (m *Mem) hostFilesLocked() *hostFileData {
	d := &m.hostFiles
	if d.versions == nil {
		d.versions = map[string][]store.HostFileVersion{}
		d.applies = map[string][]store.HostFileApply{}
		d.status = map[uuid.UUID]store.HostFilesStatus{}
	}
	return d
}

// appliedLocked is the version of the last apply that succeeded or was unchanged.
func appliedLocked(applies []store.HostFileApply) *int {
	for i := len(applies) - 1; i >= 0; i-- {
		if o := applies[i].Outcome; o == store.HostFileSucceeded || o == store.HostFileUnchanged {
			v := applies[i].Version
			return &v
		}
	}
	return nil
}

func (m *Mem) SaveHostFileVersion(_ context.Context, in store.HostFileSave) (int, error) {
	if !store.ValidHostFileSave(in) {
		return 0, store.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[in.HostID]; !ok {
		return 0, store.ErrNotFound
	}
	if _, ok := m.users[in.CreatedBy]; !ok {
		return 0, store.ErrNotFound
	}
	d := m.hostFilesLocked()
	key := hostFileKey(in.HostID, in.Name)
	rows := d.versions[key]
	latest := 0
	if len(rows) > 0 {
		latest = rows[len(rows)-1].Version
	}
	if in.BaseVersion != nil && *in.BaseVersion != latest {
		return 0, store.ErrConflict
	}
	v := store.HostFileVersion{
		HostID: in.HostID, Name: in.Name, Version: latest + 1, Secret: in.Secret, Size: in.Size,
		SHA256: in.SHA256, Plain: in.Plain, CreatedBy: in.CreatedBy, CreatedAt: in.At,
	}
	if in.Secret {
		nonce, ct, kv, err := in.Seal(v.Version)
		if err != nil {
			return 0, err
		}
		if len(nonce) != 12 || len(ct) != in.Size+16 || kv < 1 {
			return 0, store.ErrInvalid
		}
		v.Nonce, v.Ciphertext, v.KeyVersion = append([]byte(nil), nonce...), append([]byte(nil), ct...), kv
	}
	rows = append(rows, v)
	applied := appliedLocked(d.applies[key])
	protect := 0
	if in.Protect != nil {
		protect = in.Protect()
	}
	if len(rows) > store.HostFileKeep {
		cut := rows[len(rows)-store.HostFileKeep].Version
		kept := rows[:0]
		for _, r := range rows {
			if r.Version >= cut || (applied != nil && r.Version == *applied) || r.Version == protect {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	d.versions[key] = rows
	return v.Version, nil
}

func (m *Mem) ListHostFileVersions(_ context.Context, hostID uuid.UUID, name string) ([]store.HostFileVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.hostFilesLocked().versions[hostFileKey(hostID, name)]
	out := make([]store.HostFileVersion, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		v := rows[i]
		v.Plain, v.Nonce, v.Ciphertext, v.KeyVersion = "", nil, nil, 0
		out = append(out, v)
	}
	return out, nil
}

func (m *Mem) GetHostFileVersion(_ context.Context, hostID uuid.UUID, name string, version int) (store.HostFileVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.hostFilesLocked().versions[hostFileKey(hostID, name)]
	for i := len(rows) - 1; i >= 0; i-- {
		if version <= 0 || rows[i].Version == version {
			v := rows[i]
			v.Nonce = append([]byte(nil), v.Nonce...)
			v.Ciphertext = append([]byte(nil), v.Ciphertext...)
			return v, nil
		}
	}
	return store.HostFileVersion{}, store.ErrNotFound
}

func (m *Mem) RecordHostFileApply(_ context.Context, in store.HostFileApply) error {
	if !store.ValidHostFileName(in.Name) || !store.ValidHostFileOutcome(in.Outcome) || in.Version < 1 {
		return store.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[in.HostID]; !ok {
		return store.ErrNotFound
	}
	d := m.hostFilesLocked()
	key := hostFileKey(in.HostID, in.Name)
	d.applies[key] = append(d.applies[key], in)
	return nil
}

func (m *Mem) HostFileStates(_ context.Context, hostID uuid.UUID) ([]store.HostFileState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.hostFilesLocked()
	byName := map[string]*store.HostFileState{}
	get := func(name string) *store.HostFileState {
		s, ok := byName[name]
		if !ok {
			s = &store.HostFileState{Name: name}
			byName[name] = s
		}
		return s
	}
	prefix := hostID.String() + "|"
	for key, rows := range d.versions {
		if len(rows) == 0 || len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		v := rows[len(rows)-1].Version
		get(key[len(prefix):]).LatestVersion = &v
	}
	for key, rows := range d.applies {
		if len(rows) == 0 || len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		s := get(key[len(prefix):])
		last := rows[len(rows)-1]
		s.LastApply = &last
		s.AppliedVersion = appliedLocked(rows)
	}
	out := make([]store.HostFileState, 0, len(byName))
	for _, s := range byName {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func cloneHostFilesStatus(s store.HostFilesStatus) store.HostFilesStatus {
	files := make([]store.HostFileStatus, len(s.Files))
	for i, f := range s.Files {
		if f.MatchesApplied != nil {
			b := *f.MatchesApplied
			f.MatchesApplied = &b
		}
		if f.Size != nil {
			n := *f.Size
			f.Size = &n
		}
		if f.BadSince != nil {
			t := *f.BadSince
			f.BadSince = &t
		}
		files[i] = f
	}
	s.Files = files
	return s
}

func (m *Mem) GetHostFilesStatus(_ context.Context, hostID uuid.UUID) (*store.HostFilesStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[hostID]; !ok {
		return nil, store.ErrNotFound
	}
	s, ok := m.hostFilesLocked().status[hostID]
	if !ok {
		return nil, nil
	}
	c := cloneHostFilesStatus(s)
	return &c, nil
}

func (m *Mem) SetHostFilesStatus(_ context.Context, hostID uuid.UUID, status store.HostFilesStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[hostID]; !ok {
		return store.ErrNotFound
	}
	m.hostFilesLocked().status[hostID] = cloneHostFilesStatus(status)
	return nil
}

func (m *Mem) ListHostFilesStatuses(_ context.Context) ([]store.HostFilesStatusRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.HostFilesStatusRow{}
	for id, s := range m.hostFilesLocked().status {
		h, ok := m.hosts[id]
		if !ok || h.RevokedAt != nil {
			continue
		}
		out = append(out, store.HostFilesStatusRow{HostID: id, OrgID: h.OrgID, Name: h.Name, Status: cloneHostFilesStatus(s)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostID.String() < out[j].HostID.String() })
	return out, nil
}

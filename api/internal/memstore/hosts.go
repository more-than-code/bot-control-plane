package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

func (m *Mem) CreateJoinToken(_ context.Context, orgID uuid.UUID, hash []byte, hint *string, grant *store.TokenGrant, createdBy uuid.UUID, expiresAt, at time.Time) (store.JoinToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return store.JoinToken{}, store.ErrNotFound
	}
	if grant != nil {
		if !store.ValidAccessLevel(grant.Level) {
			return store.JoinToken{}, store.ErrInvalid
		}
		p, ok := m.projects[grant.ProjectID]
		if !ok || p.DeletedAt != nil || p.OrgID != orgID {
			return store.JoinToken{}, store.ErrNotFound
		}
		g := *grant
		grant = &g
	}
	tok := store.JoinToken{
		ID:           uuid.New(),
		OrgID:        orgID,
		Grant:        grant,
		HostNameHint: hint,
		CreatedBy:    createdBy,
		CreatedAt:    at,
		ExpiresAt:    expiresAt,
	}
	m.tokens[tok.ID] = tokenRec{JoinToken: tok, hash: cloneBytes(hash)}
	m.tokenByHash[hashKey(hash)] = tok.ID
	return tok, nil
}

func tokenMatches(tok store.JoinToken, orgID uuid.UUID, grantProject *uuid.UUID) bool {
	if tok.OrgID != orgID {
		return false
	}
	if grantProject == nil {
		return true
	}
	return tok.Grant != nil && tok.Grant.ProjectID == *grantProject
}

func (m *Mem) ListJoinTokens(_ context.Context, orgID uuid.UUID, grantProject *uuid.UUID, limit int, cursor *store.Cursor) ([]store.JoinToken, *store.Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var items []store.JoinToken
	for _, tok := range m.tokens {
		if tokenMatches(tok.JoinToken, orgID, grantProject) {
			items = append(items, tok.JoinToken)
		}
	}
	out, next := pageBy(items, limit, cursor, func(t store.JoinToken) time.Time { return t.CreatedAt }, func(t store.JoinToken) string { return t.ID.String() }, false)
	return out, next, nil
}

func (m *Mem) RevokeJoinToken(_ context.Context, orgID uuid.UUID, grantProject *uuid.UUID, tokenID uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok, ok := m.tokens[tokenID]
	if !ok || !tokenMatches(tok.JoinToken, orgID, grantProject) {
		return store.ErrNotFound
	}
	if tok.RevokedAt == nil {
		tok.RevokedAt = &at
		m.tokens[tokenID] = tok
	}
	return nil
}

func (m *Mem) ConsumeJoinToken(_ context.Context, tokenHash []byte, in store.EnrollInput, credentialID uuid.UUID, credentialHash []byte, at time.Time) (store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.tokenByHash[hashKey(tokenHash)]
	if !ok || !secret.Equal(tokenHash, m.tokens[id].hash) {
		secret.Equal(tokenHash, dummyHash)
		return store.Host{}, store.ErrUnauthorized
	}
	tok := m.tokens[id]
	if tok.UsedAt != nil || tok.RevokedAt != nil || !at.Before(tok.ExpiresAt) {
		return store.Host{}, store.ErrUnauthorized
	}
	if _, ok := m.liveOrgLocked(tok.OrgID); !ok {
		return store.Host{}, store.ErrUnauthorized
	}
	// A grant whose project has since been deleted (or moved) is dropped;
	// the host still joins the org.
	var grant *store.TokenGrant
	if tok.Grant != nil {
		if p, ok := m.projects[tok.Grant.ProjectID]; ok && p.DeletedAt == nil && p.OrgID == tok.OrgID {
			grant = tok.Grant
		}
	}
	name, err := store.Unique(in.Name, 63, func(candidate string) bool {
		for _, h := range m.hosts {
			if h.OrgID == tok.OrgID && h.Name == candidate {
				return true
			}
		}
		return false
	})
	if err != nil {
		return store.Host{}, err
	}
	host := store.Host{
		ID:           uuid.New(),
		OrgID:        tok.OrgID,
		Name:         name,
		NodeType:     in.NodeType,
		OS:           in.OS,
		Arch:         in.Arch,
		AlwaysOn:     in.AlwaysOn,
		AgentVersion: in.AgentVersion,
		LastSeenAt:   &at,
		EnrolledAt:   at,
	}
	if grant != nil {
		pid := grant.ProjectID
		host.ProjectID = &pid
		by := tok.CreatedBy
		m.access[accessKey(host.ID, pid)] = store.HostAccess{HostID: host.ID, ProjectID: pid, Level: grant.Level, GrantedBy: &by, GrantedAt: at}
	}
	m.hosts[host.ID] = host
	m.creds[credentialID] = credRec{Credential: store.Credential{
		ID:     credentialID,
		HostID: host.ID,
		Hash:   cloneBytes(credentialHash),
	}}
	m.credByHash[hashKey(credentialHash)] = credentialID
	tok.UsedAt = &at
	tok.UsedByHostID = &host.ID
	m.tokens[id] = tok
	return cloneHost(host), nil
}

func (m *Mem) GetHost(_ context.Context, id uuid.UUID) (store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hosts[id]
	if !ok {
		return store.Host{}, store.ErrNotFound
	}
	return cloneHost(h), nil
}

func (m *Mem) ListHosts(_ context.Context, projectID uuid.UUID, limit int, cursor *store.Cursor) ([]store.Host, *store.Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []store.Host
	for _, a := range m.access {
		if a.ProjectID != projectID {
			continue
		}
		h, ok := m.hosts[a.HostID]
		if !ok {
			continue
		}
		h = cloneHost(h)
		level := a.Level
		h.AccessLevel = &level
		all = append(all, h)
	}
	items, next := pageHosts(all, limit, cursor)
	return items, next, nil
}

func (m *Mem) ListOrgHosts(_ context.Context, orgID uuid.UUID, limit int, cursor *store.Cursor) ([]store.Host, *store.Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return nil, nil, store.ErrNotFound
	}
	var all []store.Host
	for _, h := range m.hosts {
		if h.OrgID != orgID {
			continue
		}
		h = cloneHost(h)
		access := m.hostAccessLocked(h.ID)
		h.Access = &access
		all = append(all, h)
	}
	items, next := pageHosts(all, limit, cursor)
	return items, next, nil
}

// pageHosts sorts by (name, id) and pages with a rank-0 host cursor.
func pageHosts(all []store.Host, limit int, cursor *store.Cursor) ([]store.Host, *store.Cursor) {
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].ID.String() < all[j].ID.String()
	})
	items := make([]store.Host, 0, len(all))
	for _, h := range all {
		if hostAfter(h, cursor) {
			items = append(items, h)
		}
	}
	if limit <= 0 {
		limit = 50
	}
	var next *store.Cursor
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		rank := 0
		next = &store.Cursor{Rank: &rank, Name: last.Name, ID: last.ID.String()}
	}
	return items, next
}

// hostAfter reports whether h is strictly after the host-list cursor.
// A nil cursor, or one without a rank, starts at the beginning. A rank above
// 0 (an old shared-hosts cursor) is past every host. The cursor host does
// not have to still be in the list.
func hostAfter(h store.Host, cursor *store.Cursor) bool {
	if cursor == nil || cursor.Rank == nil {
		return true
	}
	if *cursor.Rank > 0 {
		return false
	}
	if *cursor.Rank < 0 {
		return true
	}
	if h.Name != cursor.Name {
		return h.Name > cursor.Name
	}
	return h.ID.String() > cursor.ID
}

func (m *Mem) RevokeHost(_ context.Context, hostID uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hosts[hostID]
	if !ok {
		return store.ErrNotFound
	}
	if h.RevokedAt == nil {
		h.RevokedAt = &at
		m.hosts[hostID] = h
	}
	m.revokeCredsLocked(hostID, at)
	m.markHostServicesGoneLocked(hostID)
	return nil
}

func (m *Mem) TouchHost(_ context.Context, hostID uuid.UUID, at time.Time, version, osName, arch string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hosts[hostID]
	if !ok {
		return store.ErrNotFound
	}
	h.LastSeenAt = &at
	if version != "" {
		h.AgentVersion = version
	}
	if osName != "" {
		h.OS = osName
	}
	if arch != "" {
		h.Arch = arch
	}
	m.hosts[hostID] = h
	return nil
}

func (m *Mem) LookupCredential(_ context.Context, hash []byte) (store.Credential, store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.credByHash[hashKey(hash)]
	if !ok {
		secret.Equal(hash, dummyHash)
		return store.Credential{}, store.Host{}, store.ErrUnauthorized
	}
	cred := m.creds[id]
	if !secret.Equal(hash, cred.Hash) {
		return store.Credential{}, store.Host{}, store.ErrUnauthorized
	}
	host, ok := m.hosts[cred.HostID]
	if !ok {
		return store.Credential{}, store.Host{}, store.ErrUnauthorized
	}
	return cred.Credential, cloneHost(host), nil
}

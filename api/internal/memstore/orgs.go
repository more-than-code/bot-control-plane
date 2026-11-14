package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

func orgMemberKey(org, user uuid.UUID) string {
	return org.String() + "/" + user.String()
}

// PersonalOrg returns the oldest org the user owns, or uuid.Nil. It is a
// test helper: UpsertGoogleUser creates one org per new user (D-8).
func (m *Mem) PersonalOrg(userID uuid.UUID) uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best store.Org
	for _, om := range m.orgMembers {
		if om.UserID != userID || om.Role != store.OrgRoleOwner {
			continue
		}
		o, ok := m.orgs[om.OrgID]
		if !ok || o.DeletedAt != nil {
			continue
		}
		if best.ID == uuid.Nil || o.CreatedAt.Before(best.CreatedAt) || (o.CreatedAt.Equal(best.CreatedAt) && o.ID.String() < best.ID.String()) {
			best = o
		}
	}
	return best.ID
}

// inAnyOrgLocked reports whether the user is a member of a live org.
func (m *Mem) inAnyOrgLocked(userID uuid.UUID) bool {
	for _, om := range m.orgMembers {
		if om.UserID != userID {
			continue
		}
		if _, ok := m.liveOrgLocked(om.OrgID); ok {
			return true
		}
	}
	return false
}

func (m *Mem) createOrgLocked(ownerID uuid.UUID, name, baseSlug string, at time.Time) (store.Org, error) {
	if _, ok := m.users[ownerID]; !ok {
		return store.Org{}, store.ErrNotFound
	}
	slug, err := store.Unique(baseSlug, store.MaxOrgSlug, func(s string) bool {
		_, taken := m.orgBySlug[s]
		return taken
	})
	if err != nil {
		return store.Org{}, err
	}
	o := store.Org{ID: uuid.New(), Name: name, Slug: slug, CreatedBy: ownerID, CreatedAt: at}
	m.orgs[o.ID] = o
	m.orgBySlug[slug] = o.ID
	m.orgMembers[orgMemberKey(o.ID, ownerID)] = orgMemberRec{OrgID: o.ID, UserID: ownerID, Role: store.OrgRoleOwner, AddedBy: ownerID, AddedAt: at}
	o.Role = store.OrgRoleOwner
	return o, nil
}

func (m *Mem) CreateOrg(_ context.Context, ownerID uuid.UUID, name, baseSlug string, at time.Time) (store.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createOrgLocked(ownerID, name, baseSlug, at)
}

func (m *Mem) liveOrgLocked(id uuid.UUID) (store.Org, bool) {
	o, ok := m.orgs[id]
	if !ok || o.DeletedAt != nil {
		return store.Org{}, false
	}
	return o, true
}

func (m *Mem) GetOrg(_ context.Context, id uuid.UUID) (store.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.liveOrgLocked(id)
	if !ok {
		return store.Org{}, store.ErrNotFound
	}
	return o, nil
}

// orgRoleLocked is the user's role in a live org, or "".
func (m *Mem) orgRoleLocked(orgID, userID uuid.UUID) string {
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return ""
	}
	return m.orgMembers[orgMemberKey(orgID, userID)].Role
}

func (m *Mem) OrgRole(_ context.Context, orgID, userID uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role := m.orgRoleLocked(orgID, userID)
	if role == "" {
		return "", store.ErrNotFound
	}
	return role, nil
}

func (m *Mem) ListUserOrgs(_ context.Context, userID uuid.UUID) ([]store.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.Org{}
	for _, om := range m.orgMembers {
		if om.UserID != userID {
			continue
		}
		o, ok := m.liveOrgLocked(om.OrgID)
		if !ok {
			continue
		}
		o.Role = om.Role
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

func (m *Mem) RenameOrg(_ context.Context, id uuid.UUID, name string) (store.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.liveOrgLocked(id)
	if !ok {
		return store.Org{}, store.ErrNotFound
	}
	o.Name = name
	m.orgs[id] = o
	return o, nil
}

func (m *Mem) DeleteOrg(_ context.Context, id uuid.UUID, at time.Time) ([]uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.liveOrgLocked(id)
	if !ok {
		return nil, store.ErrNotFound
	}
	o.DeletedAt = &at
	m.orgs[id] = o
	for pid, p := range m.projects {
		if p.OrgID == id && p.DeletedAt == nil {
			m.deleteProjectLocked(pid, at)
		}
	}
	var ids []uuid.UUID
	for hid, h := range m.hosts {
		if h.OrgID != id {
			continue
		}
		ids = append(ids, hid)
		if h.RevokedAt == nil {
			h.RevokedAt = &at
			m.hosts[hid] = h
		}
		m.revokeCredsLocked(hid, at)
		m.markHostServicesGoneLocked(hid)
	}
	for tid, tok := range m.tokens {
		if tok.OrgID == id && tok.RevokedAt == nil {
			t := at
			tok.RevokedAt = &t
			m.tokens[tid] = tok
		}
	}
	sortUUIDs(ids)
	return ids, nil
}

func (m *Mem) orgMemberLocked(rec orgMemberRec) store.OrgMember {
	u := m.users[rec.UserID]
	return store.OrgMember{UserID: rec.UserID, Email: u.Email, Name: u.Name, Role: rec.Role, AddedAt: rec.AddedAt}
}

func (m *Mem) ListOrgMembers(_ context.Context, orgID uuid.UUID, limit int, cursor *store.Cursor) ([]store.OrgMember, *store.Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return nil, nil, store.ErrNotFound
	}
	var items []store.OrgMember
	for _, om := range m.orgMembers {
		if om.OrgID == orgID {
			items = append(items, m.orgMemberLocked(om))
		}
	}
	out, next := pageBy(items, limit, cursor, func(it store.OrgMember) time.Time { return it.AddedAt }, func(it store.OrgMember) string { return it.UserID.String() }, false)
	return out, next, nil
}

func (m *Mem) AddOrgMember(_ context.Context, orgID, userID uuid.UUID, role string, addedBy uuid.UUID, at time.Time) (store.OrgMember, error) {
	if role != store.OrgRoleAdmin && role != store.OrgRoleMember {
		return store.OrgMember{}, store.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return store.OrgMember{}, store.ErrNotFound
	}
	if _, ok := m.users[userID]; !ok {
		return store.OrgMember{}, store.ErrNotFound
	}
	key := orgMemberKey(orgID, userID)
	if _, exists := m.orgMembers[key]; exists {
		return store.OrgMember{}, store.ErrConflict
	}
	rec := orgMemberRec{OrgID: orgID, UserID: userID, Role: role, AddedBy: addedBy, AddedAt: at}
	m.orgMembers[key] = rec
	return m.orgMemberLocked(rec), nil
}

func (m *Mem) UpdateOrgMemberRole(_ context.Context, orgID, userID uuid.UUID, role string) (store.OrgMember, error) {
	if role != store.OrgRoleAdmin && role != store.OrgRoleMember {
		return store.OrgMember{}, store.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := orgMemberKey(orgID, userID)
	rec, ok := m.orgMembers[key]
	if _, live := m.liveOrgLocked(orgID); !ok || !live {
		return store.OrgMember{}, store.ErrNotFound
	}
	if rec.Role == store.OrgRoleOwner {
		return store.OrgMember{}, store.ErrOwner
	}
	rec.Role = role
	m.orgMembers[key] = rec
	return m.orgMemberLocked(rec), nil
}

func (m *Mem) RemoveOrgMember(_ context.Context, orgID, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := orgMemberKey(orgID, userID)
	rec, ok := m.orgMembers[key]
	if _, live := m.liveOrgLocked(orgID); !ok || !live {
		return store.ErrNotFound
	}
	if rec.Role == store.OrgRoleOwner {
		return store.ErrOwner
	}
	delete(m.orgMembers, key)
	return nil
}

func (m *Mem) TransferOrgOwnership(_ context.Context, orgID, newOwner uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return store.ErrNotFound
	}
	targetKey := orgMemberKey(orgID, newOwner)
	target, ok := m.orgMembers[targetKey]
	if !ok {
		return store.ErrNotFound
	}
	for k, om := range m.orgMembers {
		if om.OrgID == orgID && om.Role == store.OrgRoleOwner {
			if k == targetKey {
				return nil
			}
			om.Role = store.OrgRoleAdmin
			m.orgMembers[k] = om
		}
	}
	target.Role = store.OrgRoleOwner
	m.orgMembers[targetKey] = target
	return nil
}

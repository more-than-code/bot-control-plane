package memstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

func (m *Mem) CreateProject(_ context.Context, orgID, ownerID uuid.UUID, name, baseSlug string, at time.Time) (store.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[ownerID]; !ok {
		return store.Project{}, store.ErrNotFound
	}
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return store.Project{}, store.ErrNotFound
	}
	slug, err := store.Unique(baseSlug, 80, func(s string) bool {
		_, taken := m.bySlug[s]
		return taken
	})
	if err != nil {
		return store.Project{}, err
	}
	p := store.Project{
		ID:        uuid.New(),
		OrgID:     orgID,
		Name:      name,
		Slug:      slug,
		Role:      storeRoleOwner,
		Via:       "member",
		CreatedBy: ownerID,
		CreatedAt: at,
	}
	stored := p
	stored.Role, stored.Via = "", ""
	m.projects[p.ID] = stored
	m.bySlug[slug] = p.ID
	m.members[memberKey(p.ID, ownerID)] = memberRec{
		ProjectID: p.ID,
		UserID:    ownerID,
		Role:      storeRoleOwner,
		AddedBy:   ownerID,
		CreatedAt: at,
	}
	return p, nil
}

const storeRoleOwner = "owner"

// reachableLocked returns the project with the caller's role and via, and
// whether the caller reaches it (api-v1 v1.24 item 1). A membership wins.
func (m *Mem) reachableLocked(p store.Project, userID uuid.UUID) (store.Project, bool) {
	if mem, ok := m.members[memberKey(p.ID, userID)]; ok {
		p.Role, p.Via = mem.Role, "member"
		return p, true
	}
	if store.OrgAdmin(m.orgRoleLocked(p.OrgID, userID)) {
		p.Role, p.Via = "admin", "org"
		return p, true
	}
	return p, false
}

func (m *Mem) reachableProjectsLocked(userID uuid.UUID, orgID *uuid.UUID) []store.Project {
	var items []store.Project
	for _, p := range m.projects {
		if p.DeletedAt != nil || (orgID != nil && p.OrgID != *orgID) {
			continue
		}
		if r, ok := m.reachableLocked(p, userID); ok {
			r.OrgName = m.orgs[p.OrgID].Name
			items = append(items, r)
		}
	}
	return items
}

func (m *Mem) ListProjects(_ context.Context, userID uuid.UUID, limit int, cursor *store.Cursor) ([]store.Project, *store.Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out, next := pageProjects(m.reachableProjectsLocked(userID, nil), limit, cursor)
	return out, next, nil
}

func (m *Mem) ListAllProjects(ctx context.Context, userID uuid.UUID) ([]store.Project, error) {
	items, _, err := m.ListProjects(ctx, userID, 10000, nil)
	return items, err
}

func (m *Mem) ListOrgProjects(_ context.Context, orgID, userID uuid.UUID) ([]store.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return nil, store.ErrNotFound
	}
	out, _ := pageProjects(m.reachableProjectsLocked(userID, &orgID), 10000, nil)
	if out == nil {
		out = []store.Project{}
	}
	return out, nil
}

func (m *Mem) GetProject(_ context.Context, id uuid.UUID) (store.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok || p.DeletedAt != nil {
		return store.Project{}, store.ErrNotFound
	}
	return p, nil
}

func (m *Mem) Role(_ context.Context, projectID, userID uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[projectID]
	if !ok || p.DeletedAt != nil {
		return "", store.ErrNotFound
	}
	mem, ok := m.members[memberKey(projectID, userID)]
	if !ok {
		return "", store.ErrNotFound
	}
	return mem.Role, nil
}

func (m *Mem) ProjectAccess(_ context.Context, projectID, userID uuid.UUID) (store.ProjectAccess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[projectID]
	if !ok || p.DeletedAt != nil {
		return store.ProjectAccess{}, store.ErrNotFound
	}
	return store.ProjectAccess{
		OrgID:      p.OrgID,
		MemberRole: m.members[memberKey(projectID, userID)].Role,
		OrgRole:    m.orgRoleLocked(p.OrgID, userID),
	}, nil
}

func (m *Mem) RenameProject(_ context.Context, id uuid.UUID, name string) (store.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok || p.DeletedAt != nil {
		return store.Project{}, store.ErrNotFound
	}
	p.Name = name
	m.projects[id] = p
	return p, nil
}

func (m *Mem) DeleteProject(_ context.Context, id uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok || p.DeletedAt != nil {
		return store.ErrNotFound
	}
	m.deleteProjectLocked(id, at)
	return nil
}

// deleteProjectLocked is api-v1 v1.24 item 7: no host is revoked. The
// project's access rows go, join tokens granting it are revoked, and its
// services are marked gone (a gone row keeps its time).
func (m *Mem) deleteProjectLocked(id uuid.UUID, at time.Time) {
	p := m.projects[id]
	p.DeletedAt = &at
	m.projects[id] = p
	for key, a := range m.access {
		if a.ProjectID == id {
			delete(m.access, key)
		}
	}
	for tid, tok := range m.tokens {
		if tok.Grant != nil && tok.Grant.ProjectID == id && tok.RevokedAt == nil && tok.UsedAt == nil {
			t := at
			tok.RevokedAt = &t
			m.tokens[tid] = tok
		}
	}
	for sid, svc := range m.services {
		if svc.ProjectID != nil && *svc.ProjectID == id && svc.GoneAt == nil {
			t := at
			svc.GoneAt = &t
			m.services[sid] = svc
		}
	}
	m.deleteInvitesLocked(id)
	m.dropProjectEdgeLocked(id)
	m.deleteCatalogLocked(id)
	m.deleteSecretsLocked(id)
}

func (m *Mem) revokeCredsLocked(hostID uuid.UUID, at time.Time) {
	for id, c := range m.creds {
		if c.HostID == hostID && c.RevokedAt == nil {
			t := at
			c.RevokedAt = &t
			m.creds[id] = c
		}
	}
}

// markHostServicesGoneLocked sets gone_at to the host's revoke time on rows
// that are still present. An already-gone row keeps its time. The caller holds m.mu.
func (m *Mem) markHostServicesGoneLocked(hostID uuid.UUID) {
	h, ok := m.hosts[hostID]
	if !ok || h.RevokedAt == nil {
		return
	}
	for id, s := range m.services {
		if s.HostID != hostID || s.GoneAt != nil {
			continue
		}
		gone := *h.RevokedAt
		s.GoneAt = &gone
		m.services[id] = s
	}
}

func (m *Mem) ListMembers(_ context.Context, projectID uuid.UUID, limit int, cursor *store.Cursor) ([]store.Member, *store.Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.projects[projectID]; !ok || p.DeletedAt != nil {
		return nil, nil, store.ErrNotFound
	}
	var items []store.Member
	for _, mem := range m.members {
		if mem.ProjectID != projectID {
			continue
		}
		u := m.users[mem.UserID]
		items = append(items, store.Member{
			UserID:    mem.UserID,
			Email:     u.Email,
			Name:      u.Name,
			Role:      mem.Role,
			CreatedAt: mem.CreatedAt,
		})
	}
	out, next := pageBy(items, limit, cursor, func(it store.Member) time.Time { return it.CreatedAt }, func(it store.Member) string { return it.UserID.String() }, false)
	return out, next, nil
}

func (m *Mem) AddMember(_ context.Context, projectID, userID uuid.UUID, role string, addedBy uuid.UUID, at time.Time) (store.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.projects[projectID]; !ok || p.DeletedAt != nil {
		return store.Member{}, store.ErrNotFound
	}
	u, ok := m.users[userID]
	if !ok {
		return store.Member{}, store.ErrNotFound
	}
	key := memberKey(projectID, userID)
	if _, exists := m.members[key]; exists {
		return store.Member{}, store.ErrConflict
	}
	m.members[key] = memberRec{ProjectID: projectID, UserID: userID, Role: role, AddedBy: addedBy, CreatedAt: at}
	return store.Member{UserID: userID, Email: u.Email, Name: u.Name, Role: role, CreatedAt: at}, nil
}

func (m *Mem) UpdateMemberRole(_ context.Context, projectID, userID uuid.UUID, role string) (store.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := memberKey(projectID, userID)
	mem, ok := m.members[key]
	if !ok {
		return store.Member{}, store.ErrNotFound
	}
	if mem.Role == storeRoleOwner {
		return store.Member{}, store.ErrOwner
	}
	mem.Role = role
	m.members[key] = mem
	u := m.users[userID]
	return store.Member{UserID: userID, Email: u.Email, Name: u.Name, Role: role, CreatedAt: mem.CreatedAt}, nil
}

func (m *Mem) RemoveMember(_ context.Context, projectID, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := memberKey(projectID, userID)
	mem, ok := m.members[key]
	if !ok {
		return store.ErrNotFound
	}
	if mem.Role == storeRoleOwner {
		return store.ErrOwner
	}
	delete(m.members, key)
	return nil
}

func (m *Mem) TransferOwnership(_ context.Context, projectID, newOwner uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	targetKey := memberKey(projectID, newOwner)
	target, ok := m.members[targetKey]
	if !ok {
		return store.ErrNotFound
	}
	var ownerKey string
	for k, mem := range m.members {
		if mem.ProjectID == projectID && mem.Role == storeRoleOwner {
			ownerKey = k
			break
		}
	}
	if ownerKey == "" {
		return store.ErrState
	}
	owner := m.members[ownerKey]
	owner.Role = "admin"
	m.members[ownerKey] = owner
	target = m.members[targetKey]
	target.Role = storeRoleOwner
	m.members[targetKey] = target
	return nil
}

func pageProjects(items []store.Project, limit int, cursor *store.Cursor) ([]store.Project, *store.Cursor) {
	return pageBy(items, limit, cursor, func(p store.Project) time.Time { return p.CreatedAt }, func(p store.Project) string { return p.ID.String() }, false)
}

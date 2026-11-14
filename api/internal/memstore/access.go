package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

func accessKey(hostID, projectID uuid.UUID) string {
	return hostID.String() + "/" + projectID.String()
}

func (m *Mem) HostFacts(_ context.Context, userID, hostID uuid.UUID) (store.HostFacts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hosts[hostID]
	if !ok {
		return store.HostFacts{}, store.ErrNotFound
	}
	facts := store.HostFacts{OrgID: h.OrgID, OrgRole: m.orgRoleLocked(h.OrgID, userID)}
	for _, a := range m.access {
		if a.HostID != hostID {
			continue
		}
		p, ok := m.projects[a.ProjectID]
		if !ok || p.DeletedAt != nil {
			continue
		}
		mem, ok := m.members[memberKey(a.ProjectID, userID)]
		if !ok {
			continue
		}
		facts.Access = append(facts.Access, store.MemberAccess{ProjectID: a.ProjectID, Level: a.Level, Role: mem.Role})
	}
	sort.Slice(facts.Access, func(i, j int) bool {
		return facts.Access[i].ProjectID.String() < facts.Access[j].ProjectID.String()
	})
	return facts, nil
}

// hostAccessLocked is the host's access rows with project names, sorted by
// project name, then id.
func (m *Mem) hostAccessLocked(hostID uuid.UUID) []store.HostAccess {
	out := []store.HostAccess{}
	for _, a := range m.access {
		if a.HostID != hostID {
			continue
		}
		if p, ok := m.projects[a.ProjectID]; ok {
			a.ProjectName = p.Name
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectName != out[j].ProjectName {
			return out[i].ProjectName < out[j].ProjectName
		}
		return out[i].ProjectID.String() < out[j].ProjectID.String()
	})
	return out
}

func (m *Mem) ListHostAccess(_ context.Context, hostID uuid.UUID) ([]store.HostAccess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[hostID]; !ok {
		return nil, store.ErrNotFound
	}
	return m.hostAccessLocked(hostID), nil
}

func (m *Mem) PutHostAccess(_ context.Context, hostID, projectID uuid.UUID, level string, grantedBy uuid.UUID, at time.Time) (store.HostAccess, bool, error) {
	if !store.ValidAccessLevel(level) {
		return store.HostAccess{}, false, store.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hosts[hostID]
	if !ok {
		return store.HostAccess{}, false, store.ErrNotFound
	}
	p, ok := m.projects[projectID]
	if !ok || p.DeletedAt != nil || p.OrgID != h.OrgID {
		return store.HostAccess{}, false, store.ErrNotFound
	}
	key := accessKey(hostID, projectID)
	prev, existed := m.access[key]
	by := grantedBy
	item := store.HostAccess{HostID: hostID, ProjectID: projectID, Level: level, GrantedBy: &by, GrantedAt: at}
	if existed {
		item.GrantedBy = prev.GrantedBy
		item.GrantedAt = prev.GrantedAt
	}
	m.access[key] = item
	item.ProjectName = p.Name
	return item, !existed, nil
}

func (m *Mem) DeleteHostAccess(_ context.Context, hostID, projectID uuid.UUID, reassignOrg bool) (store.HostAccess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[hostID]; !ok {
		return store.HostAccess{}, store.ErrNotFound
	}
	key := accessKey(hostID, projectID)
	item, ok := m.access[key]
	if !ok {
		return store.HostAccess{}, store.ErrNotFound
	}
	if p, ok := m.projects[projectID]; ok {
		item.ProjectName = p.Name
	}
	var ids []uuid.UUID
	for id, svc := range m.services {
		if svc.HostID == hostID && svc.ProjectID != nil && *svc.ProjectID == projectID && svc.GoneAt == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 && !reassignOrg {
		return store.HostAccess{}, store.ErrConflict
	}
	for _, id := range ids {
		svc := m.services[id]
		svc.ProjectID = nil
		svc.CatalogEntry = nil
		svc.Source = "discovered"
		m.services[id] = svc
	}
	delete(m.access, key)
	return item, nil
}

// accessLevelLocked is the project's level on the host, or "".
func (m *Mem) accessLevelLocked(hostID, projectID uuid.UUID) string {
	return m.access[accessKey(hostID, projectID)].Level
}

func (m *Mem) HostProjectAccess(_ context.Context, projectID, hostID uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[hostID]; !ok {
		return "", store.ErrNotFound
	}
	if p, ok := m.projects[projectID]; !ok || p.DeletedAt != nil {
		return "", nil
	}
	return m.accessLevelLocked(hostID, projectID), nil
}

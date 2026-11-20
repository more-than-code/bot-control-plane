package memstore

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

func (m *Mem) ListServices(_ context.Context, hostID uuid.UUID, projects []uuid.UUID, now time.Time) ([]store.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var items []store.Service
	for _, s := range m.services {
		if s.HostID != hostID {
			continue
		}
		if !projectVisible(s.ProjectID, projects) {
			continue
		}
		s.Health = store.DeriveHealth(m.latestHealthLocked(s.ID, now), now)
		s.Ports = copyInts(s.Ports)
		items = append(items, s)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if len(items) > 500 {
		items = items[:500]
	}
	if items == nil {
		items = []store.Service{}
	}
	return items, nil
}

func (m *Mem) GetService(_ context.Context, id uuid.UUID, now time.Time) (store.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[id]
	if !ok {
		return store.Service{}, store.ErrNotFound
	}
	s.Health = store.DeriveHealth(m.latestHealthLocked(s.ID, now), now)
	s.Ports = copyInts(s.Ports)
	return s, nil
}

func (m *Mem) UpsertDiscovered(_ context.Context, hostID uuid.UUID, items []store.Discovered, at time.Time, complete bool, presence bool) error {
	if !complete {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[hostID]; !ok {
		return store.ErrNotFound
	}
	reported := make(map[string]struct{}, len(items))
	lookup := func(slug, entry, kind string) (uuid.UUID, bool) {
		return m.lookupOwnerLocked(hostID, slug, entry, kind)
	}
	for _, item := range items {
		name, source, projectID, catalogEntry := store.PlaceDiscovered(item, lookup)
		reported[name] = struct{}{}
		var found uuid.UUID
		var existing store.Service
		ok := false
		for id, s := range m.services {
			if s.HostID == hostID && s.Name == name {
				found = id
				existing = s
				ok = true
				break
			}
		}
		if !ok {
			id := uuid.New()
			m.services[id] = store.Service{
				ID:            id,
				HostID:        hostID,
				ProjectID:     cloneID(projectID),
				Name:          name,
				Source:        source,
				Kind:          item.Kind,
				Ports:         copyInts(item.Ports),
				SystemdUnit:   cloneString(item.SystemdUnit),
				SystemdScope:  cloneString(item.SystemdScope),
				ContainerName: cloneString(item.ContainerName),
				ImageDigest:   cloneString(item.ImageDigest),
				ProcessName:   cloneString(item.ProcessName),
				OwnerUser:     store.NormalizeOwnerUser(item.OwnerUser),
				CatalogEntry:  cloneString(catalogEntry),
				FirstSeenAt:   at,
				LastSeenAt:    at,
			}
			continue
		}
		// Rows are keyed by (host, name), and an owned row is named after its
		// entry, so two projects' entries (or an org-level row) can share a
		// name. An owned report never moves a row away from a different
		// project or from the org: the row is left as it is and the conflict
		// is audited.
		if source == "owned" && existing.Source != "declared" && !sameProject(existing.ProjectID, projectID) {
			m.ownerConflictLocked(hostID, existing, item, at)
			continue
		}
		existing.LastSeenAt = at
		existing.GoneAt = nil
		existing.OwnerUser = store.NormalizeOwnerUser(item.OwnerUser)
		// An owned report adopts a discovered row of its own project. A discovered placement
		// ends ownership. declared is left alone. id, display_name, and
		// health checks are not written here.
		if existing.Source != "declared" && source == "owned" {
			existing.Source = "owned"
			existing.ProjectID = cloneID(projectID)
			existing.CatalogEntry = cloneString(catalogEntry)
		} else if existing.Source == "owned" && source == "discovered" {
			// Ownership ends, but the row keeps its project, so the same
			// project can own it again (review A2).
			existing.Source = "discovered"
			existing.CatalogEntry = nil
		}
		if existing.Source != "declared" {
			existing.Kind = item.Kind
			existing.Ports = copyInts(item.Ports)
			existing.SystemdUnit = cloneString(item.SystemdUnit)
			existing.SystemdScope = cloneString(item.SystemdScope)
			existing.ContainerName = cloneString(item.ContainerName)
			existing.ImageDigest = cloneString(item.ImageDigest)
			existing.ProcessName = cloneString(item.ProcessName)
		}
		m.services[found] = existing
	}
	if presence {
		for id, s := range m.services {
			if s.HostID != hostID || s.GoneAt != nil {
				continue
			}
			if _, ok := reported[s.Name]; ok {
				continue
			}
			gone := at
			s.GoneAt = &gone
			m.services[id] = s
		}
	}
	return nil
}

func (m *Mem) ReplaceHealthChecks(_ context.Context, serviceID uuid.UUID, checks []store.HealthCheckInput) ([]store.HealthCheck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.replaceHealthChecksLocked(serviceID, checks)
}

func (m *Mem) replaceHealthChecksLocked(serviceID uuid.UUID, checks []store.HealthCheckInput) ([]store.HealthCheck, error) {
	if _, ok := m.services[serviceID]; !ok {
		return nil, store.ErrNotFound
	}
	for id, c := range m.checks {
		if c.ServiceID == serviceID {
			delete(m.checks, id)
		}
	}
	out := make([]store.HealthCheck, 0, len(checks))
	for _, in := range checks {
		c := store.HealthCheck{
			ID:              uuid.New(),
			ServiceID:       serviceID,
			Protocol:        in.Protocol,
			Port:            in.Port,
			Path:            in.Path,
			IntervalSec:     in.IntervalSec,
			ExpectStatusMin: in.ExpectStatusMin,
			ExpectStatusMax: in.ExpectStatusMax,
			Enabled:         in.Enabled,
		}
		m.checks[c.ID] = c
		out = append(out, c)
	}
	return out, nil
}

func (m *Mem) ListHealthChecks(_ context.Context, serviceID uuid.UUID) ([]store.HealthCheck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listHealthChecksLocked(serviceID), nil
}

func (m *Mem) listHealthChecksLocked(serviceID uuid.UUID) []store.HealthCheck {
	var out []store.HealthCheck
	for _, c := range m.checks {
		if c.ServiceID == serviceID {
			out = append(out, c)
		}
	}
	if out == nil {
		out = []store.HealthCheck{}
	}
	// Ordered by id, as the DB does.
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out
}

func (m *Mem) ListHealthChecksForServices(_ context.Context, serviceIDs []string) (map[string][]store.HealthCheck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]store.HealthCheck{}
	for _, raw := range serviceIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			continue
		}
		checks := m.listHealthChecksLocked(id)
		if len(checks) == 0 {
			continue
		}
		out[id.String()] = checks
	}
	return out, nil
}

func (m *Mem) HealthChecksForHost(_ context.Context, hostID uuid.UUID) ([]store.AgentCheck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.AgentCheck
	for _, c := range m.checks {
		if !c.Enabled {
			continue
		}
		svc, ok := m.services[c.ServiceID]
		if !ok || svc.HostID != hostID {
			continue
		}
		out = append(out, store.AgentCheck{
			ID:              c.ID,
			Service:         svc.Name,
			Protocol:        c.Protocol,
			Port:            c.Port,
			Path:            c.Path,
			IntervalSec:     c.IntervalSec,
			ExpectStatusMin: c.ExpectStatusMin,
			ExpectStatusMax: c.ExpectStatusMax,
		})
	}
	if out == nil {
		out = []store.AgentCheck{}
	}
	return out, nil
}

func (m *Mem) SetServiceDisplayName(_ context.Context, id uuid.UUID, name *string, now time.Time) (store.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[id]
	if !ok {
		return store.Service{}, store.ErrNotFound
	}
	if name != nil {
		want := strings.ToLower(*name)
		for otherID, other := range m.services {
			if otherID == id || other.HostID != s.HostID || other.DisplayName == nil {
				continue
			}
			if strings.ToLower(*other.DisplayName) == want {
				return store.Service{}, store.ErrConflict
			}
		}
		copied := *name
		s.DisplayName = &copied
	} else {
		s.DisplayName = nil
	}
	m.services[id] = s
	return m.serviceCopyLocked(s, now), nil
}

func (m *Mem) ListProjectServices(_ context.Context, projectID uuid.UUID, q store.ServiceListQuery, now time.Time) (store.ServicePage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if q.HostID != nil {
		_, ok := m.hosts[*q.HostID]
		level := ""
		if ok {
			level = m.accessLevelLocked(*q.HostID, projectID)
		}
		if !ok || !authz.ProjectHostReached(level) {
			return store.ServicePage{}, store.ErrInvalid
		}
	}
	var rows []store.ProjectService
	for _, svc := range m.services {
		host, ok := m.hosts[svc.HostID]
		if !ok {
			continue
		}
		level := m.accessLevelLocked(host.ID, projectID)
		if !authz.ProjectServiceVisible(level, svc.ProjectID, projectID) {
			continue
		}
		if q.HostID != nil && svc.HostID != *q.HostID {
			continue
		}
		if !m.serviceInViewLocked(svc, host, q.View) {
			continue
		}
		projectName := ""
		if p, ok := m.projects[projectID]; ok {
			projectName = p.Name
		}
		row := m.projectRowLocked(svc, host, projectName, now)
		if !serviceMatches(row, q.Q) {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return store.ServiceBefore(serviceOrder(rows[i]), serviceOrder(rows[j]), q.Sort, q.Dir)
	})
	total := len(rows)
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	rows = rows[offset:]
	limit := q.Limit
	if limit < 1 {
		limit = 50
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []store.ProjectService{}
	}
	return store.ServicePage{Items: rows, Total: total}, nil
}

func (m *Mem) projectRowLocked(svc store.Service, host store.Host, projectName string, now time.Time) store.ProjectService {
	copied := m.serviceCopyLocked(svc, now)
	var revoked *time.Time
	if host.RevokedAt != nil {
		t := *host.RevokedAt
		revoked = &t
	}
	return store.ProjectService{
		Service:       copied,
		HostName:      host.Name,
		ProjectName:   projectName,
		HostRevokedAt: revoked,
		HostAlwaysOn:  host.AlwaysOn,
	}
}

func (m *Mem) serviceCopyLocked(svc store.Service, now time.Time) store.Service {
	svc.Health = store.DeriveHealth(m.latestHealthLocked(svc.ID, now), now)
	svc.Ports = copyInts(svc.Ports)
	svc.DisplayName = cloneString(svc.DisplayName)
	svc.OwnerUser = cloneString(svc.OwnerUser)
	svc.SystemdUnit = cloneString(svc.SystemdUnit)
	svc.SystemdScope = cloneString(svc.SystemdScope)
	svc.ContainerName = cloneString(svc.ContainerName)
	svc.ImageDigest = cloneString(svc.ImageDigest)
	svc.ProcessName = cloneString(svc.ProcessName)
	svc.CatalogEntry = cloneString(svc.CatalogEntry)
	svc.GoneAt = cloneTime(svc.GoneAt)
	return svc
}

func serviceOrder(row store.ProjectService) store.ServiceOrder {
	name := row.Name
	if row.DisplayName != nil {
		name = *row.DisplayName
	}
	return store.ServiceOrder{
		ID:         row.ID,
		NameKey:    strings.ToLower(name),
		Host:       row.HostName,
		Kind:       row.Kind,
		StatusRank: store.HealthRank(row.Health.Status),
		Gone:       row.GoneAt != nil,
		LastSeen:   row.LastSeenAt,
	}
}

func (m *Mem) serviceInViewLocked(svc store.Service, host store.Host, view string) bool {
	// Rows of archived hosts are left out in every view (api-v1 v1.40).
	if host.ArchivedAt != nil {
		return false
	}
	switch view {
	case "deploybot":
		return host.RevokedAt == nil && m.deploybotRowLocked(svc)
	case "deploybot_and_discovered":
		if host.RevokedAt != nil {
			return false
		}
		if m.deploybotRowLocked(svc) {
			return true
		}
		return svc.Source == "discovered" && svc.GoneAt == nil
	default:
		return true
	}
}

// deploybotRowLocked mirrors db.deploybotRowsSQL: a gone owned row whose
// catalog entry was deleted is no longer a deploybot service.
func (m *Mem) deploybotRowLocked(svc store.Service) bool {
	switch svc.Source {
	case "declared":
		return true
	case "owned":
		if svc.GoneAt == nil || svc.CatalogEntry == nil || svc.ProjectID == nil {
			return true
		}
		_, ok := m.entryByName[entryKey(*svc.ProjectID, *svc.CatalogEntry)]
		return ok
	default:
		return false
	}
}

func serviceMatches(row store.ProjectService, q string) bool {
	if q == "" {
		return true
	}
	needle := strings.ToLower(q)
	fields := []string{row.Name, row.Kind, row.HostName}
	if row.DisplayName != nil {
		fields = append(fields, *row.DisplayName)
	}
	if row.OwnerUser != nil {
		fields = append(fields, *row.OwnerUser)
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	for _, port := range row.Ports {
		if strings.Contains(strconv.Itoa(port), needle) {
			return true
		}
	}
	return false
}

func sameProject(a, b *uuid.UUID) bool {
	return a != nil && b != nil && *a == *b
}

// ownerConflictLocked records a refused owned report (audit
// service.owner_conflict, via agent). The caller holds m.mu.
func (m *Mem) ownerConflictLocked(hostID uuid.UUID, existing store.Service, item store.Discovered, at time.Time) {
	// Audit only a new conflict: none for the same service, project and
	// entry within audit.OwnerConflictWindow (as Postgres).
	for _, a := range m.audits {
		if a.Action != audit.ActionServiceOwnerConflict || a.ServiceID == nil || *a.ServiceID != existing.ID || !a.At.After(at.Add(-audit.OwnerConflictWindow)) {
			continue
		}
		var d struct{ Project, Entry string }
		if json.Unmarshal(a.Detail, &d) == nil && d.Project == item.Owner.Project && d.Entry == item.Owner.Entry {
			return
		}
	}
	org := m.hosts[hostID].OrgID
	host := hostID
	svc := existing.ID
	m.insertAuditLocked(store.AuditRow{
		At: at, Via: "agent", OrgID: &org, HostID: &host, ServiceID: &svc,
		Action: audit.ActionServiceOwnerConflict, Decision: "denied",
		Detail: map[string]any{"name": existing.Name, "project": item.Owner.Project, "entry": item.Owner.Entry},
	})
}

func cloneID(in *uuid.UUID) *uuid.UUID {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func cloneString(in *string) *string {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func cloneTime(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func copyInts(in []int) []int {
	if len(in) == 0 {
		return []int{}
	}
	return append([]int(nil), in...)
}

// firstDeployProjectLocked is the host's deploy grant project from its join
// token, or else the project granted deploy access to the host first (by
// granted_at, then project id), or nil. The seed helpers below place a row
// there, as a host's former owner used to.
func (m *Mem) firstDeployProjectLocked(hostID uuid.UUID) *uuid.UUID {
	if pid, ok := m.enrolGrant[hostID]; ok {
		return &pid
	}
	var best *store.HostAccess
	for _, a := range m.access {
		if a.HostID != hostID || a.Level != "deploy" {
			continue
		}
		if best == nil || a.GrantedAt.Before(best.GrantedAt) || (a.GrantedAt.Equal(best.GrantedAt) && a.ProjectID.String() < best.ProjectID.String()) {
			cp := a
			best = &cp
		}
	}
	if best == nil {
		return nil
	}
	id := best.ProjectID
	return &id
}

// PutDeclared seeds a declared service. Tests use it to prove discovery does
// not downgrade manually set fields. It is not part of store.Store.
func (m *Mem) PutDeclared(hostID uuid.UUID, name, kind string, ports []int, at time.Time) store.Service {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := store.Service{
		ID:          uuid.New(),
		HostID:      hostID,
		ProjectID:   m.firstDeployProjectLocked(hostID),
		Name:        name,
		Source:      "declared",
		Kind:        kind,
		Ports:       copyInts(ports),
		FirstSeenAt: at,
		LastSeenAt:  at,
	}
	m.services[s.ID] = s
	return s
}

// PutDiscovered seeds one discovered row and does not change presence on
// other rows. Tests use it to build a list without a full services report.
// It is not part of store.Store.
func (m *Mem) PutDiscovered(hostID uuid.UUID, name, kind string, ports []int, owner *string, at time.Time) store.Service {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := store.Service{
		ID:          uuid.New(),
		HostID:      hostID,
		ProjectID:   m.firstDeployProjectLocked(hostID),
		Name:        name,
		Source:      "discovered",
		Kind:        kind,
		Ports:       copyInts(ports),
		OwnerUser:   store.NormalizeOwnerUser(owner),
		FirstSeenAt: at,
		LastSeenAt:  at,
	}
	m.services[s.ID] = s
	return s
}

// SetServiceProject assigns a service to a project; uuid.Nil makes it
// org-level. Tests use it to place a service. It is not part of store.Store.
func (m *Mem) SetServiceProject(id, projectID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[id]
	if !ok {
		return
	}
	s.ProjectID = nil
	if projectID != uuid.Nil {
		s.ProjectID = &projectID
	}
	m.services[id] = s
}

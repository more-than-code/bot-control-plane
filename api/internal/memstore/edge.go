package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Domains, routes and host edges (api-v1 v1.35). Every method holds m.mu.

type edgeData struct {
	domains       map[uuid.UUID]store.OrgDomain // Assignments kept here too
	routes        map[uuid.UUID]store.Route
	hostEdges     map[uuid.UUID]store.HostEdge
	confirmations map[string]store.EdgeConfirmation
	acmeEmail     map[uuid.UUID]string
}

func (m *Mem) edgeLocked() *edgeData {
	if m.edge == nil {
		m.edge = &edgeData{
			domains:       map[uuid.UUID]store.OrgDomain{},
			routes:        map[uuid.UUID]store.Route{},
			hostEdges:     map[uuid.UUID]store.HostEdge{},
			confirmations: map[string]store.EdgeConfirmation{},
			acmeEmail:     map[uuid.UUID]string{},
		}
	}
	return m.edge
}

func cloneDomain(d store.OrgDomain) store.OrgDomain {
	d.Assignments = append([]store.DomainAssignment{}, d.Assignments...)
	d.VerifiedAt = cloneTime(d.VerifiedAt)
	d.LastCheckAt = cloneTime(d.LastCheckAt)
	return d
}

func cloneRoute(r store.Route) store.Route {
	r.NoStorePaths = append([]string{}, r.NoStorePaths...)
	if r.ClientIPFrom != nil {
		v := *r.ClientIPFrom
		r.ClientIPFrom = &v
	}
	return r
}

// domainViewLocked fills project names and drops assignments of deleted
// projects, sorted by project name then pattern.
func (m *Mem) domainViewLocked(d store.OrgDomain) store.OrgDomain {
	d = cloneDomain(d)
	out := []store.DomainAssignment{}
	for _, a := range d.Assignments {
		p, ok := m.projects[a.ProjectID]
		if !ok || p.DeletedAt != nil {
			continue
		}
		a.ProjectName = p.Name
		out = append(out, a)
	}
	sortAssignments(out)
	d.Assignments = out
	return d
}

func sortAssignments(in []store.DomainAssignment) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].ProjectName != in[j].ProjectName {
			return in[i].ProjectName < in[j].ProjectName
		}
		if in[i].ProjectID != in[j].ProjectID {
			return in[i].ProjectID.String() < in[j].ProjectID.String()
		}
		return in[i].Pattern < in[j].Pattern
	})
}

func (m *Mem) ListOrgDomains(_ context.Context, orgID uuid.UUID) ([]store.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.OrgDomain{}
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return out, nil
	}
	for _, d := range m.edgeLocked().domains {
		if d.OrgID == orgID {
			out = append(out, m.domainViewLocked(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mem) GetOrgDomain(_ context.Context, orgID, domainID uuid.UUID) (store.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.liveDomainLocked(orgID, domainID)
	if !ok {
		return store.OrgDomain{}, store.ErrNotFound
	}
	return m.domainViewLocked(d), nil
}

func (m *Mem) liveDomainLocked(orgID, domainID uuid.UUID) (store.OrgDomain, bool) {
	d, ok := m.edgeLocked().domains[domainID]
	if !ok || d.OrgID != orgID {
		return store.OrgDomain{}, false
	}
	if _, live := m.liveOrgLocked(orgID); !live {
		return store.OrgDomain{}, false
	}
	return d, true
}

// nameHeldLocked reports whether another org holds name verified or lapsed.
func (m *Mem) nameHeldLocked(name string, except uuid.UUID) bool {
	for _, d := range m.edgeLocked().domains {
		if d.Name == name && d.ID != except && (d.Status == store.DomainVerified || d.Status == store.DomainLapsed) {
			return true
		}
	}
	return false
}

func (m *Mem) CreateOrgDomain(_ context.Context, orgID uuid.UUID, name, token string, actor uuid.UUID, at time.Time) (store.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return store.OrgDomain{}, store.ErrNotFound
	}
	for _, d := range m.edgeLocked().domains {
		if d.OrgID == orgID && d.Name == name {
			return store.OrgDomain{}, store.ErrConflict
		}
	}
	if m.nameHeldLocked(name, uuid.Nil) {
		return store.OrgDomain{}, store.ErrDomainTaken
	}
	d := store.OrgDomain{
		ID: uuid.New(), OrgID: orgID, Name: name, Token: token, Status: store.DomainPending,
		CreatedBy: actor, CreatedAt: at, Assignments: []store.DomainAssignment{},
	}
	m.edgeLocked().domains[d.ID] = d
	return cloneDomain(d), nil
}

func (m *Mem) MarkDomainVerified(_ context.Context, orgID, domainID uuid.UUID, at time.Time) (store.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.liveDomainLocked(orgID, domainID)
	if !ok {
		return store.OrgDomain{}, store.ErrNotFound
	}
	if m.nameHeldLocked(d.Name, d.ID) {
		return store.OrgDomain{}, store.ErrDomainTaken
	}
	if d.Status != store.DomainVerified {
		t := at
		d.VerifiedAt = &t
	}
	d.Status = store.DomainVerified
	d.Failures = 0
	check := at
	d.LastCheckAt = &check
	m.edgeLocked().domains[d.ID] = d
	return m.domainViewLocked(d), nil
}

func (m *Mem) TouchDomainCheck(_ context.Context, orgID, domainID uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.liveDomainLocked(orgID, domainID)
	if !ok {
		return store.ErrNotFound
	}
	if d.Status == store.DomainVerified {
		return nil
	}
	check := at
	d.LastCheckAt = &check
	m.edgeLocked().domains[d.ID] = d
	return nil
}

func (m *Mem) RecordDomainCheck(_ context.Context, domainID uuid.UUID, ok bool, at time.Time) (store.OrgDomain, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, found := m.edgeLocked().domains[domainID]
	if !found {
		return store.OrgDomain{}, false, store.ErrNotFound
	}
	if d.Status == store.DomainPending {
		return store.OrgDomain{}, false, store.ErrState
	}
	lapsed := false
	if ok {
		if d.Status != store.DomainVerified {
			t := at
			d.VerifiedAt = &t
		}
		d.Status = store.DomainVerified
		d.Failures = 0
	} else {
		d.Failures++
		if d.Status == store.DomainVerified && d.Failures >= store.DomainLapseAfter {
			d.Status = store.DomainLapsed
			lapsed = true
		}
	}
	check := at
	d.LastCheckAt = &check
	m.edgeLocked().domains[d.ID] = d
	return m.domainViewLocked(d), lapsed, nil
}

func (m *Mem) ListDomainsDue(_ context.Context, before time.Time, limit int) ([]store.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.OrgDomain{}
	for _, d := range m.edgeLocked().domains {
		if d.Status == store.DomainPending {
			continue
		}
		if _, ok := m.liveOrgLocked(d.OrgID); !ok {
			continue
		}
		if d.LastCheckAt != nil && !d.LastCheckAt.Before(before) {
			continue
		}
		out = append(out, m.domainViewLocked(d))
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].LastCheckAt, out[j].LastCheckAt
		switch {
		case a == nil && b != nil:
			return true
		case a != nil && b == nil:
			return false
		case a != nil && b != nil && !a.Equal(*b):
			return a.Before(*b)
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Mem) PutDomainAssignments(_ context.Context, orgID, domainID uuid.UUID, in []store.DomainAssignment) (store.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.liveDomainLocked(orgID, domainID)
	if !ok {
		return store.OrgDomain{}, store.ErrNotFound
	}
	seen := map[string]bool{}
	next := make([]store.DomainAssignment, 0, len(in))
	for _, a := range in {
		p, ok := m.projects[a.ProjectID]
		if !ok || p.DeletedAt != nil || p.OrgID != orgID || (a.Pattern != "@" && a.Pattern != "*") {
			return store.OrgDomain{}, store.ErrInvalid
		}
		key := a.ProjectID.String() + a.Pattern
		if seen[key] {
			return store.OrgDomain{}, store.ErrInvalid
		}
		seen[key] = true
		next = append(next, store.DomainAssignment{ProjectID: a.ProjectID, Pattern: a.Pattern})
	}
	d.Assignments = next
	m.edgeLocked().domains[d.ID] = d
	return m.domainViewLocked(d), nil
}

func (m *Mem) ProjectDomains(_ context.Context, projectID uuid.UUID) ([]store.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.OrgDomain{}
	p, ok := m.projects[projectID]
	if !ok || p.DeletedAt != nil {
		return out, nil
	}
	if _, live := m.liveOrgLocked(p.OrgID); !live {
		return out, nil
	}
	for _, d := range m.edgeLocked().domains {
		if d.OrgID != p.OrgID {
			continue
		}
		view := m.domainViewLocked(d)
		mine := []store.DomainAssignment{}
		for _, a := range view.Assignments {
			if a.ProjectID == projectID {
				mine = append(mine, a)
			}
		}
		if len(mine) == 0 {
			continue
		}
		view.Assignments = mine
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// planLocked checks a plan for a confirm: owned by actor, unused,
// unexpired and of kind. Anything else is ErrNotFound.
func (m *Mem) planLocked(confirm []byte, kind string, actor uuid.UUID, at time.Time) (store.EdgeConfirmation, error) {
	c, ok := m.edgeLocked().confirmations[hashKey(confirm)]
	if !ok || c.Kind != kind || c.UserID != actor || c.UsedAt != nil || !c.ExpiresAt.After(at) {
		return store.EdgeConfirmation{}, store.ErrNotFound
	}
	return c, nil
}

func (m *Mem) markPlanLocked(confirm []byte, at time.Time) {
	c := m.edgeLocked().confirmations[hashKey(confirm)]
	t := at
	c.UsedAt = &t
	m.edgeLocked().confirmations[hashKey(confirm)] = c
}

func (m *Mem) DeleteOrgDomain(_ context.Context, orgID, domainID uuid.UUID, want []string, confirm []byte, actor uuid.UUID, at time.Time) ([]store.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.planLocked(confirm, store.EdgePlanDomainDelete, actor, at)
	if err != nil {
		return nil, err
	}
	if c.OrgID == nil || *c.OrgID != orgID || c.DomainID == nil || *c.DomainID != domainID {
		return nil, store.ErrNotFound
	}
	d, ok := m.liveDomainLocked(orgID, domainID)
	if !ok {
		return nil, store.ErrNotFound
	}
	removed := m.routesUnderLocked(d)
	if !sameKeys(store.RouteKeys(removed), want) {
		return nil, store.ErrRoutesChanged
	}
	for _, r := range removed {
		delete(m.edgeLocked().routes, r.ID)
	}
	delete(m.edgeLocked().domains, d.ID)
	m.markPlanLocked(confirm, at)
	return removed, nil
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// routesUnderLocked is RoutesUnderDomain for d.
func (m *Mem) routesUnderLocked(d store.OrgDomain) []store.Route {
	out := []store.Route{}
	for id, r := range m.edgeLocked().routes {
		if !store.UnderDomain(d.Name, r.Hostname) || !m.projectAliveLocked(r.ProjectID) {
			continue
		}
		if m.projects[r.ProjectID].OrgID != d.OrgID || m.coveredElsewhereLocked(d, r) {
			continue
		}
		r.ID = id
		out = append(out, m.routeViewLocked(r))
	}
	sortRoutesByHost(out)
	return out
}

// coveredElsewhereLocked reports whether another verified or lapsed domain
// of the org assigned to the route's project still covers its hostname.
func (m *Mem) coveredElsewhereLocked(d store.OrgDomain, r store.Route) bool {
	for _, other := range m.edgeLocked().domains {
		if other.ID == d.ID || other.OrgID != d.OrgID || (other.Status != store.DomainVerified && other.Status != store.DomainLapsed) {
			continue
		}
		for _, a := range other.Assignments {
			if a.ProjectID == r.ProjectID && store.Covers(other.Name, a.Pattern, r.Hostname) {
				return true
			}
		}
	}
	return false
}

func (m *Mem) RoutesUnderDomain(_ context.Context, orgID, domainID uuid.UUID) ([]store.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.liveDomainLocked(orgID, domainID)
	if !ok {
		return nil, store.ErrNotFound
	}
	return m.routesUnderLocked(d), nil
}

// routeViewLocked fills the entry name from the entry row.
func (m *Mem) routeViewLocked(r store.Route) store.Route {
	r = cloneRoute(r)
	if e, ok := m.entries[r.EntryID]; ok {
		r.Entry = e.Name
	}
	return r
}

func sortRoutesByHost(out []store.Route) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].PathPrefix < out[j].PathPrefix
	})
}

func (m *Mem) ListProjectRoutes(_ context.Context, projectID uuid.UUID) ([]store.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.Route{}
	if !m.projectAliveLocked(projectID) {
		return out, nil
	}
	for id, r := range m.edgeLocked().routes {
		if r.ProjectID == projectID {
			r.ID = id
			out = append(out, m.routeViewLocked(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Entry != out[j].Entry {
			return out[i].Entry < out[j].Entry
		}
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].PathPrefix < out[j].PathPrefix
	})
	return out, nil
}

func (m *Mem) ListRoutesByHostname(_ context.Context, hostnames []string) ([]store.Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]bool{}
	for _, h := range hostnames {
		want[h] = true
	}
	out := []store.Route{}
	for id, r := range m.edgeLocked().routes {
		if want[r.Hostname] && m.projectAliveLocked(r.ProjectID) {
			r.ID = id
			out = append(out, m.routeViewLocked(r))
		}
	}
	sortRoutesByHost(out)
	return out, nil
}

func (m *Mem) ReplaceEntryRoutes(_ context.Context, projectID, entryID uuid.UUID, routes []store.Route, confirm []byte, actor uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.planLocked(confirm, store.EdgePlanRoutes, actor, at)
	if err != nil {
		return err
	}
	if c.ProjectID == nil || *c.ProjectID != projectID || c.EntryID == nil || *c.EntryID != entryID {
		return store.ErrNotFound
	}
	e, ok := m.entries[entryID]
	if !ok || e.ProjectID != projectID || !m.projectAliveLocked(projectID) {
		return store.ErrNotFound
	}
	// The unique (hostname, path_prefix) index: rows of other entries,
	// including those of deleted projects, still hold their key.
	taken := map[string]bool{}
	for _, r := range m.edgeLocked().routes {
		if r.EntryID != entryID {
			taken[r.Key()] = true
		}
	}
	seen := map[string]bool{}
	for _, r := range routes {
		if taken[r.Key()] || seen[r.Key()] {
			return store.ErrConflict
		}
		seen[r.Key()] = true
	}
	for id, r := range m.edgeLocked().routes {
		if r.EntryID == entryID {
			delete(m.edgeLocked().routes, id)
		}
	}
	for _, r := range routes {
		r = cloneRoute(r)
		r.ProjectID, r.EntryID, r.Entry = projectID, entryID, e.Name
		r.CreatedBy, r.CreatedAt = actor, at
		if r.NoStorePaths == nil {
			r.NoStorePaths = []string{}
		}
		m.edgeLocked().routes[uuid.New()] = r
	}
	m.markPlanLocked(confirm, at)
	return nil
}

func (m *Mem) GetHostEdge(_ context.Context, hostID uuid.UUID) (store.HostEdge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[hostID]; !ok {
		return store.HostEdge{}, store.ErrNotFound
	}
	e, ok := m.edgeLocked().hostEdges[hostID]
	if !ok {
		return store.HostEdge{HostID: hostID, Status: store.EdgeOff}, nil
	}
	e.AppliedAt = cloneTime(e.AppliedAt)
	return e, nil
}

func (m *Mem) SetHostEdgeEnabled(_ context.Context, hostID uuid.UUID, enabled bool, at time.Time) (store.HostEdge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[hostID]; !ok {
		return store.HostEdge{}, store.ErrNotFound
	}
	e, ok := m.edgeLocked().hostEdges[hostID]
	if !ok {
		e = store.HostEdge{HostID: hostID, Status: store.EdgeOff}
	}
	e.Enabled = enabled
	if enabled || e.Status != store.EdgeOff {
		e.Status, e.Detail = store.EdgePending, ""
	}
	e.UpdatedAt = at
	m.edgeLocked().hostEdges[hostID] = e
	e.AppliedAt = cloneTime(e.AppliedAt)
	return e, nil
}

func (m *Mem) SetHostEdgeState(_ context.Context, e store.HostEdge) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[e.HostID]; !ok {
		return store.ErrNotFound
	}
	cur, ok := m.edgeLocked().hostEdges[e.HostID]
	if !ok {
		cur = store.HostEdge{HostID: e.HostID}
	}
	cur.Status, cur.Detail, cur.ConfigHash, cur.UpdatedAt = e.Status, e.Detail, e.ConfigHash, e.UpdatedAt
	cur.AppliedAt = cloneTime(e.AppliedAt)
	m.edgeLocked().hostEdges[e.HostID] = cur
	return nil
}

func (m *Mem) ListOrgEdges(_ context.Context, orgID uuid.UUID) ([]store.HostEdge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.HostEdge{}
	for id, e := range m.edgeLocked().hostEdges {
		h, ok := m.hosts[id]
		if !ok || h.OrgID != orgID || h.RevokedAt != nil {
			continue
		}
		if !e.Enabled && e.Status == store.EdgeOff {
			continue
		}
		e.AppliedAt = cloneTime(e.AppliedAt)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostID.String() < out[j].HostID.String() })
	return out, nil
}

// liveOwnedLocked reports whether svc is a live owned row of a live project
// on a host that is not revoked.
func (m *Mem) liveOwnedLocked(svc store.Service) bool {
	if svc.GoneAt != nil || svc.ProjectID == nil || svc.CatalogEntry == nil {
		return false
	}
	h, ok := m.hosts[svc.HostID]
	if !ok || h.RevokedAt != nil {
		return false
	}
	return m.projectAliveLocked(*svc.ProjectID)
}

func (m *Mem) EntryHosts(_ context.Context, projectID uuid.UUID) (map[string][]uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]uuid.UUID{}
	seen := map[string]bool{}
	for _, svc := range m.services {
		if !m.liveOwnedLocked(svc) || *svc.ProjectID != projectID {
			continue
		}
		key := *svc.CatalogEntry + "/" + svc.HostID.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out[*svc.CatalogEntry] = append(out[*svc.CatalogEntry], svc.HostID)
	}
	for name := range out {
		sortUUIDs(out[name])
	}
	return out, nil
}

func (m *Mem) HostEntries(_ context.Context, hostID uuid.UUID) ([]store.HostEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byKey := map[string]*store.HostEntry{}
	for _, svc := range m.services {
		if svc.HostID != hostID || !m.liveOwnedLocked(svc) {
			continue
		}
		key := svc.ProjectID.String() + "/" + *svc.CatalogEntry
		e, ok := byKey[key]
		if !ok {
			e = &store.HostEntry{ProjectID: *svc.ProjectID, Entry: *svc.CatalogEntry, Ports: []int{}}
			byKey[key] = e
		}
		e.Ports = append(e.Ports, svc.Ports...)
	}
	out := make([]store.HostEntry, 0, len(byKey))
	for _, e := range byKey {
		e.Ports = uniqueInts(e.Ports)
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID != out[j].ProjectID {
			return out[i].ProjectID.String() < out[j].ProjectID.String()
		}
		return out[i].Entry < out[j].Entry
	})
	return out, nil
}

func uniqueInts(in []int) []int {
	sort.Ints(in)
	out := []int{}
	for i, v := range in {
		if i == 0 || in[i-1] != v {
			out = append(out, v)
		}
	}
	return out
}

func (m *Mem) PutEdgeConfirmation(_ context.Context, hash []byte, c store.EdgeConfirmation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	plans := m.edgeLocked().confirmations
	for key, old := range plans {
		if old.ExpiresAt.Before(c.CreatedAt.Add(-store.RequestKeep)) {
			delete(plans, key)
		}
	}
	if _, dup := plans[hashKey(hash)]; dup {
		return store.ErrConflict
	}
	c.Body = append([]byte(nil), c.Body...)
	plans[hashKey(hash)] = c
	return nil
}

func (m *Mem) GetEdgeConfirmation(_ context.Context, hash []byte) (store.EdgeConfirmation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.edgeLocked().confirmations[hashKey(hash)]
	if !ok {
		return store.EdgeConfirmation{}, store.ErrNotFound
	}
	c.Body = append([]byte(nil), c.Body...)
	c.UsedAt = cloneTime(c.UsedAt)
	return c, nil
}

func (m *Mem) OrgACMEEmail(_ context.Context, orgID uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return "", store.ErrNotFound
	}
	return m.edgeLocked().acmeEmail[orgID], nil
}

func (m *Mem) SetOrgACMEEmail(_ context.Context, orgID uuid.UUID, email string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.liveOrgLocked(orgID); !ok {
		return store.ErrNotFound
	}
	if email == "" {
		delete(m.edgeLocked().acmeEmail, orgID)
		return nil
	}
	m.edgeLocked().acmeEmail[orgID] = email
	return nil
}

// dropEntryEdgeLocked removes an entry's routes and plans (the cascades of
// app.routes.entry_id and app.edge_confirmations.entry_id).
func (m *Mem) dropEntryEdgeLocked(entryID uuid.UUID) {
	if m.edge == nil {
		return
	}
	for id, r := range m.edge.routes {
		if r.EntryID == entryID {
			delete(m.edge.routes, id)
		}
	}
	for key, c := range m.edge.confirmations {
		if c.EntryID != nil && *c.EntryID == entryID {
			delete(m.edge.confirmations, key)
		}
	}
}

// dropProjectEdgeLocked removes a deleted project's routes and domain
// assignments, so their hostnames are free again (review 5).
func (m *Mem) dropProjectEdgeLocked(projectID uuid.UUID) {
	if m.edge == nil {
		return
	}
	for id, r := range m.edge.routes {
		if r.ProjectID == projectID {
			delete(m.edge.routes, id)
		}
	}
	for id, d := range m.edge.domains {
		kept := d.Assignments[:0:0]
		for _, a := range d.Assignments {
			if a.ProjectID != projectID {
				kept = append(kept, a)
			}
		}
		d.Assignments = kept
		m.edge.domains[id] = d
	}
}

// dropOrgDomainsLocked removes an org's domains when the org is deleted.
func (m *Mem) dropOrgDomainsLocked(orgID uuid.UUID) {
	if m.edge == nil {
		return
	}
	for id, d := range m.edge.domains {
		if d.OrgID == orgID {
			delete(m.edge.domains, id)
		}
	}
}

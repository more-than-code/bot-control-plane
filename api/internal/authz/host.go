package authz

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

const (
	LevelMetrics = "metrics"
	LevelDeploy  = "deploy"

	// Host and service capabilities. AuthorizeHost is the check.
	// Project routes keep using Authorize.
	// CapHostView covers seeing the host, its status and metrics, the full
	// process list, and host snapshot.
	CapHostView     = "host_view"
	CapHostServices = "host_services"
	CapServiceRead  = "service_read"
	// CapServiceWrite is deploy, run, stop, health-check edits and rename
	// (api-v1 v1.24 item 2): an org admin+, or an admin+ of a deploy project
	// for that service's own project.
	CapServiceWrite = "service_write"
	// CapHostAdmin is credential revoke, access grants and join tokens: org admin+.
	CapHostAdmin = "host_admin"
)

// Access is one project with access to the host that the caller belongs to,
// with the caller's role in that project.
type Access struct {
	ProjectID uuid.UUID
	Level     string
	Role      string
}

// Relation is one user's standing on one host (spec-organizations §3.2).
// OrgRole is the caller's role in the host's org ("" when none). Access is
// every project with access to the host that the caller is a member of.
type Relation struct {
	OrgID   uuid.UUID
	OrgRole string
	Access  []Access
}

// Relate builds a relation and drops access rows that are not usable.
func Relate(orgID uuid.UUID, orgRole string, access []Access) Relation {
	kept := make([]Access, 0, len(access))
	for _, a := range access {
		if a.ProjectID == uuid.Nil || a.Role == "" {
			continue
		}
		if a.Level != LevelMetrics && a.Level != LevelDeploy {
			continue
		}
		kept = append(kept, a)
	}
	return Relation{OrgID: orgID, OrgRole: orgRole, Access: kept}
}

// OrgAdmin reports whether the caller owns or administers the host's org.
func (rel Relation) OrgAdmin() bool {
	return orgAdminRole(rel.OrgRole)
}

// Sees reports whether the caller can see the host at all.
func (rel Relation) Sees() bool {
	return rel.OrgAdmin() || len(rel.Access) > 0
}

// HighestLevel is deploy when any of the caller's access rows is deploy,
// otherwise metrics. No rows returns "".
func HighestLevel(access []Access) string {
	level := ""
	for _, a := range access {
		if a.Level == LevelDeploy {
			return LevelDeploy
		}
		if a.Level == LevelMetrics {
			level = LevelMetrics
		}
	}
	return level
}

// ServiceProjectFilter is the project set for the service list.
// Nil means every service on the host, org-level rows included (an org
// admin). Otherwise it is the caller's deploy projects; the slice is empty
// when there are none, and an empty slice matches no rows.
// The process list is not filtered: it uses CapHostView.
func ServiceProjectFilter(rel Relation) []uuid.UUID {
	if rel.OrgAdmin() {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(rel.Access))
	for _, a := range rel.Access {
		if a.Level == LevelDeploy {
			ids = append(ids, a.ProjectID)
		}
	}
	return ids
}

// HostLookup loads the relation for a user and a host.
type HostLookup func(ctx context.Context, userID, hostID uuid.UUID) (Relation, error)

type hostKey struct{}

// WithHostLookup stores the host relation lookup on the request context.
func WithHostLookup(ctx context.Context, lookup HostLookup) context.Context {
	return context.WithValue(ctx, hostKey{}, lookup)
}

// HostRelation returns the caller's relation to hostID.
// A nil user is unauthorized. A missing lookup is a programmer error.
func HostRelation(ctx context.Context, userID, hostID uuid.UUID) (Relation, error) {
	if userID == uuid.Nil {
		return Relation{}, &Denied{Code: "unauthorized"}
	}
	lookup, _ := ctx.Value(hostKey{}).(HostLookup)
	if lookup == nil {
		return Relation{}, errors.New("authz host lookup is not configured")
	}
	return lookup(ctx, userID, hostID)
}

// AuthorizeHost decides whether rel may use capability.
// serviceProject is the service's project for service_read and
// service_write; nil is an org-level service. It is ignored for host-level
// capabilities. A caller who cannot see the host is not_found. One who is
// too weak is forbidden.
func AuthorizeHost(rel Relation, capability string, serviceProject *uuid.UUID) error {
	if !rel.Sees() {
		return &Denied{Code: "not_found"}
	}
	if !hostAllows(rel, capability, serviceProject) {
		return &Denied{Code: "forbidden"}
	}
	return nil
}

// AuthorizeAccessRemove decides whether rel may remove targetProjectID's
// access (api-v1 v1.24 item 3): an org admin+, or an admin+ of that project
// leaving. projectRole is the caller's role in that project ("" when none).
// A caller who cannot see the host is not_found.
func AuthorizeAccessRemove(rel Relation, projectRole string) error {
	if !rel.Sees() {
		return &Denied{Code: "not_found"}
	}
	if rel.OrgAdmin() || adminRole(projectRole) {
		return nil
	}
	return &Denied{Code: "forbidden"}
}

func hostAllows(rel Relation, capability string, serviceProject *uuid.UUID) bool {
	if rel.OrgAdmin() {
		switch capability {
		case CapHostView, CapHostServices, CapServiceRead, CapServiceWrite, CapHostAdmin:
			return true
		default:
			return false
		}
	}
	switch capability {
	case CapHostView:
		return len(rel.Access) > 0
	case CapHostServices:
		return HighestLevel(rel.Access) == LevelDeploy
	case CapServiceRead:
		_, ok := deployAccess(rel, serviceProject)
		return ok
	case CapServiceWrite:
		a, ok := deployAccess(rel, serviceProject)
		return ok && adminRole(a.Role)
	default:
		return false
	}
}

// deployAccess is the caller's deploy row for this service's project.
// An org-level service (nil), a metrics row, or no row does not match.
func deployAccess(rel Relation, projectID *uuid.UUID) (Access, bool) {
	if projectID == nil || *projectID == uuid.Nil {
		return Access{}, false
	}
	for _, a := range rel.Access {
		if a.ProjectID == *projectID && a.Level == LevelDeploy {
			return a, true
		}
	}
	return Access{}, false
}

func adminRole(role string) bool {
	return role == RoleOwner || role == RoleAdmin
}

// ProjectServiceVisible is the v1.3 project-services row rule under v1.24:
// a project sees its own services on hosts where it has deploy access.
// Org-level rows (nil project) are never in a project list.
func ProjectServiceVisible(level string, serviceProject *uuid.UUID, projectID uuid.UUID) bool {
	if projectID == uuid.Nil || serviceProject == nil {
		return false
	}
	return level == LevelDeploy && *serviceProject == projectID
}

// ProjectHostReached reports whether a project services list may filter
// to this host: the project has deploy access.
func ProjectHostReached(level string) bool {
	return level == LevelDeploy
}

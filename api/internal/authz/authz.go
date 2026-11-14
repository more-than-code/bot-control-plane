// Package authz is the one permission check for every authenticated request.
package authz

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

const (
	// CapSession is any signed-in user. It is not a project capability.
	// Project routes use the capabilities below, which follow design §2.
	CapSession      = "session"
	CapRead         = "read"
	CapProbe        = "probe"
	CapJoinToken    = "join_token"
	CapMembers      = "members"
	CapProjectWrite = "project_write"
	// CapCatalogEdit is catalog replace: owner or admin. Create, delete, and
	// permissions stay CapProjectWrite.
	CapCatalogEdit = "catalog_edit"
	// CapHealthWrite and CapAuditRead are phase-1 admin+ routes that §2 does
	// not name on their own. See NOTES.md.
	CapHealthWrite = "health_write"
	CapAuditRead   = "audit_read"

	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleViewer = "viewer"

	// Org roles (api-v1 v1.24).
	OrgOwner  = "owner"
	OrgAdmin  = "admin"
	OrgMember = "member"
)

// ProjectFacts is the caller's standing in one project. MemberRole is their
// project role ("" when not a member). OrgRole is their role in the
// project's org ("" when not an org member). Found is false when the
// project does not exist or is deleted.
type ProjectFacts struct {
	Found      bool
	MemberRole string
	OrgRole    string
}

// Lookup loads the caller's standing in a project.
type Lookup func(ctx context.Context, userID, projectID uuid.UUID) (ProjectFacts, error)

type ctxKey struct{}

// WithLookup stores the role lookup on the request context.
func WithLookup(ctx context.Context, lookup Lookup) context.Context {
	return context.WithValue(ctx, ctxKey{}, lookup)
}

// Denied is an authorization failure. Code is "forbidden", "not_found", or
// "unauthorized".
type Denied struct {
	Code string
}

func (d *Denied) Error() string { return d.Code }

// Allows reports whether role may use capability. The zero role never may,
// except that CapSession does not consult a role.
func Allows(role, capability string) bool {
	switch capability {
	case CapSession:
		return role == RoleOwner || role == RoleAdmin || role == RoleViewer
	case CapRead, CapProbe:
		return role == RoleOwner || role == RoleAdmin || role == RoleViewer
	case CapJoinToken, CapMembers, CapHealthWrite, CapAuditRead, CapCatalogEdit:
		return role == RoleOwner || role == RoleAdmin
	case CapProjectWrite:
		return role == RoleOwner
	default:
		return false
	}
}

// orgAdminRole reports whether an org role manages the org.
func orgAdminRole(role string) bool {
	return role == OrgOwner || role == OrgAdmin
}

// AllowsProject is the v1.24 project rule (item 2): a project member's role,
// or an org owner or admin of the project's org acting as admin. The org
// owner also holds CapProjectWrite (rename, delete, transfer).
func AllowsProject(f ProjectFacts, capability string) bool {
	if !f.Found {
		return false
	}
	if f.MemberRole != "" && Allows(f.MemberRole, capability) {
		return true
	}
	if orgAdminRole(f.OrgRole) && Allows(RoleAdmin, capability) {
		return true
	}
	return f.OrgRole == OrgOwner && capability == CapProjectWrite
}

// Reaches reports whether the caller sees the project at all: a member, or
// an org owner or admin of its org. An org member without a project role does not.
func Reaches(f ProjectFacts) bool {
	return f.Found && (f.MemberRole != "" || orgAdminRole(f.OrgRole))
}

// AllCapabilities is every capability Authorize understands, for tests.
func AllCapabilities() []string {
	return []string{
		CapSession,
		CapRead,
		CapProbe,
		CapJoinToken,
		CapMembers,
		CapProjectWrite,
		CapCatalogEdit,
		CapHealthWrite,
		CapAuditRead,
	}
}

// Authorize decides whether userID may use capability in projectID.
// CapSession ignores projectID and succeeds for any non-nil user.
// A caller the project does not reach (Reaches) gets not_found. One whose
// role is too low gets forbidden.
func Authorize(ctx context.Context, userID, projectID uuid.UUID, capability string) error {
	if userID == uuid.Nil {
		return &Denied{Code: "unauthorized"}
	}
	if capability == CapSession {
		return nil
	}
	lookup, _ := ctx.Value(ctxKey{}).(Lookup)
	if lookup == nil {
		return errors.New("authz lookup is not configured")
	}
	facts, err := lookup(ctx, userID, projectID)
	if err != nil {
		return err
	}
	if !Reaches(facts) {
		return &Denied{Code: "not_found"}
	}
	if !AllowsProject(facts, capability) {
		return &Denied{Code: "forbidden"}
	}
	return nil
}

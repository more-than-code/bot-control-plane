package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// Expected grants are the phase-1 matrix. They are written out here so the
// test does not echo Allows back at itself.
func TestAuthorizeMatrix(t *testing.T) {
	const (
		session      = "session"
		read         = "read"
		probe        = "probe"
		joinToken    = "join_token"
		members      = "members"
		projectWrite = "project_write"
		catalogEdit  = "catalog_edit"
		healthWrite  = "health_write"
		auditRead    = "audit_read"
	)
	allowed := map[string]map[string]bool{
		RoleViewer: {
			session: true, read: true, probe: true,
			joinToken: false, members: false, projectWrite: false, catalogEdit: false,
			healthWrite: false, auditRead: false,
		},
		RoleAdmin: {
			session: true, read: true, probe: true,
			joinToken: true, members: true, projectWrite: false, catalogEdit: true,
			healthWrite: true, auditRead: true,
		},
		RoleOwner: {
			session: true, read: true, probe: true,
			joinToken: true, members: true, projectWrite: true, catalogEdit: true,
			healthWrite: true, auditRead: true,
		},
	}
	caps := []string{session, read, probe, joinToken, members, projectWrite, catalogEdit, healthWrite, auditRead}
	projectID := uuid.New()
	for _, role := range []string{RoleViewer, RoleAdmin, RoleOwner} {
		userID := uuid.New()
		ctx := WithLookup(context.Background(), func(context.Context, uuid.UUID, uuid.UUID) (ProjectFacts, error) {
			return ProjectFacts{Found: true, MemberRole: role, OrgRole: OrgMember}, nil
		})
		for _, cap := range caps {
			err := Authorize(ctx, userID, projectID, cap)
			if allowed[role][cap] {
				if err != nil {
					t.Fatalf("%s %s: %v", role, cap, err)
				}
				continue
			}
			var denied *Denied
			if !errors.As(err, &denied) || denied.Code != "forbidden" {
				t.Fatalf("%s %s: got %v, want forbidden", role, cap, err)
			}
		}
	}
}

func TestAuthorizeNonMember(t *testing.T) {
	// A missing project, a stranger, and an org member without a project role
	// (api-v1 v1.24 item 2) are all not_found.
	for _, facts := range []ProjectFacts{{}, {Found: true}, {Found: true, OrgRole: OrgMember}} {
		ctx := WithLookup(context.Background(), func(context.Context, uuid.UUID, uuid.UUID) (ProjectFacts, error) {
			return facts, nil
		})
		userID := uuid.New()
		for _, cap := range []string{"read", "probe", "join_token", "members", "project_write", "catalog_edit", "health_write", "audit_read", "nope"} {
			err := Authorize(ctx, userID, uuid.New(), cap)
			var denied *Denied
			if !errors.As(err, &denied) || denied.Code != "not_found" {
				t.Fatalf("%+v %s: got %v, want not_found", facts, cap, err)
			}
		}
		if err := Authorize(ctx, userID, uuid.Nil, CapSession); err != nil {
			t.Fatalf("session: %v", err)
		}
	}
}

// TestAuthorizeOrgReach is api-v1 v1.24 item 2 / D-1: an org owner or admin
// acts as project admin without a member row; only the org owner (or the
// project owner) holds project_write. A viewer who is also an org admin is admin.
func TestAuthorizeOrgReach(t *testing.T) {
	adminCaps := map[string]bool{
		CapRead: true, CapProbe: true, CapJoinToken: true, CapMembers: true,
		CapCatalogEdit: true, CapHealthWrite: true, CapAuditRead: true,
	}
	cases := []struct {
		name  string
		facts ProjectFacts
		write bool
	}{
		{"org admin, no member row", ProjectFacts{Found: true, OrgRole: OrgAdmin}, false},
		{"org owner, no member row", ProjectFacts{Found: true, OrgRole: OrgOwner}, true},
		{"viewer and org admin", ProjectFacts{Found: true, MemberRole: RoleViewer, OrgRole: OrgAdmin}, false},
		{"project owner, outside the org", ProjectFacts{Found: true, MemberRole: RoleOwner}, true},
	}
	for _, tc := range cases {
		ctx := WithLookup(context.Background(), func(context.Context, uuid.UUID, uuid.UUID) (ProjectFacts, error) {
			return tc.facts, nil
		})
		for cap := range adminCaps {
			if err := Authorize(ctx, uuid.New(), uuid.New(), cap); err != nil {
				t.Fatalf("%s %s: %v", tc.name, cap, err)
			}
		}
		err := Authorize(ctx, uuid.New(), uuid.New(), CapProjectWrite)
		if tc.write != (err == nil) {
			t.Fatalf("%s project_write: %v", tc.name, err)
		}
	}
}

func TestAuthorizeAnonymousAndMemberUnknown(t *testing.T) {
	ctx := WithLookup(context.Background(), func(context.Context, uuid.UUID, uuid.UUID) (ProjectFacts, error) {
		return ProjectFacts{Found: true, MemberRole: RoleViewer}, nil
	})
	err := Authorize(ctx, uuid.Nil, uuid.New(), CapRead)
	var denied *Denied
	if !errors.As(err, &denied) || denied.Code != "unauthorized" {
		t.Fatalf("anonymous: %v", err)
	}
	err = Authorize(ctx, uuid.New(), uuid.New(), "nope")
	if !errors.As(err, &denied) || denied.Code != "forbidden" {
		t.Fatalf("unknown cap: %v", err)
	}
}

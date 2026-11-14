package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// TestAuthorizeHostMatrix is spec-organizations §3.2 (api-v1 v1.24 item 2) at
// the authz function. The expected codes are written out per cell so the test
// does not echo hostAllows back at itself. HTTP coverage of the same cells is
// in the api package.
func TestAuthorizeHostMatrix(t *testing.T) {
	own := uuid.MustParse("00000000-0000-4000-8000-0000000000b1")
	other := uuid.MustParse("00000000-0000-4000-8000-0000000000c2")
	org := uuid.MustParse("00000000-0000-4000-8000-0000000000a0")

	orgOwner := Relate(org, OrgOwner, nil)
	orgAdmin := Relate(org, OrgAdmin, nil)
	deployAdmin := Relate(org, "", []Access{{ProjectID: own, Level: LevelDeploy, Role: RoleAdmin}})
	deployViewer := Relate(org, "", []Access{{ProjectID: own, Level: LevelDeploy, Role: RoleViewer}})
	metrics := Relate(org, "", []Access{{ProjectID: own, Level: LevelMetrics, Role: RoleOwner}})
	orgMember := Relate(org, OrgMember, nil)
	none := Relate(org, "", nil)

	ok, no, miss := "", "forbidden", "not_found"
	actors := []struct {
		name string
		rel  Relation
	}{
		{"org_owner", orgOwner},
		{"org_admin", orgAdmin},
		{"deploy_admin", deployAdmin},
		{"deploy_viewer", deployViewer},
		{"metrics", metrics},
		{"org_member", orgMember},
		{"none", none},
	}
	ownP, otherP := &own, &other
	var orgLevel *uuid.UUID
	// cap, service project, then one code per actor in the order above.
	cases := []struct {
		cap     string
		project *uuid.UUID
		want    []string
	}{
		{CapHostView, nil, []string{ok, ok, ok, ok, ok, miss, miss}},
		{CapHostServices, nil, []string{ok, ok, ok, ok, no, miss, miss}},
		{CapServiceRead, ownP, []string{ok, ok, ok, ok, no, miss, miss}},
		{CapServiceRead, otherP, []string{ok, ok, no, no, no, miss, miss}},
		{CapServiceRead, orgLevel, []string{ok, ok, no, no, no, miss, miss}},
		{CapServiceWrite, ownP, []string{ok, ok, ok, no, no, miss, miss}},
		{CapServiceWrite, otherP, []string{ok, ok, no, no, no, miss, miss}},
		{CapServiceWrite, orgLevel, []string{ok, ok, no, no, no, miss, miss}},
		{CapHostAdmin, nil, []string{ok, ok, no, no, no, miss, miss}},
		{"nope", nil, []string{no, no, no, no, no, miss, miss}},
	}
	for _, tc := range cases {
		for i, a := range actors {
			err := AuthorizeHost(a.rel, tc.cap, tc.project)
			got := ""
			var denied *Denied
			if errors.As(err, &denied) {
				got = denied.Code
			} else if err != nil {
				t.Fatalf("%s %s: unexpected %v", a.name, tc.cap, err)
			}
			if got != tc.want[i] {
				t.Fatalf("%s %s project=%v: got %q, want %q", a.name, tc.cap, tc.project, got, tc.want[i])
			}
		}
	}
}

func TestRelateDropsUnusableRows(t *testing.T) {
	p := uuid.New()
	rel := Relate(uuid.New(), "", []Access{
		{ProjectID: uuid.Nil, Level: LevelDeploy, Role: RoleAdmin},
		{ProjectID: p, Level: "owner", Role: RoleAdmin},
		{ProjectID: p, Level: LevelDeploy, Role: ""},
	})
	if rel.Sees() || len(rel.Access) != 0 {
		t.Fatalf("kept %+v", rel)
	}
}

func TestServiceProjectFilter(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	if got := ServiceProjectFilter(Relate(uuid.New(), OrgAdmin, nil)); got != nil {
		t.Fatalf("org admin filter %v, want nil (every service)", got)
	}
	// An org admin who is also a deploy member still sees every service.
	if got := ServiceProjectFilter(Relate(uuid.New(), OrgOwner, []Access{{ProjectID: a, Level: LevelDeploy, Role: RoleViewer}})); got != nil {
		t.Fatalf("org owner filter %v", got)
	}
	got := ServiceProjectFilter(Relate(uuid.New(), OrgMember, []Access{
		{ProjectID: a, Level: LevelDeploy, Role: RoleViewer},
		{ProjectID: b, Level: LevelMetrics, Role: RoleAdmin},
	}))
	if len(got) != 1 || got[0] != a {
		t.Fatalf("member filter %v", got)
	}
	got = ServiceProjectFilter(Relate(uuid.New(), "", []Access{{ProjectID: b, Level: LevelMetrics, Role: RoleAdmin}}))
	if got == nil || len(got) != 0 {
		t.Fatalf("metrics filter %v, want empty non-nil", got)
	}
}

func TestHighestLevel(t *testing.T) {
	p := uuid.New()
	if HighestLevel(nil) != "" {
		t.Fatal("empty")
	}
	if HighestLevel([]Access{{ProjectID: p, Level: LevelMetrics}}) != LevelMetrics {
		t.Fatal("metrics")
	}
	if HighestLevel([]Access{{ProjectID: p, Level: LevelMetrics}, {ProjectID: p, Level: LevelDeploy}}) != LevelDeploy {
		t.Fatal("deploy")
	}
}

// TestAuthorizeAccessRemove is the DELETE /v1/hosts/{id}/access/{project_id}
// rule: org admin+, or an admin+ of that project leaving.
func TestAuthorizeAccessRemove(t *testing.T) {
	p := uuid.New()
	cases := []struct {
		name string
		rel  Relation
		role string
		want string
	}{
		{"org admin", Relate(uuid.New(), OrgAdmin, nil), "", ""},
		{"project admin leaving", Relate(uuid.New(), "", []Access{{ProjectID: p, Level: LevelMetrics, Role: RoleAdmin}}), RoleAdmin, ""},
		{"project owner leaving", Relate(uuid.New(), "", []Access{{ProjectID: p, Level: LevelDeploy, Role: RoleOwner}}), RoleOwner, ""},
		{"project viewer", Relate(uuid.New(), "", []Access{{ProjectID: p, Level: LevelDeploy, Role: RoleViewer}}), RoleViewer, "forbidden"},
		{"admin of another project", Relate(uuid.New(), "", []Access{{ProjectID: uuid.New(), Level: LevelDeploy, Role: RoleAdmin}}), "", "forbidden"},
		{"stranger", Relate(uuid.New(), "", nil), RoleAdmin, "not_found"},
		{"org member", Relate(uuid.New(), OrgMember, nil), "", "not_found"},
	}
	for _, tc := range cases {
		err := AuthorizeAccessRemove(tc.rel, tc.role)
		got := ""
		var denied *Denied
		if errors.As(err, &denied) {
			got = denied.Code
		}
		if got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestProjectServiceVisible(t *testing.T) {
	p, q := uuid.New(), uuid.New()
	if !ProjectServiceVisible(LevelDeploy, &p, p) {
		t.Fatal("own deploy row hidden")
	}
	if ProjectServiceVisible(LevelDeploy, &q, p) {
		t.Fatal("another project's row shown")
	}
	if ProjectServiceVisible(LevelDeploy, nil, p) {
		t.Fatal("org-level row shown in a project list")
	}
	if ProjectServiceVisible(LevelMetrics, &p, p) {
		t.Fatal("metrics access shows services")
	}
	if ProjectServiceVisible("", &p, p) || ProjectServiceVisible(LevelDeploy, &p, uuid.Nil) {
		t.Fatal("no access shown")
	}
	if !ProjectHostReached(LevelDeploy) || ProjectHostReached(LevelMetrics) || ProjectHostReached("") {
		t.Fatal("reached")
	}
}

func TestHostRelationNeedsUserAndLookup(t *testing.T) {
	_, err := HostRelation(context.Background(), uuid.Nil, uuid.New())
	var denied *Denied
	if !errors.As(err, &denied) || denied.Code != "unauthorized" {
		t.Fatalf("nil user: %v", err)
	}
	if _, err := HostRelation(context.Background(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("missing lookup accepted")
	}
	want := Relate(uuid.New(), OrgAdmin, nil)
	ctx := WithHostLookup(context.Background(), func(context.Context, uuid.UUID, uuid.UUID) (Relation, error) {
		return want, nil
	})
	got, err := HostRelation(ctx, uuid.New(), uuid.New())
	if err != nil || got.OrgRole != OrgAdmin {
		t.Fatalf("lookup: %+v %v", got, err)
	}
}

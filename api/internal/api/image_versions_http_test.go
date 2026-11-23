package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// fakePackages is GitHub's container package versions for acme/web.
type fakePackages struct {
	mu       sync.Mutex
	versions []map[string]any
	deleted  []string
	refuse   bool
	auth     []string
}

func digestOf(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func (p *fakePackages) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.auth = append(p.auth, r.Header.Get("Authorization"))
	const base = "/orgs/acme/packages/container/web/versions"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/users/acme":
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "acme", "type": "Organization"})
	case r.Method == http.MethodGet && r.URL.Path == base:
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_ = json.NewEncoder(w).Encode(p.versions)
	case strings.HasPrefix(r.URL.Path, base+"/"):
		id := strings.TrimPrefix(r.URL.Path, base+"/")
		for i, v := range p.versions {
			if v["id"].(json.Number).String() != id {
				continue
			}
			if r.Method == http.MethodDelete {
				if p.refuse {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"message":"GITHUB-BODY-DETAIL"}`))
					return
				}
				p.deleted = append(p.deleted, id)
				p.versions = append(p.versions[:i], p.versions[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(v)
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func pkgVersion(id, digest string, tags []string, updated time.Time) map[string]any {
	return map[string]any{
		"id": json.Number(id), "name": digest,
		"created_at": updated.Format(time.RFC3339), "updated_at": updated.Format(time.RFC3339),
		"metadata": map[string]any{"package_type": "container", "container": map[string]any{"tags": tags}},
	}
}

type versionsFixture struct {
	*registryFixture
	gh  *fakeGitHub
	pkg *fakePackages
}

// newVersionsFixture: a project with a Connect GitHub login (delete
// granted when canDelete), an image entry web on ghcr.io/acme/web whose
// last deploy to pi4 succeeded with digest A, and four package versions:
// A (in use), B (stale), C (stale release tag), D (fresh, unused).
func newVersionsFixture(t *testing.T, canDelete bool) *versionsFixture {
	t.Helper()
	f := newRegistryFixture(t)
	gh := newFakeGitHub(t)
	wireGitHub(f.w, gh)
	f.w.srv.Secrets = testBox(t)
	pkg := &fakePackages{}
	old := f.w.now.Add(-30 * 24 * time.Hour)
	pkg.versions = []map[string]any{
		pkgVersion("101", digestOf("a"), []string{"dev", "sha-a"}, old),
		pkgVersion("102", digestOf("b"), []string{"sha-b"}, old),
		pkgVersion("103", digestOf("c"), []string{"release-1"}, old),
		pkgVersion("104", digestOf("d"), []string{"sha-d"}, f.w.now.Add(-time.Hour)),
	}
	gh.set(func(g *fakeGitHub) {
		g.packages = pkg.handle
		if canDelete {
			g.scopes = "read:packages, delete:packages"
		}
	})
	body := ""
	if canDelete {
		body = `{"delete_packages":true}`
	}
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, githubStartPath(f.project), body)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	u, _ := url.Parse(objectPayload(t, env)["authorize_url"].(string))
	wantScope := "read:packages"
	if canDelete {
		wantScope = "read:packages delete:packages"
	}
	if u.Query().Get("scope") != wantScope {
		t.Fatalf("scope %q", u.Query().Get("scope"))
	}
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", u.Query().Get("state")))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if items := listRegistries(t, f.w, f.owner, f.project); len(items) != 1 || items[0]["delete_packages"] != canDelete {
		t.Fatalf("registries %v", items)
	}

	ctx := context.Background()
	img, err := catalog.BuildImage("web", []byte(`{"kind":"service","image":"ghcr.io/acme/web:dev"}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hash := catalog.ImageHash(img, nil)
	ownerID := f.sc.project.CreatedBy
	entry, err := f.w.mem.CreateCatalogEntry(ctx, f.sc.project.ID, store.CatalogWrite{
		Name: "web", Kind: "image", ImageRaw: []byte(catalog.SpecJSON(img)),
		SpecHash: hash, CommandDisplay: img.Image, Actor: ownerID, At: f.w.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.w.mem.BeginImageOp(ctx, entry.ID, f.sc.host.ID, hash, ownerID, "console", f.w.now, f.w.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	d := digestOf("a")
	if _, err := f.w.mem.FinishImageDeploy(ctx, row.ID, "succeeded", &d, nil, f.w.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return &versionsFixture{registryFixture: f, gh: gh, pkg: pkg}
}

func versionsPath(project string) string {
	return "/v1/projects/" + project + "/image-versions?image=" + url.QueryEscape("ghcr.io/acme/web:dev")
}

func versionDeletePath(project, id string) string {
	return "/v1/projects/" + project + "/image-versions/" + id + "?image=" + url.QueryEscape("ghcr.io/acme/web")
}

func deleteBody(digest, confirm string) string {
	raw, _ := json.Marshal(map[string]string{"digest": digest, "confirm": confirm})
	return string(raw)
}

func TestImageVersionsListAndDelete(t *testing.T) {
	f := newVersionsFixture(t, true)
	status, env, raw := mustCall(t, f.w, f.owner, http.MethodGet, versionsPath(f.project), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if strings.Contains(raw, fakeGitHubToken) {
		t.Fatal("token in response")
	}
	p := objectPayload(t, env)
	if p["can_delete"] != true || p["can_delete_reason"] != nil || p["truncated"] != false {
		t.Fatalf("payload %v", p)
	}
	items := p["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("items %v", items)
	}
	byID := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byID[m["digest"].(string)] = m
	}
	a := byID[digestOf("a")]
	held := a["in_use"].([]any)
	h0 := held[0].(map[string]any)
	if len(held) != 1 || h0["entry"] != "web" || h0["host_name"] != "pi4" || h0["why"] != "rollback_target" || h0["project_name"] != "Proj" || a["stale"] != false {
		t.Fatalf("A %v", a)
	}
	if b := byID[digestOf("b")]; b["stale"] != true || b["release"] != false || len(b["in_use"].([]any)) != 0 {
		t.Fatalf("B %v", b)
	}
	if c := byID[digestOf("c")]; c["stale"] != true || c["release"] != true {
		t.Fatalf("C %v", c)
	}
	if d := byID[digestOf("d")]; d["stale"] != false {
		t.Fatalf("D %v", d)
	}
	for _, a := range f.pkg.auth {
		if !strings.HasPrefix(a, "Bearer "+fakeGitHubToken) {
			t.Fatalf("auth %q", a)
		}
	}

	// A held digest is refused and names its holder.
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodDelete, versionDeletePath(f.project, "101"), deleteBody(digestOf("a"), strings.Repeat("a", 12)))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "in use: web on pi4 (rollback_target)")
	// A digest that is not the version's: changed.
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodDelete, versionDeletePath(f.project, "102"), deleteBody(digestOf("c"), strings.Repeat("c", 12)))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", msgVersionChanged)
	// A wrong confirmation.
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodDelete, versionDeletePath(f.project, "102"), deleteBody(digestOf("b"), "bbbb"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgDeleteConfirm)
	// Unknown version.
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodDelete, versionDeletePath(f.project, "999"), deleteBody(digestOf("b"), strings.Repeat("b", 12)))
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	if len(f.pkg.deleted) != 0 {
		t.Fatalf("deleted %v", f.pkg.deleted)
	}

	before := len(f.w.mem.AllAudits())
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodDelete, versionDeletePath(f.project, "102"), deleteBody(digestOf("b"), strings.Repeat("b", 12)))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if out := objectPayload(t, env); out["deleted"] != true || out["digest"] != digestOf("b") {
		t.Fatalf("delete %v", out)
	}
	if len(f.pkg.deleted) != 1 || f.pkg.deleted[0] != "102" {
		t.Fatalf("deleted %v", f.pkg.deleted)
	}
	audits := f.w.mem.AllAudits()
	if len(audits) != before+1 {
		t.Fatalf("audits %d", len(audits)-before)
	}
	row := audits[len(audits)-1]
	detail, _ := json.Marshal(row.Detail)
	if row.Action != audit.ActionRegistryImageVersionDeleted || !strings.Contains(string(detail), `"package":"acme/web"`) ||
		!strings.Contains(string(detail), `"version_id":102`) || !strings.Contains(string(detail), `"tags":["sha-b"]`) ||
		strings.Contains(string(detail), fakeGitHubToken) {
		t.Fatalf("audit %s %s", row.Action, detail)
	}

	// GitHub refusing the delete is delete_refused, with no body echoed.
	f.pkg.mu.Lock()
	f.pkg.refuse = true
	f.pkg.mu.Unlock()
	status, env, raw = mustCall(t, f.w, f.owner, http.MethodDelete, versionDeletePath(f.project, "104"), deleteBody(digestOf("d"), strings.Repeat("d", 12)))
	if status != http.StatusBadGateway || env.Code != "registry_unavailable" || objectPayload(t, env)["reason"] != "delete_refused" || strings.Contains(raw, "GITHUB-BODY") {
		t.Fatalf("refused %d %s", status, raw)
	}
}

func TestImageVersionsAccess(t *testing.T) {
	f := newVersionsFixture(t, true)
	// A project admin who is not an org admin lists, but cannot delete.
	status, env, _ := mustCall(t, f.w, f.admin, http.MethodGet, versionsPath(f.project), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if p := objectPayload(t, env); p["can_delete"] != false || p["can_delete_reason"] != msgDeleteOrgAdmin {
		t.Fatalf("admin payload %v", p)
	}
	status, env, _ = mustCall(t, f.w, f.admin, http.MethodDelete, versionDeletePath(f.project, "102"), deleteBody(digestOf("b"), strings.Repeat("b", 12)))
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", msgDeleteOrgAdmin)
	// A viewer gets nothing.
	status, env, _ = mustCall(t, f.w, f.viewer, http.MethodGet, versionsPath(f.project), "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	// Delegated sessions are refused.
	status, env, _ = mustCall(t, f.w, f.del, http.MethodGet, versionsPath(f.project), "")
	if status != http.StatusForbidden {
		t.Fatalf("delegated list %d %v", status, env)
	}
	status, env, _ = mustCall(t, f.w, f.del, http.MethodDelete, versionDeletePath(f.project, "102"), deleteBody(digestOf("b"), strings.Repeat("b", 12)))
	if status != http.StatusForbidden {
		t.Fatalf("delegated delete %d %v", status, env)
	}
	// Only ghcr.io.
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodGet, "/v1/projects/"+f.project+"/image-versions?image=nginx", "")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgVersionsGHCROnly)
	if len(f.pkg.deleted) != 0 {
		t.Fatalf("deleted %v", f.pkg.deleted)
	}
}

func TestImageVersionsWithoutDeleteScope(t *testing.T) {
	f := newVersionsFixture(t, false)
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodGet, versionsPath(f.project), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if p := objectPayload(t, env); p["can_delete"] != false || p["can_delete_reason"] != msgDeleteNoScope {
		t.Fatalf("payload %v", p)
	}
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodDelete, versionDeletePath(f.project, "102"), deleteBody(digestOf("b"), strings.Repeat("b", 12)))
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", msgDeleteNoScope)

	// The opt-in step without the scope granted leaves the login as it was.
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, githubStartPath(f.project), `{"delete_packages":true}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	u, _ := url.Parse(objectPayload(t, env)["authorize_url"].(string))
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", u.Query().Get("state")))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgGitHubDeleteScope)
	if items := listRegistries(t, f.w, f.owner, f.project); len(items) != 1 || items[0]["delete_packages"] != false {
		t.Fatalf("registries %v", items)
	}
}

func TestImageVersionsNeedGitHubLogin(t *testing.T) {
	f := newRegistryFixture(t)
	gh := newFakeGitHub(t)
	wireGitHub(f.w, gh)
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodGet, versionsPath(f.project), "")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgVersionsNeedGitHub)
}

// The tag picker lists a ghcr.io image's tags newest first when the
// project's Connect GitHub login can read the package versions; tags GitHub
// does not know follow in the plain order.
func TestImageTagsNewestFirstWithGitHub(t *testing.T) {
	f := newVersionsFixture(t, false)
	f.pkg.versions = []map[string]any{
		pkgVersion("101", digestOf("a"), []string{"sha-a", "dev"}, f.w.now.Add(-2*time.Hour)),
		pkgVersion("102", digestOf("b"), []string{"sha-b"}, f.w.now.Add(-48*time.Hour)),
		pkgVersion("104", digestOf("d"), []string{"sha-d", "release-2"}, f.w.now.Add(-time.Hour)),
	}
	f.public([]string{"dev", "release-2", "sha-a", "sha-b", "sha-d", "zz-untracked"}, digestOf("a"))
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodGet, tagsPath(f.project, "ghcr.io/acme/web"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	var got []string
	for _, item := range objectPayload(t, env)["items"].([]any) {
		got = append(got, item.(map[string]any)["tag"].(string))
	}
	if want := "sha-d,release-2,sha-a,dev,sha-b,zz-untracked"; strings.Join(got, ",") != want {
		t.Fatalf("tags %s, want %s", strings.Join(got, ","), want)
	}
	// api-v1 v1.46 item 1: the order and each known tag's push time.
	payload := objectPayload(t, env)
	if payload["order"] != "time" {
		t.Fatalf("order %v, want time", payload["order"])
	}
	items := payload["items"].([]any)
	if at := items[0].(map[string]any)["updated_at"]; at != f.w.now.Add(-time.Hour).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("sha-d updated_at %v", at)
	}
	if _, has := items[5].(map[string]any)["updated_at"]; has {
		t.Fatal("zz-untracked has updated_at")
	}

	// Without GitHub's versions the plain order stays.
	f.pkg.mu.Lock()
	f.pkg.versions = nil
	f.pkg.mu.Unlock()
	f.w.srv.Registry.Cache.DropPrefix("tags|")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodGet, tagsPath(f.project, "ghcr.io/acme/web"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if first := objectPayload(t, env)["items"].([]any)[0].(map[string]any)["tag"]; first != "zz-untracked" {
		t.Fatalf("first tag %v", first)
	}
	if order := objectPayload(t, env)["order"]; order != "name" {
		t.Fatalf("order %v, want name", order)
	}
}

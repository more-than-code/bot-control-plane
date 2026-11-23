package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.52: an assignment may name one host below the domain, or a
// narrower wildcard, and a hostname under a verified domain that nothing
// assigns yet is refused with the console link that assigns it.

func TestStoreAssignmentPatterns(t *testing.T) {
	for _, p := range []string{"@", "*", "app", "api.dev", "*.dev", "a-b.c"} {
		if !store.ValidPattern("example.com", p) {
			t.Fatalf("pattern %q should be valid", p)
		}
	}
	for _, p := range []string{"", "**", "*.", "a..b", "-app", "app.", "App", "*.*", strings.Repeat("a", 242)} {
		if store.ValidPattern("example.com", p) {
			t.Fatalf("pattern %q should be invalid", p)
		}
	}
	cases := []struct {
		pattern, hostname string
		covers            bool
	}{
		{"@", "example.com", true},
		{"@", "app.example.com", false},
		{"*", "app.example.com", true},
		{"*", "a.b.example.com", false},
		{"app", "app.example.com", true},
		{"app", "other.example.com", false},
		{"app", "x.app.example.com", false},
		{"*.dev", "a.dev.example.com", true},
		{"*.dev", "dev.example.com", false},
		{"*.dev", "a.b.dev.example.com", false},
		{"api.dev", "api.dev.example.com", true},
	}
	for _, c := range cases {
		if got := store.Covers("example.com", c.pattern, c.hostname); got != c.covers {
			t.Fatalf("Covers(%q, %q) = %v", c.pattern, c.hostname, got)
		}
	}
	if store.PatternName("example.com", "@") != "example.com" || store.PatternName("example.com", "*.dev") != "*.dev.example.com" {
		t.Fatal("PatternName")
	}
	// Any depth below the domain is under it, so a domain delete finds it.
	if !store.UnderDomain("example.com", "a.b.example.com") || store.UnderDomain("example.com", "example.com.evil.test") {
		t.Fatal("UnderDomain")
	}
	if p, ok := store.AssignPattern("example.com", "a.b.example.com"); !ok || p != "a.b" {
		t.Fatalf("AssignPattern = %q %v", p, ok)
	}
	if _, ok := store.AssignPattern("example.com", "other.test"); ok {
		t.Fatal("AssignPattern took a name outside the domain")
	}
}

// A route under a single-name assignment works; a sibling name is refused.
func TestRoutesUnderNameAssignment(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	status, env := w.do(c.tok, http.MethodPost, c.org+"/domains", `{"name":"example.com"}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	d := objectPayload(t, env)
	c.dns.set(d["txt_name"].(string), d["txt_value"].(string))
	id := d["id"].(string)
	status, env = w.do(c.tok, http.MethodPost, c.org+"/domains/"+id+"/verify", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")

	project := c.sc.project.ID.String()
	assign := func(pattern string) (int, envelope) {
		return w.do(c.tok, http.MethodPut, c.org+"/domains/"+id+"/assignments",
			`{"assignments":[{"project_id":"`+project+`","pattern":"`+pattern+`"}]}`)
	}
	status, env = assign("marketingos")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got := objectPayload(t, env)["assignments"].([]any)[0].(map[string]any)["pattern"]; got != "marketingos" {
		t.Fatalf("assignment %#v", got)
	}
	status, env = assign("a..b")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgAssignShape)

	createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	plan, _ := c.planAndConfirm(t, c.tok, "web", `[{"hostname":"marketingos.example.com","port":8788}]`, "n1")
	if !strings.Contains(plan, "https://marketingos.example.com/") {
		t.Fatalf("plan %q", plan)
	}
	// console.example.com is under the domain but assigned to nobody: the
	// refusal carries the one name to assign and the ids of the Assign row.
	status, env = w.do(c.tok, http.MethodPut, c.entryPath("web"), `{"routes":[{"hostname":"console.example.com","port":8788}],"idempotency_key":"n2"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "console.example.com is not under a verified domain assigned to this project")
	link := objectPayload(t, env)
	if link["domain"] != "example.com" || link["pattern"] != "console" || link["name"] != "console.example.com" {
		t.Fatalf("assign link %#v", link)
	}
	if link["domain_id"] != id || link["org_id"] != c.sc.project.OrgID.String() {
		t.Fatalf("assign ids %#v", link)
	}
	if _, ok := link["assign_url"]; ok {
		t.Fatal("the API builds no console link")
	}
	// A hostname under no domain of the org has no link to offer.
	status, env = w.do(c.tok, http.MethodPut, c.entryPath("web"), `{"routes":[{"hostname":"app.other.test","port":8788}],"idempotency_key":"n3"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "app.other.test is not under a verified domain assigned to this project")
	if env.Payload != nil {
		t.Fatalf("payload %#v", env.Payload)
	}
}

// The readiness fix names the assignment by ids, and the routes warning
// keeps its text.
func TestReadinessAssignDomainFix(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	id := c.verifiedDomain(t, "example.com")
	createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	c.planAndConfirm(t, c.tok, "web", `[{"hostname":"app.example.com","port":8788}]`, "k1")
	// Narrow the assignment to another name: the route is now unassigned.
	status, env := w.do(c.tok, http.MethodPut, c.org+"/domains/"+id+"/assignments",
		`{"assignments":[{"project_id":"`+c.sc.project.ID.String()+`","pattern":"other"}]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env = w.do(c.tok, http.MethodGet, "/v1/projects/"+c.sc.project.ID.String()+"/catalog/web/readiness", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	checks := objectPayload(t, env)["checks"].([]any)
	for _, raw := range checks {
		check := raw.(map[string]any)
		if check["check"] != "routes" {
			continue
		}
		if check["status"] != "warn" || !strings.Contains(check["message"].(string), "app.example.com is not under a verified domain") {
			t.Fatalf("routes check %#v", check)
		}
		fix := check["fix"].(map[string]any)
		if fix["action"] != "assign_domain" || fix["name"] != "app.example.com" || fix["pattern"] != "app" || fix["domain_id"] != id || fix["org_id"] != c.sc.project.OrgID.String() {
			t.Fatalf("fix %#v", fix)
		}
		return
	}
	t.Fatalf("no routes check in %#v", checks)
}

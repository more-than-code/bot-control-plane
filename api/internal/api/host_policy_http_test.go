package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
)

// api-v1 v1.51: a host's console-managed policy.

var (
	policyBase = strings.Repeat("b", 64)
	policyNew  = strings.Repeat("c", 64)
)

func managedPrograms(hash string, images ...string) map[string]any {
	p := allowPrograms(false)
	p["policy_hash"] = hash
	p["console_policy"] = true
	p["image_patterns"] = images
	p["projects"] = []string{"proj"}
	p["edge"] = map[string]any{"enabled": false, "image": ""}
	return p
}

func newPolicyCast(t *testing.T, programs map[string]any) *hfCast {
	t.Helper()
	c := newHFCast(t, programs)
	c.base = "/v1/orgs/" + c.sc.project.OrgID.String() + "/hosts/" + c.sc.host.ID.String() + "/policy"
	c.setReply(succeed(`{"state":"accepted","new_hash":"` + policyNew + `"}`))
	return c
}

func policyBody(base string, edge bool, images, projects []string) string {
	b, _ := json.Marshal(map[string]any{"base_hash": base, "edge": edge, "images": images, "projects": projects})
	return string(b)
}

func TestHostPolicyGet(t *testing.T) {
	c := newPolicyCast(t, managedPrograms(policyBase, "ghcr.io/acme/*"))
	got := objectPayload(t, c.do(t, c.adminTok, http.MethodGet, "", "", http.StatusOK, "ok"))
	if got["managed"] != true || got["policy_hash"] != policyBase || got["edge"] != false || got["apply"] != nil {
		t.Fatalf("%v", got)
	}
	if imgs, _ := got["images"].([]any); len(imgs) != 1 || imgs[0] != "ghcr.io/acme/*" {
		t.Fatalf("images %v", got["images"])
	}
	// Project admins who are not org admins, and org members, are refused.
	for _, tok := range []string{c.projTok, c.memberTok} {
		if status, _ := c.w.do(tok, http.MethodGet, c.base, ""); status == http.StatusOK {
			t.Fatal("non-admin read the policy")
		}
	}
	// The Host answer never carries the patterns.
	_, env := c.w.do(c.adminTok, http.MethodGet, "/v1/hosts/"+c.sc.host.ID.String(), "")
	if strings.Contains(envJSON(env), "ghcr.io/acme") {
		t.Fatalf("host answer leaks patterns: %s", envJSON(env))
	}
}

func TestHostPolicyNotManaged(t *testing.T) {
	c := newPolicyCast(t, allowPrograms(false))
	got := objectPayload(t, c.do(t, c.adminTok, http.MethodGet, "", "", http.StatusOK, "ok"))
	if got["managed"] != false || got["images"] != nil || got["policy_hash"] != nil {
		t.Fatalf("%v", got)
	}
	c.do(t, c.adminTok, http.MethodPost, "", policyBody(policyBase, true, []string{}, []string{"proj"}), http.StatusConflict, "unsupported_agent")
	if len(c.taskSpecs()) != 0 {
		t.Fatal("sent a task")
	}
	if a := auditsOf(c, audit.ActionHostPolicyApply); len(a) != 1 || a[0].Decision != audit.DecisionDenied {
		t.Fatalf("audit %+v", a)
	}
}

func TestHostPolicyApplyRefusals(t *testing.T) {
	c := newPolicyCast(t, managedPrograms(policyBase, "ghcr.io/acme/*"))
	c.do(t, c.adminTok, http.MethodPost, "", policyBody(policyNew, true, []string{"ghcr.io/acme/*"}, []string{"proj"}), http.StatusConflict, "conflict")
	c.do(t, c.adminTok, http.MethodPost, "", policyBody(policyBase, false, []string{"ghcr.io/acme/*"}, []string{"proj"}), http.StatusBadRequest, "bad_request")
	c.do(t, c.adminTok, http.MethodPost, "", policyBody(policyBase, true, []string{"ghcr.io/Bad"}, []string{"proj"}), http.StatusBadRequest, "bad_request")
	c.do(t, c.adminTok, http.MethodPost, "", `{"edge":true,"images":[],"projects":[]}`, http.StatusBadRequest, "bad_request")
	if len(c.taskSpecs()) != 0 {
		t.Fatal("sent a task")
	}
	c.setReply(func(map[string]any) (map[string]any, time.Duration) {
		return map[string]any{"type": "task_result", "state": "failed", "error_code": "conflict", "payload": map[string]any{}}, 0
	})
	env := c.do(t, c.adminTok, http.MethodPost, "", policyBody(policyBase, true, []string{"ghcr.io/acme/*"}, []string{"proj"}), http.StatusConflict, "conflict")
	if !strings.Contains(env.Msg, "changed") {
		t.Fatalf("%q", env.Msg)
	}
	// A refused task leaves no apply running.
	if got := objectPayload(t, c.do(t, c.adminTok, http.MethodGet, "", "", http.StatusOK, "ok")); got["apply"] != nil {
		t.Fatalf("%v", got["apply"])
	}
}

func TestHostPolicyApplyThenHello(t *testing.T) {
	c := newPolicyCast(t, managedPrograms(policyBase, "ghcr.io/acme/*"))
	body := policyBody(policyBase, true, []string{"ghcr.io/acme/*", "ghcr.io/kea-studio/marketing-os-prototype"}, []string{"proj"})
	got := objectPayload(t, c.do(t, c.ownerTok, http.MethodPost, "", body, http.StatusAccepted, "ok"))
	if got["state"] != "running" {
		t.Fatalf("%v", got)
	}
	change := got["change"].(map[string]any)
	if change["edge_to"] != true || len(change["images_added"].([]any)) != 1 {
		t.Fatalf("change %v", change)
	}
	specs := c.taskSpecs()
	if len(specs) != 1 || specs[0]["base_hash"] != policyBase || specs[0]["edge"] != true {
		t.Fatalf("specs %v", specs)
	}
	// A second apply while one runs is refused.
	c.do(t, c.ownerTok, http.MethodPost, "", body, http.StatusConflict, "conflict")
	// The restarted agent says hello with the new hash.
	c.connect(t, managedPrograms(policyNew, "ghcr.io/acme/*", "ghcr.io/kea-studio/marketing-os-prototype"))
	got = objectPayload(t, c.do(t, c.ownerTok, http.MethodGet, "", "", http.StatusOK, "ok"))
	apply := got["apply"].(map[string]any)
	if apply["state"] != "applied" || got["policy_hash"] != policyNew || got["edge"] != false {
		t.Fatalf("%v", got)
	}
	res := auditsOf(c, audit.ActionHostPolicyResult)
	if len(res) != 1 || !strings.Contains(string(res[0].Detail), `"applied"`) {
		t.Fatalf("result audit %+v", res)
	}
	if a := auditsOf(c, audit.ActionHostPolicyApply); len(a) != 1 || !strings.Contains(string(a[0].Detail), "marketing-os-prototype") {
		t.Fatalf("apply audit %+v", a)
	}
}

// No hello with the new hash within the settle window, and the host is on
// the old hash: rolled back.
func TestHostPolicyRolledBack(t *testing.T) {
	c := newPolicyCast(t, managedPrograms(policyBase))
	c.do(t, c.ownerTok, http.MethodPost, "", policyBody(policyBase, true, []string{}, []string{"proj"}), http.StatusAccepted, "ok")
	if got := objectPayload(t, c.do(t, c.ownerTok, http.MethodGet, "", "", http.StatusOK, "ok")); got["apply"].(map[string]any)["state"] != "running" {
		t.Fatalf("%v", got)
	}
	later := c.w.now.Add(hostPolicySettle + time.Second)
	c.w.srv.Now = func() time.Time { return later }
	got := objectPayload(t, c.do(t, c.ownerTok, http.MethodGet, "", "", http.StatusOK, "ok"))
	if got["apply"].(map[string]any)["state"] != "rolled_back" {
		t.Fatalf("%v", got)
	}
}

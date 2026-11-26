package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

const imageHCReason = "this host's agent must be upgraded to use on_unhealthy with the image's own health check"

func imageHCPrograms(flag bool) *store.HostPrograms {
	return &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Images: true, Docker: true, PolicyHash: "abc", HealthPolicy: true, Healthcheck: true, ImageHealthcheckPolicy: flag}
}

// TestImageHealthcheckPolicyHTTP covers api-v1 v1.63: on_unhealthy without
// the entry's own healthcheck is accepted on create and update, and deploy
// and rollback (console and confirm) to an agent without
// programs.image_healthcheck_policy are refused 409 before any task. An entry
// with its own healthcheck, or with on_unhealthy none, is not gated.
func TestImageHealthcheckPolicyHTTP(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()

	img := createImage(t, w, tok, sc.project.ID, "console", `{"kind":"service","image":"nginx","health_policy":{"on_unhealthy":"restart"}}`)
	if spec, _ := img["spec"].(map[string]any); spec["healthcheck"] != nil {
		t.Fatalf("stored spec %#v", img["spec"])
	}
	createImage(t, w, tok, sc.project.ID, "own", `{"kind":"service","image":"redis",`+policyHealthcheck+`,"health_policy":{"on_unhealthy":"restart"}}`)
	createImage(t, w, tok, sc.project.ID, "plain", `{"kind":"service","image":"nginx"}`)
	status, env, _ := mustCall(t, w, tok, http.MethodPut, "/v1/projects/"+project+"/catalog/plain", `{"kind":"image","spec":{"kind":"service","image":"nginx","health_policy":{"on_unhealthy":"rollback"}}}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = mustCall(t, w, tok, http.MethodPut, "/v1/projects/"+project+"/catalog/plain", `{"kind":"image","spec":{"kind":"service","image":"nginx"}}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")

	programs := allowImages()
	programs["health_policy"] = true
	programs["healthcheck"] = true
	programs["image_healthcheck_policy"] = true
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, programs, nil)
	log := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return imageDeployReply(msg, shaDigest(2))
	})
	// The hello key is accepted (not read as a bad programs object).
	stored, err := w.mem.GetHost(t.Context(), sc.host.ID)
	if err != nil || stored.Programs == nil || !stored.Programs.Images || !stored.Programs.ImageHealthcheckPolicy {
		t.Fatalf("hello programs %#v %v", stored.Programs, err)
	}
	setPrograms(t, w, sc.host.ID, imageHCPrograms(false))

	status, env, _ = postImage(t, w, tok, project, "console", "deploy", host, "c-1", nil)
	mustHTTP(t, status, env, http.StatusConflict, "unsupported_agent", imageHCReason)
	status, env, _ = postImage(t, w, tok, project, "console", "rollback", host, "c-2", map[string]any{"digest": shaDigest(2)})
	mustHTTP(t, status, env, http.StatusConflict, "unsupported_agent", imageHCReason)
	if log.count() != 0 {
		t.Fatal("refused action sent a task")
	}
	for _, name := range []string{"own", "plain"} {
		status, env, _ = postImage(t, w, tok, project, name, "deploy", host, name+"-1", nil)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
	if log.count() != 2 {
		t.Fatalf("ungated deploys sent %d tasks", log.count())
	}

	// Delegated: the plan is issued; confirm re-checks the gate.
	setPrograms(t, w, sc.host.ID, imageHCPrograms(true))
	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	status, env, _ = postImage(t, w, delegated, project, "console", "deploy", host, "c-del", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	setPrograms(t, w, sc.host.ID, imageHCPrograms(false))
	before := log.count()
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("c-del"))
	mustHTTP(t, status, env, http.StatusConflict, "unsupported_agent", imageHCReason)
	if log.count() != before {
		t.Fatal("refused confirm sent a task")
	}

	// With the flag the deploy goes out, with the policy and no healthcheck.
	setPrograms(t, w, sc.host.ID, imageHCPrograms(true))
	status, env, _ = postImage(t, w, tok, project, "console", "deploy", host, "c-3", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	spec, _ := lastSpec(t, log)["spec"].(map[string]any)
	if _, ok := spec["healthcheck"]; ok {
		t.Fatalf("task spec has a healthcheck %#v", spec)
	}
	if policy, _ := spec["health_policy"].(map[string]any); policy["on_unhealthy"] != "restart" {
		t.Fatalf("task health_policy %#v", spec["health_policy"])
	}

	// The Host answer never carries the flag.
	_, _, raw := mustCall(t, w, tok, http.MethodGet, "/v1/hosts/"+host, "")
	if strings.Contains(raw, "image_healthcheck_policy") {
		t.Fatalf("host answer shows image_healthcheck_policy: %s", raw)
	}
}

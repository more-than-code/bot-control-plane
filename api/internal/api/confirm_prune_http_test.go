package api

import (
	"net/http"
	"testing"
	"time"
)

// spec-release-v0239 F4: a new plan in the same table must not prune a plan
// that has only just expired, so confirming that plan still answers the
// expired conflict (api-v1 v1.32 item 1), not not_found.

var (
	pruneT0   = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	pruneLate = time.Date(2026, 10, 13, 12, 6, 0, 0, time.UTC)
)

const msgConfirmExpired = "this confirmation expired; call the tool again for a new one"

func TestExpiredCatalogPlanSurvivesNewPlan(t *testing.T) {
	c := newCatalogCast(t)
	status, env, _ := mustCall(t, c.w, c.tok, http.MethodPost, catalogCollection(c.sc.project.ID), catalogImageBody("first", nginxSpec, "a-1"))
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	first, _, _ := healthPlanOf(t, env)

	c.w.srv.Now = func() time.Time { return pruneLate }
	status, env, _ = mustCall(t, c.w, c.tok, http.MethodPost, catalogCollection(c.sc.project.ID), catalogImageBody("second", nginxSpec, "b-1"))
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")

	status, env = c.w.do(c.tok, http.MethodPost, "/v1/confirmations/"+first, confirmBody("a-1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", msgConfirmExpired)
}

func TestExpiredEnvSetPlanSurvivesNewPlan(t *testing.T) {
	c := newCatalogCast(t)
	c.w.srv.Secrets = testBox(t)
	project := c.sc.project.ID.String()
	status, env, _ := mustCall(t, c.w, c.tok, http.MethodPost, envSetsPath(project), `{"name":"one","keys":[{"key":"A"}],"idempotency_key":"a-1"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	first, _ := objectPayload(t, env)["confirmation_id"].(string)

	c.w.srv.Now = func() time.Time { return pruneLate }
	status, env, _ = mustCall(t, c.w, c.tok, http.MethodPost, envSetsPath(project), `{"name":"two","keys":[{"key":"B"}],"idempotency_key":"b-1"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")

	status, env = c.w.do(c.tok, http.MethodPost, "/v1/confirmations/"+first, confirmBody("a-1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", msgConfirmExpired)
}

func TestExpiredEdgePlanSurvivesNewPlan(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	w.srv.Now = func() time.Time { return pruneT0 }
	c.verifiedDomain(t, "example.com")
	createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	status, env := w.do(c.tok, http.MethodPut, c.entryPath("web"), `{"routes":[{"hostname":"app.example.com","port":8788}],"idempotency_key":"a-1"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	first, _ := objectPayload(t, env)["confirmation_id"].(string)

	w.srv.Now = func() time.Time { return pruneLate }
	status, env = w.do(c.tok, http.MethodPut, c.entryPath("web"), `{"routes":[{"hostname":"www.example.com","port":8788}],"idempotency_key":"b-1"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")

	status, env = w.do(c.tok, http.MethodPost, "/v1/confirmations/"+first, confirmBody("a-1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", msgConfirmExpired)
}

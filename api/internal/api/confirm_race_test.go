package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// raceStore stages the loser of a double confirm (api-v1 v1.32 item 1): each
// commit first runs once for a "winner", then again for the request under
// test, which therefore passed the pre-check but finds the plan used under
// the lock.
type raceStore struct {
	store.Store
}

func (s raceStore) CommitCatalogWrite(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time, in store.CatalogWrite) (string, store.CatalogEntry, error) {
	if _, _, err := s.Store.CommitCatalogWrite(ctx, hash, userID, at, in); err != nil {
		return "", store.CatalogEntry{}, err
	}
	return s.Store.CommitCatalogWrite(ctx, hash, userID, at, in)
}

func (s raceStore) CreateEnvSetNames(ctx context.Context, projectID uuid.UUID, name string, keys []store.EnvKeyCreate, confirm []byte, actor uuid.UUID, at time.Time) (store.EnvSet, error) {
	if confirm == nil {
		return s.Store.CreateEnvSetNames(ctx, projectID, name, keys, confirm, actor, at)
	}
	if _, err := s.Store.CreateEnvSetNames(ctx, projectID, name, keys, confirm, actor, at); err != nil {
		return store.EnvSet{}, err
	}
	return s.Store.CreateEnvSetNames(ctx, projectID, name, keys, confirm, actor, at)
}

const usedText = "this confirmation was already used"

// TestConfirmRaceLoserCatalogWrite: the commit refuses a plan another
// confirm just used; the answer is the used text, not not_found.
func TestConfirmRaceLoserCatalogWrite(t *testing.T) {
	c := newCatalogCast(t)
	status, env := c.w.do(c.tok, http.MethodPost, catalogCollection(c.sc.project.ID), catalogImageBody("web", nginxSpec, "race-1"))
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	c.w.srv.Store = raceStore{c.w.srv.Store}
	status, env = c.w.do(c.tok, http.MethodPost, "/v1/confirmations/"+id, confirmBody("race-1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", usedText)
	// Sequentially, a second confirm says the same from the pre-check.
	status, env = c.w.do(c.tok, http.MethodPost, "/v1/confirmations/"+id, confirmBody("race-1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", usedText)
	// Another user's attempt stays not_found.
	other := tokenKind(t, c.w, c.extra.ID, store.KindDelegated)
	status, env = c.w.do(other, http.MethodPost, "/v1/confirmations/"+id, confirmBody("race-1"))
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
}

// TestConfirmRaceLoserEnvSetCreate: the same for an env set create plan.
func TestConfirmRaceLoserEnvSetCreate(t *testing.T) {
	w, sc, _, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	status, env, _ := mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), `{"name":"race","keys":[{"key":"A","is_secret":false}],"idempotency_key":"race-2"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	w.srv.Store = raceStore{w.srv.Store}
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("race-2"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", usedText)
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("race-2"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", usedText)
}

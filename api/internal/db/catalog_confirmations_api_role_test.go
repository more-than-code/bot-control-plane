package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestReopenConfirmationAsAPIRole runs the confirmation writes as
// deploybot_api, the role the server uses. ReopenConfirmation sets
// expires_at, which needs UPDATE (expires_at) (migration 0040); with only
// 0007's UPDATE (used_at) every reopen after a host refusal was denied and
// the plan stayed used (api-v1 v1.55 item 2).
func TestReopenConfirmationAsAPIRole(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	apiURL := os.Getenv("DEPLOYBOT_TEST_API_DATABASE_URL")
	if apiURL == "" {
		t.Fatal("DEPLOYBOT_TEST_API_DATABASE_URL is not set: give the deploybot_api login on the same test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer d.Close()
	if !onTestDatabase(ctx, d) {
		t.Fatal("refusing a database that is not deploybot_test or deploybot_test_*")
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api, err := Open(ctx, apiURL)
	if err != nil {
		t.Fatal("api login connect failed")
	}
	defer api.Close()
	var role string
	if err := api.pool.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil || role != "deploybot_api" {
		t.Fatalf("api login is %q (%v), want deploybot_api", role, err)
	}

	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "reopen-api-"+tag, at)
	proj := catalogProject(t, d, ctx, owner.ID, "Reopen", "reopen-"+tag, at)
	host := enrollHostDB(t, d, ctx, proj.ID, owner.ID, "reopen-"+tag, at)
	spec, hash, display := catalogSpec(t, "pnpm run dev")
	entry, err := d.CreateCatalogEntry(ctx, proj.ID, store.CatalogWrite{
		Name: "webapp", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: at,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, tokenHash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	req := bytes32(4)
	conf := store.CatalogConfirmation{
		UserID: owner.ID, ProjectID: proj.ID, EntryID: entry.ID, EntryName: "webapp", HostID: host.ID,
		Action: "deploy", SpecHash: hash, IdempotencyKey: "reopen-key-" + tag, RequestHash: req[:],
		CreatedAt: at, ExpiresAt: at.Add(store.ConfirmationTTL),
	}
	if err := api.PutConfirmation(ctx, tokenHash, conf); err != nil {
		t.Fatalf("put as deploybot_api: %v", err)
	}
	if err := api.MarkConfirmationUsed(ctx, tokenHash, owner.ID, at.Add(time.Minute)); err != nil {
		t.Fatalf("mark used as deploybot_api: %v", err)
	}
	until := at.Add(time.Hour)
	exp, err := api.ReopenConfirmation(ctx, tokenHash, owner.ID, at.Add(2*time.Minute), until)
	if err != nil {
		t.Fatalf("reopen as deploybot_api: %v", err)
	}
	if !exp.Equal(until) {
		t.Fatalf("reopened until %v, want %v", exp, until)
	}
	row, err := d.GetConfirmation(ctx, tokenHash)
	if err != nil || row.UsedAt != nil || !row.ExpiresAt.Equal(until) {
		t.Fatalf("reopened row %+v %v", row, err)
	}
	if err := api.MarkConfirmationUsed(ctx, tokenHash, owner.ID, at.Add(3*time.Minute)); err != nil {
		t.Fatalf("use after reopen as deploybot_api: %v", err)
	}
}

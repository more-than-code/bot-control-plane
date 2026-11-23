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

// TestHostUpdateNoticesAsAPIRole runs NoticeHostUpdates as deploybot_api,
// the role the server uses. Its SELECT FOR UPDATE needs the UPDATE
// privilege (migration 0037); with only 0035's grants every notifications
// list failed with permission denied in production (v0.2.46).
func TestHostUpdateNoticesAsAPIRole(t *testing.T) {
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
	owner := catalogUser(t, d, ctx, "hun-api-"+tag, at)
	org := personalOrg(t, d, ctx, owner.ID)
	// A project in the org, so the 0015 down script in TestOrgsMigration
	// finds an owner for this host.
	if _, err := d.CreateProject(ctx, org, owner.ID, "hun-api", "hun-api-"+tag, at); err != nil {
		t.Fatal(err)
	}
	_, hash, _ := secret.NewToken()
	if _, err := d.CreateJoinToken(ctx, org, hash, nil, nil, owner.ID, at.Add(time.Hour), at); err != nil {
		t.Fatal(err)
	}
	_, cred, _ := secret.NewToken()
	host, err := d.ConsumeJoinToken(ctx, hash, store.EnrollInput{Name: "hun-api-" + tag, NodeType: "pi", OS: "linux", Arch: "arm64", AlwaysOn: true, AgentVersion: "0.2.8"}, uuid.New(), cred, at)
	if err != nil {
		t.Fatal(err)
	}

	// First notice for 0.3.0, then a newer version deletes it: both paths
	// take the FOR UPDATE lock as deploybot_api.
	got, err := api.NoticeHostUpdates(ctx, []uuid.UUID{host.ID}, "0.3.0", at)
	if err != nil {
		t.Fatalf("notice as deploybot_api: %v", err)
	}
	if !got[host.ID].Equal(at) {
		t.Fatalf("noticed %v, want %v", got[host.ID], at)
	}
	later := at.Add(time.Minute)
	got, err = api.NoticeHostUpdates(ctx, []uuid.UUID{host.ID}, "0.3.1", later)
	if err != nil {
		t.Fatalf("newer notice as deploybot_api: %v", err)
	}
	if !got[host.ID].Equal(later) {
		t.Fatalf("newer noticed %v, want %v", got[host.ID], later)
	}
	var old int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM app.host_update_notices WHERE host_id = $1::uuid AND version = '0.3.0'`, host.ID.String()).Scan(&old); err != nil || old != 0 {
		t.Fatalf("old notice rows %d (%v), want 0", old, err)
	}
}

package db

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// releaseTestVersion is a version no other run uses: the table is global,
// and the shared test database keeps rows between runs.
func releaseTestVersion() string {
	return fmt.Sprintf("9.%d.%d", rand.IntN(10000), rand.IntN(10000))
}

// TestParityReleaseServed: the first record of a version keeps its time;
// unknown versions are absent (migration 0038, api-v1 v1.57).
func TestParityReleaseServed(t *testing.T) {
	runParity(t, func(p parity) {
		a, b := releaseTestVersion(), releaseTestVersion()
		if err := p.st.RecordReleaseServed(p.ctx, a, p.at); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.RecordReleaseServed(p.ctx, a, p.at.Add(time.Hour)); err != nil {
			p.t.Fatal(err)
		}
		got, err := p.st.ReleasesServed(p.ctx, []string{a, b})
		if err != nil || len(got) != 1 || !got[a].Equal(p.at) {
			p.t.Fatalf("served %v %v", got, err)
		}
		for _, bad := range []string{"", "unknown", "0.2", "0.2.47-rc1", "12345.0.0"} {
			if err := p.st.RecordReleaseServed(p.ctx, bad, p.at); !errors.Is(err, store.ErrInvalid) {
				p.t.Fatalf("%q: %v", bad, err)
			}
		}
		if got, err := p.st.ReleasesServed(p.ctx, nil); err != nil || len(got) != 0 {
			p.t.Fatalf("none %v %v", got, err)
		}
	})
}

// TestReleaseServedAsAPIRole runs both calls as deploybot_api, the role the
// server uses, with only the 0038 grants (AGENTS.md: a new table needs a
// test as the API role).
func TestReleaseServedAsAPIRole(t *testing.T) {
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
	v := releaseTestVersion()
	at := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 2; i++ {
		if err := api.RecordReleaseServed(ctx, v, at.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("record as deploybot_api: %v", err)
		}
	}
	got, err := api.ReleasesServed(ctx, []string{v})
	if err != nil || !got[v].Equal(at) {
		t.Fatalf("read as deploybot_api %v %v", got, err)
	}
}

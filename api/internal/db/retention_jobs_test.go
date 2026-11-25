package db

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRetentionJobsAsAPIRole reads the 9 retention policies of migration
// 0002 through TimescaleDB's views as deploybot_api, the role the server
// uses, with no grant beyond the migrations (api-v1 v1.58).
func TestRetentionJobsAsAPIRole(t *testing.T) {
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
	jobs, err := api.RetentionJobs(ctx)
	if err != nil {
		t.Fatalf("read as deploybot_api: %v", err)
	}
	// Sorted by hypertable name (the query orders them).
	want := []string{"health_results", "host_cpu", "host_disk", "host_diskio", "host_mem", "host_net", "host_system", "host_temp", "process_samples"}
	if len(jobs) != len(want) {
		t.Fatalf("got %d jobs: %+v", len(jobs), jobs)
	}
	for i, j := range jobs {
		if j.Hypertable != want[i] || j.JobID < 1000 || j.TotalRuns < 0 || j.TotalFailures < 0 {
			t.Fatalf("%d: %+v", i, j)
		}
		// NULL and +/-infinity read as nil; anything else is a finite UTC time.
		for _, at := range []*time.Time{j.LastSuccess, j.NextStart} {
			if at != nil && (at.Location() != time.UTC || at.Year() < 2000 || at.Year() > 3000) {
				t.Fatalf("%d: time %v", i, *at)
			}
		}
	}
}

package db

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestHostResourceStatsPerf is acceptance 7 of spec-host-resource-alerts:
// with 7 days of 10-second samples for 10 hosts (mem, two disk mounts,
// cpu-total, system), the sweep's four grouped reads together take under
// 50 ms. Opt in with BOT_TEST_PERF=1 and the test database; it inserts
// about 3 million rows under fresh host ids and deletes them afterwards.
func TestHostResourceStatsPerf(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" || os.Getenv("BOT_TEST_PERF") != "1" {
		t.Skip("set BOT_TEST_PERF=1 and DEPLOYBOT_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	// Cleanups run last first: the row cleanup below runs before this Close.
	t.Cleanup(d.Close)
	if !onTestDatabase(ctx, d) {
		t.Fatal("refusing a database that is not deploybot_test or deploybot_test_*")
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ids := make([]uuid.UUID, 10)
	strs := make([]string, 10)
	for i := range ids {
		ids[i] = uuid.New()
		strs[i] = ids[i].String()
	}
	now := time.Now().UTC().Truncate(time.Second)
	from := now.Add(-7 * 24 * time.Hour)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer ccancel()
		for _, table := range []string{"host_mem", "host_disk", "host_cpu", "host_system"} {
			if _, err := d.pool.Exec(cctx, `DELETE FROM metrics.`+table+` WHERE host_id = ANY($1::uuid[])`, strs); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	series := `FROM unnest($1::uuid[]) AS h(id), generate_series($2::timestamptz, $3::timestamptz, interval '10 seconds') AS g(ts)`
	inserts := []string{
		`INSERT INTO metrics.host_mem (time, host_id, total, available, used, used_percent) SELECT ts, id, 1073741824, 536870912, 536870912, 50 ` + series,
		`INSERT INTO metrics.host_disk (time, host_id, path, fstype, total, used, free, used_percent) SELECT ts, id, p, 'ext4', 100, 50, 50, 50 ` + series + `, unnest(ARRAY['/', '/data']) AS m(p)`,
		`INSERT INTO metrics.host_cpu (time, host_id, cpu, usage_user, usage_system, usage_idle, usage_iowait) SELECT ts, id, 'cpu-total', 10, 5, 80, 5 ` + series,
		`INSERT INTO metrics.host_system (time, host_id, load1, load5, load15, uptime, n_cpus) SELECT ts, id, 0.5, 0.5, 0.5, 1000, 4 ` + series,
	}
	start := time.Now()
	for _, q := range inserts {
		if _, err := d.pool.Exec(ctx, q, strs, from, now); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	t.Logf("inserted 7 days for 10 hosts in %s", time.Since(start).Round(time.Second))
	if _, err := d.pool.Exec(ctx, `ANALYZE metrics.host_mem`); err != nil {
		t.Logf("analyze skipped: %s", strings.SplitN(err.Error(), "\n", 2)[0])
	}
	run := func() time.Duration {
		begin := time.Now()
		for _, m := range store.ResourceMeasurements {
			stats, err := d.HostResourceStats(ctx, m, ids, now)
			if err != nil {
				t.Fatalf("%s: %v", m, err)
			}
			want := 10
			if m == store.ResourceDisk {
				want = 20
			}
			if len(stats) != want {
				t.Fatalf("%s rows %d", m, len(stats))
			}
		}
		return time.Since(begin)
	}
	// Wall time from a test machine includes the network to the database
	// (from a laptop on Wi-Fi to pi4 a bare SELECT 1 varies from 5 to 250
	// ms), so the target is checked on the server's own time: planning plus
	// execution of the four reads, from EXPLAIN ANALYZE, median of 20 rounds.
	// The wall time is logged for reference.
	cold := run()
	walls := make([]time.Duration, 20)
	servers := make([]float64, 20)
	for i := range walls {
		walls[i] = run()
		for _, m := range store.ResourceMeasurements {
			servers[i] += explainMS(t, ctx, d, m, ids, now)
		}
	}
	sort.Slice(walls, func(i, j int) bool { return walls[i] < walls[j] })
	sort.Float64s(servers)
	median := servers[len(servers)/2]
	t.Logf("four grouped reads, server time (planning + execution): min %.1f ms, median %.1f ms, max %.1f ms", servers[0], median, servers[len(servers)-1])
	t.Logf("four grouped reads, wall time from this machine: first %s; next 20: min %s, median %s, max %s", cold, walls[0], walls[len(walls)/2], walls[len(walls)-1])
	if median > 50 {
		t.Errorf("four grouped reads took %.1f ms on the server (median), over 50 ms", median)
	}
}

// explainMS is the planning plus execution time of one HostResourceStats
// read as the server reports it.
func explainMS(t *testing.T, ctx context.Context, d *DB, measurement string, ids []uuid.UUID, now time.Time) float64 {
	t.Helper()
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = id.String()
	}
	var raw []byte
	err := d.pool.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+resourceStatsSQL(resourceSpecs[measurement]), strs, now,
		now.Add(-store.ResourceWindowShort), now.Add(-store.ResourceWindowMedium), now.Add(-store.ResourceWindowLong)).Scan(&raw)
	if err != nil {
		t.Fatalf("explain %s: %v", measurement, err)
	}
	var plans []struct {
		Planning  float64 `json:"Planning Time"`
		Execution float64 `json:"Execution Time"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("explain %s: %v", measurement, err)
	}
	return plans[0].Planning + plans[0].Execution
}

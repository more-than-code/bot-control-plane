package db

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

const hostAlertsMigration = "0033_host_alerts.sql"

func intp(v int) *int { return &v }

// TestParityHostAlertSettings is spec-host-resource-alerts §8: defaults
// without a row, a write and read back, back to null, the range check, and
// the list used by the sweep.
func TestParityHostAlertSettings(t *testing.T) {
	runParity(t, func(p parity) {
		t := p.t
		owner := p.user("ha")
		org := p.org(owner.ID)
		host := p.enroll(org, nil, owner.ID, "ha")
		got, err := p.st.GetHostAlertSettings(p.ctx, host.ID)
		if err != nil || !got.AlertsEnabled || got.MemMinAvailablePercent != nil || got.DiskMaxUsedPercent != nil || got.CPUMaxBusyPercent != nil {
			t.Fatalf("defaults %+v %v", got, err)
		}
		in := store.HostAlertSettings{AlertsEnabled: false, MemMinAvailablePercent: intp(10), DiskMaxUsedPercent: intp(95), CPUMaxBusyPercent: intp(100)}
		if err := p.st.PutHostAlertSettings(p.ctx, host.ID, in, owner.ID, p.at); err != nil {
			t.Fatal(err)
		}
		got, err = p.st.GetHostAlertSettings(p.ctx, host.ID)
		if err != nil || got.AlertsEnabled || *got.MemMinAvailablePercent != 10 || *got.DiskMaxUsedPercent != 95 || *got.CPUMaxBusyPercent != 100 {
			t.Fatalf("stored %+v %v", got, err)
		}
		in = store.HostAlertSettings{AlertsEnabled: true, DiskMaxUsedPercent: intp(50)}
		if err := p.st.PutHostAlertSettings(p.ctx, host.ID, in, owner.ID, p.at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		got, _ = p.st.GetHostAlertSettings(p.ctx, host.ID)
		if !got.AlertsEnabled || got.MemMinAvailablePercent != nil || *got.DiskMaxUsedPercent != 50 || got.CPUMaxBusyPercent != nil {
			t.Fatalf("replaced %+v", got)
		}
		for _, bad := range []store.HostAlertSettings{
			{AlertsEnabled: true, MemMinAvailablePercent: intp(4)},
			{AlertsEnabled: true, MemMinAvailablePercent: intp(51)},
			{AlertsEnabled: true, DiskMaxUsedPercent: intp(49)},
			{AlertsEnabled: true, DiskMaxUsedPercent: intp(100)},
			{AlertsEnabled: true, CPUMaxBusyPercent: intp(49)},
			{AlertsEnabled: true, CPUMaxBusyPercent: intp(101)},
		} {
			if err := p.st.PutHostAlertSettings(p.ctx, host.ID, bad, owner.ID, p.at); !errors.Is(err, store.ErrInvalid) {
				t.Fatalf("out of range %+v: %v", bad, err)
			}
		}
		all, err := p.st.ListHostAlertSettings(p.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := all[host.ID]; !ok || *s.DiskMaxUsedPercent != 50 {
			t.Fatalf("list %+v", all[host.ID])
		}
		other := p.enroll(org, nil, owner.ID, "hb")
		if _, ok := all[other.ID]; ok {
			t.Fatal("a host without a row is listed")
		}
	})
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// TestParityHostResourceStats is the grouped window read (§8): per host and
// mount, the newest value, and count, average, minimum, maximum (and the
// memory total) in the 5, 10 and 15 minute windows; samples older than 15
// minutes, in the future, of another cpu or with no cores do not count.
func TestParityHostResourceStats(t *testing.T) {
	runParity(t, func(p parity) {
		t := p.t
		owner := p.user("rs")
		org := p.org(owner.ID)
		h1 := p.enroll(org, nil, owner.ID, "rs1")
		h2 := p.enroll(org, nil, owner.ID, "rs2")
		now := p.at
		const mib = 1 << 20
		var samples []store.Sample
		// k = 0..89: one sample every 10 s back to now-890s.
		for k := 0; k < 90; k++ {
			at := now.Add(-time.Duration(k) * 10 * time.Second)
			avail := int64(100 * mib)
			if k >= 30 {
				avail = 50 * mib
			}
			samples = append(samples,
				store.Sample{M: "mem", T: at, Ints: map[string]int64{"total": 1024 * mib, "available": avail, "used": 1}, Floats: map[string]float64{"used_percent": 1}},
				store.Sample{M: "disk", T: at, Tags: map[string]string{"path": "/", "fstype": "ext4"}, Floats: map[string]float64{"used_percent": 90 + float64(k%3)}},
				store.Sample{M: "cpu", T: at, Tags: map[string]string{"cpu": "cpu-total"}, Floats: map[string]float64{"usage_idle": 20}},
				store.Sample{M: "cpu", T: at, Tags: map[string]string{"cpu": "cpu0"}, Floats: map[string]float64{"usage_idle": 99}},
				store.Sample{M: "system", T: at, Floats: map[string]float64{"load5": 2}, Ints: map[string]int64{"n_cpus": 4}},
			)
			if k < 10 {
				samples = append(samples, store.Sample{M: "disk", T: at, Tags: map[string]string{"path": "/data", "fstype": "xfs"}, Floats: map[string]float64{"used_percent": 40}})
			}
		}
		// Not counted: too old, in the future, no cores.
		samples = append(samples,
			store.Sample{M: "mem", T: now.Add(-16 * time.Minute), Ints: map[string]int64{"total": 1024 * mib, "available": 1}},
			store.Sample{M: "mem", T: now.Add(time.Minute), Ints: map[string]int64{"total": 1024 * mib, "available": 1}},
			store.Sample{M: "system", T: now.Add(-time.Second), Floats: map[string]float64{"load5": 9}, Ints: map[string]int64{"n_cpus": 0}},
		)
		if _, err := p.st.InsertBatch(p.ctx, h1.ID, uuid.New(), samples, now); err != nil {
			t.Fatal(err)
		}
		ids := []uuid.UUID{h1.ID, h2.ID}

		mem, err := p.st.HostResourceStats(p.ctx, store.ResourceMem, ids, now)
		if err != nil || len(mem) != 1 {
			t.Fatalf("mem %+v %v", mem, err)
		}
		m := mem[0]
		if m.HostID != h1.ID || m.Path != "" || !m.LastAt.Equal(now) || !near(m.Last, 100*mib) ||
			m.Short.Count != 30 || !near(m.Short.Avg, 100*mib) || !near(m.Short.AvgTotal, 1024*mib) ||
			m.Medium.Count != 60 || !near(m.Medium.Avg, 75*mib) || !near(m.Medium.Min, 50*mib) || !near(m.Medium.Max, 100*mib) ||
			m.Long.Count != 90 || !near(m.Long.Avg, (30*100+60*50)*mib/90.0) {
			t.Fatalf("mem stat %+v", m)
		}
		disk, err := p.st.HostResourceStats(p.ctx, store.ResourceDisk, ids, now)
		if err != nil || len(disk) != 2 || disk[0].Path != "/" || disk[1].Path != "/data" {
			t.Fatalf("disk %+v %v", disk, err)
		}
		if d := disk[0]; d.Medium.Count != 60 || !near(d.Medium.Min, 90) || !near(d.Medium.Max, 92) || !near(d.Last, 90) {
			t.Fatalf("disk / %+v", d)
		}
		if d := disk[1]; d.Short.Count != 10 || d.Medium.Count != 10 || !near(d.Last, 40) {
			t.Fatalf("disk /data %+v", d)
		}
		cpu, err := p.st.HostResourceStats(p.ctx, store.ResourceCPU, ids, now)
		if err != nil || len(cpu) != 1 || cpu[0].Long.Count != 90 || !near(cpu[0].Long.Avg, 80) {
			t.Fatalf("cpu %+v %v", cpu, err)
		}
		load, err := p.st.HostResourceStats(p.ctx, store.ResourceLoad, ids, now)
		if err != nil || len(load) != 1 || load[0].Long.Count != 90 || !near(load[0].Long.Avg, 0.5) {
			t.Fatalf("load %+v %v", load, err)
		}
		if _, err := p.st.HostResourceStats(p.ctx, "swap", ids, now); !errors.Is(err, store.ErrUnknownMeasurement) {
			t.Fatalf("unknown %v", err)
		}
		if got, err := p.st.HostResourceStats(p.ctx, store.ResourceMem, nil, now); err != nil || len(got) != 0 {
			t.Fatalf("no hosts %+v %v", got, err)
		}
		disks, err := p.st.ListHostDisks(p.ctx, h1.ID, now.Add(-24*time.Hour))
		if err != nil || len(disks) != 2 || disks[0] != (store.HostDisk{Path: "/", Fstype: "ext4"}) || disks[1] != (store.HostDisk{Path: "/data", Fstype: "xfs"}) {
			t.Fatalf("disks %+v %v", disks, err)
		}
		// The disk chart of one mount; an unknown mount is an empty series.
		series, err := p.st.QueryChart(p.ctx, h1.ID, "disk", "/data", now.Add(-10*time.Minute), now.Add(time.Second), 600)
		if err != nil || len(series) != 1 || !hasValue(series[0], 40) {
			t.Fatalf("disk /data chart %+v %v", series, err)
		}
		series, err = p.st.QueryChart(p.ctx, h1.ID, "disk", "/nope", now.Add(-10*time.Minute), now.Add(time.Second), 600)
		if err != nil || len(series) != 1 || hasAny(series[0]) {
			t.Fatalf("unknown mount chart %+v %v", series, err)
		}
		series, err = p.st.QueryChart(p.ctx, h1.ID, "mem", "", now.Add(-time.Minute), now.Add(time.Second), 600)
		if err != nil || len(series) != 4 || series[3].Name != "available" || series[3].Unit != "bytes" || !hasValue(series[3], 100*mib) {
			t.Fatalf("mem chart %+v %v", series, err)
		}
	})
}

func hasAny(s store.ChartSeries) bool {
	for _, pt := range s.Points {
		if pt.V != nil {
			return true
		}
	}
	return false
}

func hasValue(s store.ChartSeries, v float64) bool {
	for _, pt := range s.Points {
		if pt.V != nil && near(*pt.V, v) {
			return true
		}
	}
	return false
}

// TestHostAlertsMigrationText: 0033 adds the table, widens the kind check
// with every earlier kind plus host_resource and grants no DELETE; the down
// script deletes host_resource rows, restores the 0032 check and drops the
// table.
func TestHostAlertsMigrationText(t *testing.T) {
	if !numberedSQL(hostAlertsMigration) || numberedSQL("0033_host_alerts.down.sql") {
		t.Fatal("0033 numbering")
	}
	body, err := migrations.FS.ReadFile(hostAlertsMigration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("0033_host_alerts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{string(body), string(down)} {
		for _, line := range strings.Split(f, "\n") {
			if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "--") && !strings.Contains(trim, "DELETE FROM app.schema_migrations") && strings.ContainsAny(trim, ";'") {
				t.Fatalf("comment contains a semicolon or an apostrophe: %s", trim)
			}
		}
	}
	stmts := sqlBodies(string(body))
	if len(stmts) != 5 || !strings.HasPrefix(stmts[0], "CREATE TABLE app.host_alert_settings (") ||
		stmts[1] != "ALTER TABLE app.problems DROP CONSTRAINT problems_kind_chk" ||
		!strings.Contains(stmts[2], "('host_offline', 'registry_login', 'deploy', 'host_file', 'agent_upgrade', 'host_resource')") ||
		stmts[3] != "REVOKE ALL ON TABLE app.host_alert_settings FROM deploybot_api" ||
		stmts[4] != "GRANT SELECT, INSERT, UPDATE ON TABLE app.host_alert_settings TO deploybot_api" {
		t.Fatalf("0033 statements %q", stmts)
	}
	for _, want := range []string{"ON DELETE CASCADE", "BETWEEN 5 AND 50", "BETWEEN 50 AND 99", "BETWEEN 50 AND 100"} {
		if !strings.Contains(stmts[0], want) {
			t.Fatalf("0033 table lacks %s", want)
		}
	}
	ds := sqlBodies(string(down))
	if len(ds) != 6 || ds[0] != "BEGIN" || ds[1] != "DELETE FROM app.problems WHERE kind = 'host_resource'" ||
		!strings.Contains(ds[3], "('host_offline', 'registry_login', 'deploy', 'host_file', 'agent_upgrade')") ||
		ds[4] != "DROP TABLE IF EXISTS app.host_alert_settings" || ds[5] != "COMMIT" ||
		!strings.Contains(string(down), "DELETE FROM app.schema_migrations WHERE name = '"+hostAlertsMigration+"'") {
		t.Fatalf("down script %q", ds)
	}
}

// TestHostAlertsMigration is §8: up, down (with a host_resource problem and
// a settings row) and up again, in one transaction that is rolled back.
func TestHostAlertsMigration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	lockMigrations(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	t.Cleanup(d.Close)
	if !onTestDatabase(ctx, d) {
		t.Fatal("refusing a database that is not deploybot_test or deploybot_test_*")
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	p := parity{t: t, st: d, ctx: ctx, at: time.Now().UTC().Truncate(time.Microsecond), tag: strings.ReplaceAll(uuid.NewString(), "-", "")[:8]}
	owner := p.user("mig33")
	org := p.org(owner.ID)
	host := p.enroll(org, nil, owner.ID, "mig33")

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET ROLE deploybot_owner`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	script := func(file string) {
		t.Helper()
		body, err := migrations.FS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range sqlBodies(string(body)) {
			if stmt == "BEGIN" || stmt == "COMMIT" {
				continue
			}
			if _, err := tx.Exec(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
		}
	}
	tables := func() int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'app' AND table_name = 'host_alert_settings'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	insertProblem := func() error {
		_, err := tx.Exec(ctx, `
INSERT INTO app.problems (id, kind, subject, org_id, host_id, level, title, detail, opened_at, updated_at)
VALUES ($1::uuid, 'host_resource', $2, $3::uuid, $4::uuid, 'needs_action', 'x is low on memory', '', now(), now())`,
			uuid.NewString(), host.ID.String()+":mem:"+p.tag, org.String(), host.ID.String())
		return err
	}
	if tables() != 1 {
		t.Fatal("after up: no table")
	}
	if err := insertProblem(); err != nil {
		t.Fatalf("host_resource refused after up: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.host_alert_settings (host_id, alerts_enabled, updated_at, updated_by) VALUES ($1::uuid, false, now(), $2::uuid)`, host.ID.String(), owner.ID.String()); err != nil {
		t.Fatal(err)
	}
	script("0033_host_alerts.down.sql")
	if tables() != 0 {
		t.Fatal("after down: table left")
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT k`); err != nil {
		t.Fatal(err)
	}
	if err := insertProblem(); err == nil || !strings.Contains(err.Error(), "problems_kind_chk") {
		t.Fatalf("host_resource accepted after down: %v", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT k`); err != nil {
		t.Fatal(err)
	}
	script(hostAlertsMigration)
	if tables() != 1 {
		t.Fatal("after up again: no table")
	}
	if err := insertProblem(); err != nil {
		t.Fatalf("host_resource refused after up again: %v", err)
	}
}

// TestParityHostDisksCap (review item 2): at most store.MaxHostDisks mounts,
// the first by path.
func TestParityHostDisksCap(t *testing.T) {
	runParity(t, func(p parity) {
		t := p.t
		owner := p.user("dc")
		host := p.enroll(p.org(owner.ID), nil, owner.ID, "dc")
		var samples []store.Sample
		for i := range store.MaxHostDisks + 10 {
			samples = append(samples, store.Sample{M: "disk", T: p.at, Tags: map[string]string{"path": fmt.Sprintf("/m%03d", i), "fstype": "ext4"}, Floats: map[string]float64{"used_percent": 1}})
		}
		if _, err := p.st.InsertBatch(p.ctx, host.ID, uuid.New(), samples, p.at); err != nil {
			t.Fatal(err)
		}
		disks, err := p.st.ListHostDisks(p.ctx, host.ID, p.at.Add(-time.Hour))
		if err != nil || len(disks) != store.MaxHostDisks || disks[0].Path != "/m000" || disks[len(disks)-1].Path != fmt.Sprintf("/m%03d", store.MaxHostDisks-1) {
			t.Fatalf("disks %d %v", len(disks), err)
		}
	})
}

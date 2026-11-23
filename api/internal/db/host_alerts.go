package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Host resource alerts (api-v1 v1.49, migration 0033).

func alertPercent(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func (d *DB) GetHostAlertSettings(ctx context.Context, hostID uuid.UUID) (store.HostAlertSettings, error) {
	s := store.DefaultHostAlertSettings()
	err := d.pool.QueryRow(ctx, `
SELECT alerts_enabled, mem_min_available_percent, disk_max_used_percent, cpu_max_busy_percent
FROM app.host_alert_settings WHERE host_id = $1::uuid
`, hostID.String()).Scan(&s.AlertsEnabled, &s.MemMinAvailablePercent, &s.DiskMaxUsedPercent, &s.CPUMaxBusyPercent)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.DefaultHostAlertSettings(), nil
	}
	if err != nil {
		return store.HostAlertSettings{}, mapErr(err)
	}
	return s, nil
}

func (d *DB) PutHostAlertSettings(ctx context.Context, hostID uuid.UUID, s store.HostAlertSettings, by uuid.UUID, at time.Time) error {
	if !store.ValidHostAlertSettings(s) {
		return store.ErrInvalid
	}
	return mapErr(exec(ctx, d.pool, `
INSERT INTO app.host_alert_settings (host_id, alerts_enabled, mem_min_available_percent, disk_max_used_percent, cpu_max_busy_percent, updated_at, updated_by)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::uuid)
ON CONFLICT (host_id) DO UPDATE SET
    alerts_enabled = EXCLUDED.alerts_enabled,
    mem_min_available_percent = EXCLUDED.mem_min_available_percent,
    disk_max_used_percent = EXCLUDED.disk_max_used_percent,
    cpu_max_busy_percent = EXCLUDED.cpu_max_busy_percent,
    updated_at = EXCLUDED.updated_at,
    updated_by = EXCLUDED.updated_by
`, hostID.String(), s.AlertsEnabled, alertPercent(s.MemMinAvailablePercent), alertPercent(s.DiskMaxUsedPercent), alertPercent(s.CPUMaxBusyPercent), at, by.String()))
}

func (d *DB) ListHostAlertSettings(ctx context.Context) (map[uuid.UUID]store.HostAlertSettings, error) {
	rows, err := d.pool.Query(ctx, `
SELECT host_id::text, alerts_enabled, mem_min_available_percent, disk_max_used_percent, cpu_max_busy_percent
FROM app.host_alert_settings
`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[uuid.UUID]store.HostAlertSettings{}
	for rows.Next() {
		var id string
		var s store.HostAlertSettings
		if err := rows.Scan(&id, &s.AlertsEnabled, &s.MemMinAvailablePercent, &s.DiskMaxUsedPercent, &s.CPUMaxBusyPercent); err != nil {
			return nil, mapErr(err)
		}
		out[uuid.MustParse(id)] = s
	}
	return out, mapErr(rows.Err())
}

// resourceSpec is the value of one measurement: the expression, the total
// (mem only, else NULL), the table, the row filter and the extra group key.
type resourceSpec struct {
	value, total, table, filter, path string
}

var resourceSpecs = map[string]resourceSpec{
	store.ResourceMem: {value: "available::float8", total: "total::float8", table: "metrics.host_mem",
		filter: "available IS NOT NULL AND total IS NOT NULL AND total > 0", path: "''"},
	store.ResourceDisk: {value: "used_percent", total: "NULL::float8", table: "metrics.host_disk",
		filter: "used_percent IS NOT NULL", path: "path"},
	store.ResourceCPU: {value: "100 - usage_idle", total: "NULL::float8", table: "metrics.host_cpu",
		filter: "cpu = 'cpu-total' AND usage_idle IS NOT NULL", path: "''"},
	store.ResourceLoad: {value: "load5 / n_cpus", total: "NULL::float8", table: "metrics.host_system",
		filter: "load5 IS NOT NULL AND n_cpus > 0", path: "''"},
}

// resourceWindowSQL is the five columns of one window that starts after the
// timestamp parameter p.
func resourceWindowSQL(p int) string {
	f := fmt.Sprintf("FILTER (WHERE time > $%d)", p)
	return fmt.Sprintf(`count(*) %[1]s,
       COALESCE(avg(v) %[1]s, 0), COALESCE(min(v) %[1]s, 0), COALESCE(max(v) %[1]s, 0), COALESCE(avg(t) %[1]s, 0)`, f)
}

// resourceStatsSQL is the grouped read of one measurement: one row per host
// (and mount for disk) over (now-15m, now], with the newest value and the
// 5, 10 and 15 minute windows. $1 host ids, $2 now, $3..$5 the window starts.
func resourceStatsSQL(spec resourceSpec) string {
	return fmt.Sprintf(`
WITH s AS (
    SELECT host_id, %s AS path, time, (%s)::float8 AS v, %s AS t
    FROM %s
    WHERE host_id = ANY($1::uuid[]) AND time > $5 AND time <= $2 AND %s
)
SELECT host_id::text, path, max(time),
       (array_agg(v ORDER BY time DESC))[1],
       %s,
       %s,
       %s
FROM s
GROUP BY host_id, path
ORDER BY host_id, path
`, spec.path, spec.value, spec.total, spec.table, spec.filter,
		resourceWindowSQL(3), resourceWindowSQL(4), resourceWindowSQL(5))
}

func (d *DB) HostResourceStats(ctx context.Context, measurement string, hostIDs []uuid.UUID, now time.Time) ([]store.ResourceStat, error) {
	spec, ok := resourceSpecs[measurement]
	if !ok {
		return nil, store.ErrUnknownMeasurement
	}
	out := []store.ResourceStat{}
	if len(hostIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(hostIDs))
	for i, id := range hostIDs {
		ids[i] = id.String()
	}
	rows, err := d.pool.Query(ctx, resourceStatsSQL(spec), ids, now,
		now.Add(-store.ResourceWindowShort), now.Add(-store.ResourceWindowMedium), now.Add(-store.ResourceWindowLong))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var st store.ResourceStat
		scanWindow := func(w *store.ResourceWindow) []any {
			return []any{&w.Count, &w.Avg, &w.Min, &w.Max, &w.AvgTotal}
		}
		dest := []any{&id, &st.Path, &st.LastAt, &st.Last}
		dest = append(dest, scanWindow(&st.Short)...)
		dest = append(dest, scanWindow(&st.Medium)...)
		dest = append(dest, scanWindow(&st.Long)...)
		if err := rows.Scan(dest...); err != nil {
			return nil, mapErr(err)
		}
		st.HostID = uuid.MustParse(id)
		out = append(out, st)
	}
	return out, mapErr(rows.Err())
}

func (d *DB) ListHostDisks(ctx context.Context, hostID uuid.UUID, since time.Time) ([]store.HostDisk, error) {
	rows, err := d.pool.Query(ctx, `
SELECT DISTINCT ON (path) path, fstype
FROM metrics.host_disk
WHERE host_id = $1::uuid AND time >= $2
ORDER BY path, time DESC
LIMIT $3
`, hostID.String(), since, store.MaxHostDisks)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.HostDisk{}
	for rows.Next() {
		var disk store.HostDisk
		if err := rows.Scan(&disk.Path, &disk.Fstype); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, disk)
	}
	return out, mapErr(rows.Err())
}

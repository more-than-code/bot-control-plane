package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Agent upgrades (api-v1 v1.48, migration 0032).

const agentUpgradeCols = `u.id::text, u.host_id::text, u.from_version, u.to_version, u.state, COALESCE(u.reason, ''),
       u.started_by::text, COALESCE(us.name, ''), u.started_at, u.finished_at`

const agentUpgradeFrom = `app.host_agent_upgrades u LEFT JOIN app.users us ON us.id = u.started_by`

func scanAgentUpgrade(row pgx.Row) (store.AgentUpgrade, error) {
	var r store.AgentUpgrade
	var id, host, by string
	if err := row.Scan(&id, &host, &r.From, &r.To, &r.State, &r.Reason, &by, &r.StartedByName, &r.StartedAt, &r.FinishedAt); err != nil {
		return store.AgentUpgrade{}, mapErr(err)
	}
	r.ID, r.HostID, r.StartedBy = uuid.MustParse(id), uuid.MustParse(host), uuid.MustParse(by)
	return r, nil
}

func (d *DB) StartAgentUpgrade(ctx context.Context, in store.AgentUpgrade) (store.AgentUpgrade, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.AgentUpgrade{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	// One start at a time per host, so two clicks never both pass the
	// open-row check.
	if err := exec(ctx, tx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "agent_upgrade:"+in.HostID.String()); err != nil {
		return store.AgentUpgrade{}, mapErr(err)
	}
	var open bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM app.host_agent_upgrades WHERE host_id = $1::uuid AND state IN ('started', 'running'))
`, in.HostID.String()).Scan(&open); err != nil {
		return store.AgentUpgrade{}, mapErr(err)
	}
	if open {
		return store.AgentUpgrade{}, store.ErrConflict
	}
	if err := exec(ctx, tx, `
INSERT INTO app.host_agent_upgrades (id, host_id, from_version, to_version, state, started_by, started_at)
VALUES ($1::uuid, $2::uuid, $3, $4, 'started', $5::uuid, $6)
`, in.ID.String(), in.HostID.String(), in.From, in.To, in.StartedBy.String(), in.StartedAt); err != nil {
		return store.AgentUpgrade{}, mapErr(err)
	}
	row, err := scanAgentUpgrade(tx.QueryRow(ctx, `SELECT `+agentUpgradeCols+` FROM `+agentUpgradeFrom+` WHERE u.id = $1::uuid`, in.ID.String()))
	if err != nil {
		return store.AgentUpgrade{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.AgentUpgrade{}, mapErr(err)
	}
	return row, nil
}

func (d *DB) SettleAgentUpgrade(ctx context.Context, id uuid.UUID, from []string, to, reason string, at time.Time) (bool, error) {
	if !store.ValidAgentUpgradeState(to) || len(reason) > store.MaxAgentUpgradeReason {
		return false, store.ErrInvalid
	}
	var finished any
	if !store.AgentUpgradeOpen(to) {
		finished = at
	}
	var why any
	if reason != "" {
		why = reason
	}
	tag, err := d.pool.Exec(ctx, `
UPDATE app.host_agent_upgrades SET state = $3, reason = $4, finished_at = $5
WHERE id = $1::uuid AND state = ANY($2::text[])
`, id.String(), from, to, why, finished)
	if err != nil {
		return false, mapErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (d *DB) OpenAgentUpgrade(ctx context.Context, hostID uuid.UUID) (store.AgentUpgrade, error) {
	return scanAgentUpgrade(d.pool.QueryRow(ctx, `
SELECT `+agentUpgradeCols+` FROM `+agentUpgradeFrom+`
WHERE u.host_id = $1::uuid AND u.state IN ('started', 'running')
ORDER BY u.started_at DESC, u.id DESC
LIMIT 1
`, hostID.String()))
}

func (d *DB) ListAgentUpgrades(ctx context.Context, hostID uuid.UUID, limit int) ([]store.AgentUpgrade, error) {
	return d.agentUpgradeRows(ctx, `
SELECT `+agentUpgradeCols+` FROM `+agentUpgradeFrom+`
WHERE u.host_id = $1::uuid
ORDER BY u.started_at DESC, u.id DESC
LIMIT $2
`, hostID.String(), limit)
}

func (d *DB) LatestAgentUpgrades(ctx context.Context, hostIDs []uuid.UUID) (map[uuid.UUID]store.AgentUpgrade, error) {
	out := map[uuid.UUID]store.AgentUpgrade{}
	if len(hostIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(hostIDs))
	for i, id := range hostIDs {
		ids[i] = id.String()
	}
	rows, err := d.agentUpgradeRows(ctx, `
SELECT DISTINCT ON (u.host_id) `+agentUpgradeCols+` FROM `+agentUpgradeFrom+`
WHERE u.host_id = ANY(CAST($1 AS uuid[]))
ORDER BY u.host_id, u.started_at DESC, u.id DESC
`, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.HostID] = r
	}
	return out, nil
}

func (d *DB) ListStaleAgentUpgrades(ctx context.Context, before time.Time) ([]store.AgentUpgrade, error) {
	return d.agentUpgradeRows(ctx, `
SELECT `+agentUpgradeCols+` FROM `+agentUpgradeFrom+`
WHERE u.state IN ('started', 'running') AND u.started_at < $1
ORDER BY u.started_at ASC, u.id ASC
`, before)
}

func (d *DB) agentUpgradeRows(ctx context.Context, sql string, args ...any) ([]store.AgentUpgrade, error) {
	rows, err := d.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.AgentUpgrade{}
	for rows.Next() {
		r, err := scanAgentUpgrade(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

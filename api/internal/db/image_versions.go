package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// holdRowsPerHost bounds the deploy rows read per (entry, host): enough for
// RecentDeploys and three distinct rollback digests in any real history.
const holdRowsPerHost = 200

// ImageDigestHolds is api-v1 v1.45 §5.
func (d *DB) ImageDigestHolds(ctx context.Context, entries []store.EntryKey) ([]store.DigestHold, error) {
	if len(entries) == 0 {
		return []store.DigestHold{}, nil
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID.String()
	}
	out := []store.DigestHold{}
	// A live service carries its entry's name in its own project.
	rows, err := d.pool.Query(ctx, `
SELECT e.id::text, s.host_id::text, s.image_digest
FROM app.catalog_entries AS e
JOIN app.services AS s ON s.project_id = e.project_id AND s.catalog_entry = e.name
WHERE e.id = ANY($1::uuid[]) AND s.gone_at IS NULL AND s.image_digest IS NOT NULL
ORDER BY e.id, s.host_id
`, ids)
	if err != nil {
		return nil, mapErr(err)
	}
	for rows.Next() {
		var entryID, hostID, digest string
		if err := rows.Scan(&entryID, &hostID, &digest); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		eid, err := parseID(entryID)
		if err != nil {
			rows.Close()
			return nil, err
		}
		hid, err := parseID(hostID)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, store.DigestHold{EntryID: eid, HostID: hid, Digest: digest, Why: store.HoldRunning})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	drows, err := d.pool.Query(ctx, `
SELECT `+deployCols+`
FROM (
    SELECT *, row_number() OVER (PARTITION BY entry_id, host_id
        ORDER BY finished_at DESC NULLS FIRST, started_at DESC, id DESC) AS rn
    FROM app.image_deploys
    WHERE entry_id = ANY($1::uuid[])
) AS d
WHERE rn <= $2
ORDER BY entry_id, host_id
`, ids, holdRowsPerHost)
	if err != nil {
		return nil, mapErr(err)
	}
	defer drows.Close()
	deploys, err := collectDeploys(drows)
	if err != nil {
		return nil, err
	}
	return append(out, groupDeployHolds(deploys)...), nil
}

// groupDeployHolds runs store.DeployHolds per (entry, host), in first-seen order.
func groupDeployHolds(rows []store.ImageDeploy) []store.DigestHold {
	type key struct{ entry, host uuid.UUID }
	groups := map[key][]store.ImageDeploy{}
	var order []key
	for _, r := range rows {
		k := key{r.EntryID, r.HostID}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	out := []store.DigestHold{}
	for _, k := range order {
		out = append(out, store.DeployHolds(k.entry, k.host, groups[k])...)
	}
	return out
}

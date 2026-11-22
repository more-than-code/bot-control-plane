package memstore

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// ImageDigestHolds is api-v1 v1.45 §5, as the database answers it.
func (m *Mem) ImageDigestHolds(_ context.Context, entries []store.EntryKey) ([]store.DigestHold, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.DigestHold{}
	byID := map[uuid.UUID]store.EntryKey{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	ids := make([]uuid.UUID, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		e := byID[id]
		var running []store.DigestHold
		for _, svc := range m.services {
			if svc.GoneAt != nil || svc.ImageDigest == nil || svc.ProjectID == nil || *svc.ProjectID != e.ProjectID ||
				svc.CatalogEntry == nil || *svc.CatalogEntry != e.Name {
				continue
			}
			running = append(running, store.DigestHold{EntryID: id, HostID: svc.HostID, Digest: *svc.ImageDigest, Why: store.HoldRunning})
		}
		sort.Slice(running, func(i, j int) bool { return running[i].HostID.String() < running[j].HostID.String() })
		out = append(out, running...)
	}
	byHost := map[[2]uuid.UUID][]store.ImageDeploy{}
	var keys [][2]uuid.UUID
	for _, row := range m.imageDeploys {
		if _, ok := byID[row.EntryID]; !ok {
			continue
		}
		k := [2]uuid.UUID{row.EntryID, row.HostID}
		if _, ok := byHost[k]; !ok {
			keys = append(keys, k)
		}
		byHost[k] = append(byHost[k], copyDeploy(row))
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0].String() < keys[j][0].String()
		}
		return keys[i][1].String() < keys[j][1].String()
	})
	for _, k := range keys {
		out = append(out, store.DeployHolds(k[0], k[1], byHost[k])...)
	}
	return out, nil
}

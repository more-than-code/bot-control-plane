package store

import (
	"sort"

	"github.com/google/uuid"
)

// Image version delete guards (api-v1 v1.45 §5): the digests an image entry
// holds on a host, so a package version carrying one is never deleted.

// Why a digest is held.
const (
	HoldRunning  = "running"
	HoldRollback = "rollback_target"
	HoldRecent   = "recent_deploy"
)

// RecentDeploys is how many of an entry's newest deploy rows per host hold
// their digest, whatever their outcome.
const RecentDeploys = 5

// rollbackDigests is how many distinct succeeded or unchanged digests the
// rollback route accepts (api-v1 v1.6), all of which are held.
const rollbackDigests = 3

// EntryKey names one image entry for ImageDigestHolds.
type EntryKey struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	Name      string
}

// DigestHold is one digest an entry holds on a host, and why.
type DigestHold struct {
	EntryID uuid.UUID
	HostID  uuid.UUID
	Digest  string
	Why     string
}

// DeployHolds reduces one (entry, host)'s deploy rows to the digests they
// hold: those of the RecentDeploys newest rows (an in-flight row counts
// first), and the rollback route's targets, the newest rollbackDigests
// distinct succeeded or unchanged digests by finish time. A digest held both
// ways is reported once, as a rollback target. rows may be in any order.
func DeployHolds(entryID, hostID uuid.UUID, rows []ImageDeploy) []DigestHold {
	sorted := append([]ImageDeploy(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if (a.FinishedAt == nil) != (b.FinishedAt == nil) {
			return a.FinishedAt == nil
		}
		if a.FinishedAt != nil && !a.FinishedAt.Equal(*b.FinishedAt) {
			return a.FinishedAt.After(*b.FinishedAt)
		}
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.After(b.StartedAt)
		}
		return a.ID.String() > b.ID.String()
	})
	why := map[string]string{}
	var order []string
	add := func(d *string, w string) {
		if d == nil || *d == "" {
			return
		}
		if _, ok := why[*d]; !ok {
			order = append(order, *d)
		} else if w != HoldRollback {
			return
		}
		why[*d] = w
	}
	seen := 0
	for _, row := range sorted {
		if row.InFlight || row.Outcome != "succeeded" && row.Outcome != "unchanged" || row.Digest == nil {
			continue
		}
		if _, ok := why[*row.Digest]; ok {
			continue
		}
		add(row.Digest, HoldRollback)
		seen++
		if seen == rollbackDigests {
			break
		}
	}
	for i, row := range sorted {
		if i == RecentDeploys {
			break
		}
		add(row.Digest, HoldRecent)
	}
	out := make([]DigestHold, 0, len(order))
	for _, d := range order {
		out = append(out, DigestHold{EntryID: entryID, HostID: hostID, Digest: d, Why: why[d]})
	}
	return out
}

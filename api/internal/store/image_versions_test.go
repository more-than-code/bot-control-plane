package store

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func holdDigest(c string) *string {
	d := "sha256:" + strings.Repeat(c, 64)
	return &d
}

func TestDeployHolds(t *testing.T) {
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	entry, host := uuid.New(), uuid.New()
	row := func(min int, outcome, c string) ImageDeploy {
		done := at.Add(time.Duration(min) * time.Minute)
		r := ImageDeploy{ID: uuid.New(), EntryID: entry, HostID: host, Outcome: outcome, StartedAt: done.Add(-time.Second), FinishedAt: &done}
		if c != "" {
			r.Digest = holdDigest(c)
		}
		return r
	}
	rows := []ImageDeploy{
		row(1, "succeeded", "a"), // oldest success: 4th distinct, not a rollback target
		row(2, "succeeded", "b"),
		row(3, "unchanged", "c"),
		row(4, "failed", "x"),
		row(5, "succeeded", "d"),
		row(6, "failed", "y"),
		row(7, "rolled_back", "z"),
		row(8, "succeeded", "d"),
	}
	inFlight := ImageDeploy{ID: uuid.New(), EntryID: entry, HostID: host, Outcome: "in_flight", InFlight: true, StartedAt: at.Add(9 * time.Minute)}
	rows = append(rows, inFlight)
	got := map[string]string{}
	for _, h := range DeployHolds(entry, host, rows) {
		if h.EntryID != entry || h.HostID != host {
			t.Fatalf("hold %+v", h)
		}
		got[h.Digest[7:8]] = h.Why
	}
	want := map[string]string{
		"d": HoldRollback, "c": HoldRollback, "b": HoldRollback,
		"z": HoldRecent, "y": HoldRecent, // x is the 6th newest row
	}
	if len(got) != len(want) {
		t.Fatalf("holds %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("holds %v, want %v", got, want)
		}
	}
	if len(DeployHolds(entry, host, nil)) != 0 {
		t.Fatal("empty")
	}
}

// api-v1 v1.47: refused rows do not push a deployed digest out of the
// recent slots.
func TestDeployHoldsSkipRefused(t *testing.T) {
	at := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	entry, host := uuid.New(), uuid.New()
	var rows []ImageDeploy
	for i, c := range []string{"a", "b", "c", "d", "e"} {
		done := at.Add(time.Duration(i) * time.Minute)
		rows = append(rows, ImageDeploy{ID: uuid.New(), EntryID: entry, HostID: host, Outcome: "failed", Digest: holdDigest(c), StartedAt: done, FinishedAt: &done})
	}
	for i := 0; i < 6; i++ {
		done := at.Add(time.Hour + time.Duration(i)*time.Minute)
		rows = append(rows, ImageDeploy{ID: uuid.New(), EntryID: entry, HostID: host, Outcome: "refused", StartedAt: done, FinishedAt: &done})
	}
	if got := DeployHolds(entry, host, rows); len(got) != 5 {
		t.Fatalf("holds %+v", got)
	}
}

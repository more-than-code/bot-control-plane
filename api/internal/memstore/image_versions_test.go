package memstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.45 §5: a live service of the entry holds its running digest;
// a gone one, or one of another entry, does not.
func TestImageDigestHoldsRunning(t *testing.T) {
	m := New()
	project, host := uuid.New(), uuid.New()
	entry := store.EntryKey{ID: uuid.New(), ProjectID: project, Name: "web"}
	digest := "sha256:" + strings.Repeat("a", 64)
	other := "sha256:" + strings.Repeat("b", 64)
	name, otherName := "web", "api"
	gone := time.Now()
	m.services[uuid.New()] = store.Service{HostID: host, ProjectID: &project, CatalogEntry: &name, ImageDigest: &digest}
	m.services[uuid.New()] = store.Service{HostID: host, ProjectID: &project, CatalogEntry: &name, ImageDigest: &other, GoneAt: &gone}
	m.services[uuid.New()] = store.Service{HostID: host, ProjectID: &project, CatalogEntry: &otherName, ImageDigest: &other}
	got, err := m.ImageDigestHolds(context.Background(), []store.EntryKey{entry})
	if err != nil || len(got) != 1 || got[0].Digest != digest || got[0].Why != store.HoldRunning || got[0].HostID != host || got[0].EntryID != entry.ID {
		t.Fatalf("holds %+v %v", got, err)
	}
}

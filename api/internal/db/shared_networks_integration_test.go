package db

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestSharedNetworksPG checks api-v1 v1.25 against Postgres: the host's
// shared_networks round-trips through every host query and is cleared by a
// later hello without it, and an image entry's networks survive jsonb (which
// reorders keys) with ParseStored and spec_hash unchanged on GET then PUT.
// It skips without DEPLOYBOT_TEST_DATABASE_URL.
func TestSharedNetworksPG(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, owner, host, entries := autoStoreFixture(t, ctx, 1)
	projectID := entries[0].ProjectID
	project, err := d.GetProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)

	nets := []store.SharedNetwork{
		{Name: "dd", Subnet: "10.200.10.0/24", Projects: []string{project.Slug, "other"}},
		{Name: "lone", Subnet: "192.168.40.16/28", Projects: []string{}},
	}
	if err := d.SetHostPrograms(ctx, host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Images: true, Docker: true, SharedNetworks: nets}); err != nil {
		t.Fatal(err)
	}
	read := func(label string) [][]store.SharedNetwork {
		t.Helper()
		var out [][]store.SharedNetwork
		got, err := d.GetHost(ctx, host.ID)
		if err != nil || got.Programs == nil {
			t.Fatalf("%s get %#v %v", label, got.Programs, err)
		}
		out = append(out, got.Programs.SharedNetworks)
		listed, _, err := d.ListHosts(ctx, projectID, 100, nil)
		if err != nil {
			t.Fatalf("%s list %v", label, err)
		}
		orgListed, _, err := d.ListOrgHosts(ctx, project.OrgID, 100, nil)
		if err != nil {
			t.Fatalf("%s org list %v", label, err)
		}
		for _, list := range [][]store.Host{listed, orgListed} {
			found := false
			for _, h := range list {
				if h.ID == host.ID {
					found = true
					if h.Programs == nil {
						t.Fatalf("%s listed without programs", label)
					}
					out = append(out, h.Programs.SharedNetworks)
				}
			}
			if !found {
				t.Fatalf("%s host not listed", label)
			}
		}
		return out
	}
	for i, got := range read("set") {
		if !reflect.DeepEqual(got, nets) {
			t.Fatalf("query %d: %#v", i, got)
		}
		if got[1].Projects == nil {
			t.Fatalf("query %d: empty member list read back as nil", i)
		}
	}
	// A later hello without the key (agent < 0.2.9) clears the list.
	if err := d.SetHostPrograms(ctx, host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Images: true, Docker: true}); err != nil {
		t.Fatal(err)
	}
	for i, got := range read("cleared") {
		if got != nil {
			t.Fatalf("query %d after clear: %#v", i, got)
		}
	}

	// Entry with networks: stored, read back, re-parsed, same hash on PUT.
	img, err := catalog.BuildImage("net", []byte(`{"kind":"service","image":"nginx:1.27","networks":["edge","dd"],"ports":[{"host":8080,"container":80}]}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hash := catalog.ImageHash(img, nil)
	if _, err := d.CreateCatalogEntry(ctx, projectID, store.CatalogWrite{
		Name: "net", Kind: "image", ImageRaw: []byte(catalog.SpecJSON(img)),
		SpecHash: hash, CommandDisplay: img.Image, Actor: owner.ID, At: at,
	}); err != nil {
		t.Fatal(err)
	}
	roundTrip := func(name, wantHash string, wantNets []string) {
		t.Helper()
		e, err := d.GetCatalogEntry(ctx, projectID, name)
		if err != nil {
			t.Fatal(err)
		}
		back, err := catalog.ParseStored(e.ImageRaw)
		if err != nil {
			t.Fatalf("%s parse stored %s: %v", name, e.ImageRaw, err)
		}
		if !reflect.DeepEqual(back.Networks, wantNets) {
			t.Fatalf("%s networks %#v", name, back.Networks)
		}
		if e.SpecHash != wantHash {
			t.Fatalf("%s stored hash %s", name, e.SpecHash)
		}
		// GET then PUT: the stored spec, as Postgres returns it, is a valid
		// write body with the same hash.
		again, err := catalog.BuildImage(name, e.ImageRaw, nil, nil)
		if err != nil {
			t.Fatalf("%s rebuild: %v", name, err)
		}
		if got := catalog.ImageHash(again, nil); got != wantHash {
			t.Fatalf("%s hash after GET then PUT %s want %s", name, got, wantHash)
		}
		if !reflect.DeepEqual(again.Networks, wantNets) {
			t.Fatalf("%s rebuilt networks %#v", name, again.Networks)
		}
	}
	roundTrip("net", hash, []string{"edge", "dd"})
	// The fixture entry was stored without networks: nil, hash unchanged.
	roundTrip(entries[0].Name, entries[0].SpecHash, nil)
}

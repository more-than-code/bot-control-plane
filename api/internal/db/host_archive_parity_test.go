package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// clearArchived, on Postgres, clears archived_at on the org hosts when the
// test ends, so the test database keeps no archived host behind.
func (p parity) clearArchived(org uuid.UUID) {
	d, ok := p.st.(*DB)
	if !ok {
		return
	}
	t := p.t
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := d.pool.Exec(ctx, `UPDATE app.hosts SET archived_at = NULL WHERE org_id = $1::uuid`, org.String()); err != nil {
			t.Errorf("clear archived: %v", err)
		}
	})
}

// TestParityArchiveHost is api-v1 v1.40 at the store (spec-host-remove 6.1):
// only a revoked host is archived, a second archive keeps the first time,
// and an archived host leaves both host lists and every project services
// view while reads by id still return it.
func TestParityArchiveHost(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("ar")
		org := p.org(owner.ID)
		p.freeSharedNames(org)
		p.clearArchived(org)
		home := p.project(org, owner.ID, "arhome")
		gone := p.enroll(org, &store.TokenGrant{ProjectID: home.ID, Level: "deploy"}, owner.ID, "ag")
		live := p.enroll(org, &store.TokenGrant{ProjectID: home.ID, Level: "deploy"}, owner.ID, "al")
		spec, hash, display := catalogSpec(p.t, "pnpm run dev")
		if _, err := p.st.CreateCatalogEntry(p.ctx, home.ID, store.CatalogWrite{Name: "arweb", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: p.at}); err != nil {
			p.t.Fatal(err)
		}
		for _, h := range []store.Host{gone, live} {
			if err := p.st.SetHostPrograms(p.ctx, h.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Projects: []string{home.Slug}}); err != nil {
				p.t.Fatal(err)
			}
			if err := p.st.UpsertDiscovered(p.ctx, h.ID, []store.Discovered{{Name: "x", Kind: "process", Owner: &store.DiscoveredOwner{Kind: "program", Project: home.Slug, Entry: "arweb"}}}, p.at, true, false); err != nil {
				p.t.Fatal(err)
			}
			if s := p.service(h.ID, "arweb"); s.ProjectID == nil || *s.ProjectID != home.ID {
				p.t.Fatalf("owned row %+v", s)
			}
		}

		if _, _, err := p.st.ArchiveHost(p.ctx, uuid.New(), p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("unknown host %v", err)
		}
		if _, _, err := p.st.ArchiveHost(p.ctx, gone.ID, p.at); !errors.Is(err, store.ErrState) {
			p.t.Fatalf("live host %v", err)
		}
		if h, _ := p.st.GetHost(p.ctx, gone.ID); h.ArchivedAt != nil {
			p.t.Fatalf("live host archived %v", h.ArchivedAt)
		}
		if err := p.st.RevokeHost(p.ctx, gone.ID, p.at); err != nil {
			p.t.Fatal(err)
		}

		projectHosts := func() map[uuid.UUID]bool {
			p.t.Helper()
			items, _, err := p.st.ListHosts(p.ctx, home.ID, 200, nil)
			if err != nil {
				p.t.Fatal(err)
			}
			out := map[uuid.UUID]bool{}
			for _, h := range items {
				out[h.ID] = true
			}
			return out
		}
		orgHosts := func() map[uuid.UUID]bool {
			p.t.Helper()
			items, _, err := p.st.ListOrgHosts(p.ctx, org, 200, nil)
			if err != nil {
				p.t.Fatal(err)
			}
			out := map[uuid.UUID]bool{}
			for _, h := range items {
				out[h.ID] = true
			}
			return out
		}
		serviceHosts := func(view string) map[uuid.UUID]int {
			p.t.Helper()
			page, err := p.st.ListProjectServices(p.ctx, home.ID, store.ServiceListQuery{Sort: "name", Dir: "asc", Limit: 50, View: view}, p.at)
			if err != nil {
				p.t.Fatal(err)
			}
			out := map[uuid.UUID]int{}
			for _, row := range page.Items {
				out[row.HostID]++
			}
			if len(page.Items) != page.Total {
				p.t.Fatalf("view %q items %d total %d", view, len(page.Items), page.Total)
			}
			return out
		}

		// Revoked, not archived: still listed, and view=all keeps its row.
		if !projectHosts()[gone.ID] || !orgHosts()[gone.ID] || serviceHosts("all")[gone.ID] != 1 {
			p.t.Fatal("revoked host missing before archive")
		}

		at, changed, err := p.st.ArchiveHost(p.ctx, gone.ID, p.at)
		if err != nil || !at.Equal(p.at) || !changed {
			p.t.Fatalf("archive %v %v %v", at, changed, err)
		}
		again, changed, err := p.st.ArchiveHost(p.ctx, gone.ID, p.at.Add(time.Hour))
		if err != nil || !again.Equal(p.at) || changed {
			p.t.Fatalf("archive again %v %v %v, want the first time, unchanged", again, changed, err)
		}
		h, err := p.st.GetHost(p.ctx, gone.ID)
		if err != nil || h.ArchivedAt == nil || !h.ArchivedAt.Equal(p.at) || h.RevokedAt == nil {
			p.t.Fatalf("archived host by id %+v %v", h, err)
		}
		if l, _ := p.st.GetHost(p.ctx, live.ID); l.ArchivedAt != nil {
			p.t.Fatal("live host archived too")
		}

		if ph, oh := projectHosts(), orgHosts(); ph[gone.ID] || oh[gone.ID] || !ph[live.ID] || !oh[live.ID] {
			p.t.Fatalf("lists after archive: project %v org %v", ph, oh)
		}
		// Only "" and "all" change on archive: the deploybot views already drop
		// revoked hosts. They stay here as regression guards.
		for _, view := range []string{"", "all", "deploybot", "deploybot_and_discovered"} {
			got := serviceHosts(view)
			if got[gone.ID] != 0 || got[live.ID] != 1 {
				p.t.Fatalf("view %q rows by host %v", view, got)
			}
		}
		// Reads by host id are unchanged.
		items, err := p.st.ListServices(p.ctx, gone.ID, nil, p.at)
		if err != nil || len(items) == 0 {
			p.t.Fatalf("services by host %d %v", len(items), err)
		}
		access, err := p.st.ListHostAccess(p.ctx, gone.ID)
		if err != nil || len(access) != 1 {
			p.t.Fatalf("host access kept %v %v", access, err)
		}
	})
}

// TestParityArchiveHostConcurrent: parallel archives of one revoked host all
// return the same time and exactly one reports changed (the FOR UPDATE lock
// on Postgres, the mutex in memstore), so the handler audits once.
func TestParityArchiveHostConcurrent(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("ac")
		org := p.org(owner.ID)
		p.freeSharedNames(org)
		p.clearArchived(org)
		home := p.project(org, owner.ID, "achome")
		h := p.enroll(org, &store.TokenGrant{ProjectID: home.ID, Level: "deploy"}, owner.ID, "ac")
		if err := p.st.RevokeHost(p.ctx, h.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		const n = 8
		type result struct {
			at      time.Time
			changed bool
			err     error
		}
		results := make(chan result, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				at, changed, err := p.st.ArchiveHost(p.ctx, h.ID, p.at.Add(time.Duration(i)*time.Second))
				results <- result{at, changed, err}
			}(i)
		}
		wg.Wait()
		close(results)
		var first time.Time
		changes := 0
		for r := range results {
			if r.err != nil {
				p.t.Fatalf("archive %v", r.err)
			}
			if first.IsZero() {
				first = r.at
			}
			if !r.at.Equal(first) {
				p.t.Fatalf("archive times differ %v %v", r.at, first)
			}
			if r.changed {
				changes++
			}
		}
		if changes != 1 {
			p.t.Fatalf("changed %d times, want 1", changes)
		}
	})
}

package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// freeSharedNames, on Postgres, gives the org's revoked hosts unique names
// when the test ends, so no (org, name) pair stays shared in the test
// database: the 0015 round trip and the phase 1 restore in later tests put
// back constraints that a shared pair would break. It also checks that none
// is left.
func (p parity) freeSharedNames(org uuid.UUID) {
	d, ok := p.st.(*DB)
	if !ok {
		return
	}
	t := p.t
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := d.pool.Exec(ctx, `UPDATE app.hosts SET name = 'gone-' || replace(id::text, '-', '') WHERE org_id = $1::uuid AND revoked_at IS NOT NULL`, org.String()); err != nil {
			t.Errorf("free revoked names: %v", err)
			return
		}
		var shared int
		if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM app.hosts WHERE org_id = $1::uuid GROUP BY name HAVING count(*) > 1) AS s`, org.String()).Scan(&shared); err != nil || shared != 0 {
			t.Errorf("shared names left %d %v", shared, err)
		}
	})
}

// TestParityHostNamesEnrol is api-v1 v1.38 item 1 (spec-host-names 7.1): a
// revoked host does not hold its name, a live one does.
func TestParityHostNamesEnrol(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("hn")
		org := p.org(owner.ID)
		p.freeSharedNames(org)
		old := p.enroll(org, nil, owner.ID, "pi2")
		if err := p.st.RevokeHost(p.ctx, old.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		again := p.enroll(org, nil, owner.ID, "pi2")
		if again.Name != old.Name {
			p.t.Fatalf("re-enrolled name %q, want %q (the old host is revoked)", again.Name, old.Name)
		}
		third := p.enroll(org, nil, owner.ID, "pi2")
		if third.Name != old.Name+"-2" {
			p.t.Fatalf("live clash name %q, want %q", third.Name, old.Name+"-2")
		}
		revoke := func(h store.Host) {
			p.t.Helper()
			if err := p.st.RevokeHost(p.ctx, h.ID, p.at); err != nil {
				p.t.Fatal(err)
			}
		}

		// Two revoked hosts with the same name hold nothing: the base name.
		r1 := p.enroll(org, nil, owner.ID, "rv")
		revoke(r1)
		r2 := p.enroll(org, nil, owner.ID, "rv")
		if r2.Name != r1.Name {
			p.t.Fatalf("second enrol %q, want %q", r2.Name, r1.Name)
		}
		revoke(r2)
		if r3 := p.enroll(org, nil, owner.ID, "rv"); r3.Name != r1.Name {
			p.t.Fatalf("after two revoked %q, want %q", r3.Name, r1.Name)
		}

		// Live base and live -2: the next is -3.
		l1 := p.enroll(org, nil, owner.ID, "lv")
		if l2 := p.enroll(org, nil, owner.ID, "lv"); l2.Name != l1.Name+"-2" {
			p.t.Fatalf("second live %q", l2.Name)
		}
		if l3 := p.enroll(org, nil, owner.ID, "lv"); l3.Name != l1.Name+"-3" {
			p.t.Fatalf("third live %q, want %q", l3.Name, l1.Name+"-3")
		}

		// Live base and revoked -2: -2 is free again.
		m1 := p.enroll(org, nil, owner.ID, "mx")
		m2 := p.enroll(org, nil, owner.ID, "mx")
		if m2.Name != m1.Name+"-2" {
			p.t.Fatalf("second mixed %q", m2.Name)
		}
		revoke(m2)
		if m3 := p.enroll(org, nil, owner.ID, "mx"); m3.Name != m1.Name+"-2" {
			p.t.Fatalf("after revoked -2 %q, want %q", m3.Name, m1.Name+"-2")
		}

		// A 63-character base with a live clash is cut before the suffix.
		prefix := strings.Repeat("l", 63-len("-"+p.tag))
		long := p.enroll(org, nil, owner.ID, prefix)
		if len(long.Name) != 63 {
			p.t.Fatalf("long base length %d", len(long.Name))
		}
		clash := p.enroll(org, nil, owner.ID, prefix)
		if want := long.Name[:61] + "-2"; clash.Name != want || len(clash.Name) > 63 {
			p.t.Fatalf("long clash %q (%d), want %q", clash.Name, len(clash.Name), want)
		}
	})
}

// TestParityRenameHost is api-v1 v1.38 item 2 at the store (spec-host-names
// 7.2 to 7.4): same rules in Postgres and memstore.
func TestParityRenameHost(t *testing.T) {
	runParity(t, func(p parity) {
		owner, other := p.user("rn"), p.user("ro")
		org := p.org(owner.ID)
		p.freeSharedNames(org)
		a := p.enroll(org, nil, owner.ID, "pa")
		b := p.enroll(org, nil, owner.ID, "pb")
		gone := p.enroll(org, nil, owner.ID, "pg")
		if err := p.st.RevokeHost(p.ctx, gone.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		// Another org may hold the name.
		away := p.enroll(p.org(other.ID), nil, other.ID, "pz")

		// A live duplicate in the org is a conflict, and nothing changes.
		if _, err := p.st.RenameHost(p.ctx, a.ID, b.Name); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("live duplicate %v", err)
		}
		if h, _ := p.st.GetHost(p.ctx, a.ID); h.Name != a.Name {
			p.t.Fatalf("name after conflict %q", h.Name)
		}
		// The same name changes nothing.
		if from, err := p.st.RenameHost(p.ctx, a.ID, a.Name); err != nil || from != a.Name {
			p.t.Fatalf("same name %q %v", from, err)
		}
		// A name held only by a revoked host, or by another org, is free.
		from, err := p.st.RenameHost(p.ctx, a.ID, gone.Name)
		if err != nil || from != a.Name {
			p.t.Fatalf("rename onto revoked name %q %v", from, err)
		}
		h, err := p.st.GetHost(p.ctx, a.ID)
		if err != nil || h.Name != gone.Name || h.ID != a.ID || h.OrgID != org {
			p.t.Fatalf("renamed host %+v %v", h, err)
		}
		if g, _ := p.st.GetHost(p.ctx, gone.ID); g.Name != gone.Name || g.RevokedAt == nil {
			p.t.Fatalf("revoked host %+v", g)
		}
		if _, err := p.st.RenameHost(p.ctx, b.ID, away.Name); err != nil {
			p.t.Fatalf("other org name %v", err)
		}
		// The freed name can be taken again.
		if _, err := p.st.RenameHost(p.ctx, b.ID, a.Name); err != nil {
			p.t.Fatalf("freed name %v", err)
		}
		// A revoked host cannot be renamed; an unknown host is not found.
		if _, err := p.st.RenameHost(p.ctx, gone.ID, "fresh-"+p.tag); !errors.Is(err, store.ErrState) {
			p.t.Fatalf("revoked rename %v", err)
		}
		if _, err := p.st.RenameHost(p.ctx, uuid.New(), "fresh-"+p.tag); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("unknown host %v", err)
		}
	})
}

// TestRenameHostConcurrent is api-v1 v1.38 on Postgres: two hosts renamed to
// the same free name at once; the live-host index lets exactly one win.
func TestRenameHostConcurrent(t *testing.T) {
	if os.Getenv("DEPLOYBOT_TEST_DATABASE_URL") == "" {
		t.Skip()
	}
	d, ok := parityStores(t)["postgres"].(*DB)
	if !ok {
		t.Fatal("no postgres store")
	}
	p := parity{t: t, st: d, ctx: context.Background(), at: time.Now().UTC().Truncate(time.Microsecond), tag: strings.ReplaceAll(uuid.NewString(), "-", "")[:8]}
	owner := p.user("rc")
	org := p.org(owner.ID)
	p.freeSharedNames(org)
	a := p.enroll(org, nil, owner.ID, "ca")
	b := p.enroll(org, nil, owner.ID, "cb")
	target := "same-" + p.tag
	for round := 0; round < 5; round++ {
		name := target + "-" + string(rune('a'+round))
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for i, id := range []uuid.UUID{a.ID, b.ID} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = d.RenameHost(p.ctx, id, name)
			}()
		}
		close(start)
		wg.Wait()
		ok, conflict := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrConflict):
				conflict++
			default:
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if ok != 1 || conflict != 1 {
			t.Fatalf("round %d: %d renamed, %d conflicts (%v)", round, ok, conflict, errs)
		}
		var holders int
		if err := d.pool.QueryRow(p.ctx, `SELECT count(*) FROM app.hosts WHERE org_id = $1::uuid AND name = $2`, org.String(), name).Scan(&holders); err != nil || holders != 1 {
			t.Fatalf("round %d: %d hosts named %s %v", round, holders, name, err)
		}
	}
}

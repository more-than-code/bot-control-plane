package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

const notificationMigration = "0024_notification_reads.sql"

// TestParityNotificationReads runs the read-mark rules (api-v1 v1.37 item 3)
// against memstore and, with a test database, Postgres.
func TestParityNotificationReads(t *testing.T) {
	runParity(t, func(p parity) {
		u, other := p.user("nr"), p.user("nro")
		keys := []string{"release:0.2.40", "host_update:" + uuid.NewString() + ":0.3.0"}
		got, err := p.st.NotificationReads(p.ctx, u.ID, keys)
		if err != nil || len(got) != 0 {
			p.t.Fatalf("empty %v %v", got, err)
		}
		// Duplicates in one call are one mark.
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, []string{keys[0], keys[0], "unknown:x"}, nil, p.at); err != nil {
			p.t.Fatal(err)
		}
		got, err = p.st.NotificationReads(p.ctx, u.ID, append(keys, "unknown:x"))
		if err != nil || len(got) != 2 || !got[keys[0]] || !got["unknown:x"] || got[keys[1]] {
			p.t.Fatalf("marked %v %v", got, err)
		}
		// Marks are per user.
		if got, _ := p.st.NotificationReads(p.ctx, other.ID, keys); len(got) != 0 {
			p.t.Fatalf("other user %v", got)
		}
		// Marking again is an upsert; a nil or empty key list reads nothing.
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, keys, nil, p.at.Add(time.Minute)); err != nil {
			p.t.Fatal(err)
		}
		if got, err := p.st.NotificationReads(p.ctx, u.ID, nil); err != nil || len(got) != 0 {
			p.t.Fatalf("nil keys %v %v", got, err)
		}
		// A write prunes this user's marks older than 90 days.
		if err := p.st.MarkNotificationsRead(p.ctx, other.ID, []string{"old:1"}, nil, p.at.Add(-91*24*time.Hour)); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, []string{"old:1"}, nil, p.at.Add(-91*24*time.Hour)); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, []string{"recent:1"}, nil, p.at.Add(-89*24*time.Hour)); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, []string{"fresh:1"}, nil, p.at); err != nil {
			p.t.Fatal(err)
		}
		got, _ = p.st.NotificationReads(p.ctx, u.ID, []string{"old:1", "recent:1", "fresh:1"})
		if got["old:1"] || !got["recent:1"] || !got["fresh:1"] {
			p.t.Fatalf("prune %v", got)
		}
		if got, _ := p.st.NotificationReads(p.ctx, other.ID, []string{"old:1"}); !got["old:1"] {
			p.t.Fatal("prune touched another user")
		}
		// An old mark whose key is still a current item survives a write;
		// an old mark for a key that is gone is pruned.
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, []string{"still:1", "gone:1"}, nil, p.at.Add(-100*24*time.Hour)); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, []string{"fresh:2"}, []string{"still:1", "fresh:2"}, p.at); err != nil {
			p.t.Fatal(err)
		}
		got, _ = p.st.NotificationReads(p.ctx, u.ID, []string{"still:1", "gone:1", "fresh:2"})
		if !got["still:1"] || got["gone:1"] || !got["fresh:2"] {
			p.t.Fatalf("keep %v", got)
		}
		// Bad keys and unknown users.
		for _, bad := range [][]string{{""}, {strings.Repeat("k", 201)}, {"a\x00b"}, {"a\x1fb"}, {"a\x7fb"}, {"a\xffb"}} {
			if err := p.st.MarkNotificationsRead(p.ctx, u.ID, bad, nil, p.at); !errors.Is(err, store.ErrInvalid) {
				p.t.Fatalf("bad key %v", err)
			}
		}
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, []string{strings.Repeat("é", 200)}, nil, p.at); err != nil {
			p.t.Fatalf("200 characters %v", err)
		}
		if err := p.st.MarkNotificationsRead(p.ctx, uuid.New(), []string{"a"}, nil, p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("unknown user %v", err)
		}
	})
}

// TestParityNotificationReadsCap: a write keeps at most 500 marks per user,
// deleting the oldest (by read_at, then key) but never a key in keep.
func TestParityNotificationReadsCap(t *testing.T) {
	runParity(t, func(p parity) {
		u, other := p.user("nc"), p.user("nco")
		old := make([]string, 500)
		for i := range old {
			old[i] = fmt.Sprintf("k%03d", i)
		}
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, old, nil, p.at.Add(-10*time.Minute)); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.MarkNotificationsRead(p.ctx, other.ID, []string{"k000"}, nil, p.at.Add(-time.Hour)); err != nil {
			p.t.Fatal(err)
		}
		all := append(append([]string{}, old...), "new:1", "new:2")
		if got, err := p.st.NotificationReads(p.ctx, u.ID, all); err != nil || len(got) != 500 {
			p.t.Fatalf("500 marks %d %v", len(got), err)
		}
		// 502 marks: the two oldest are k000 and k001 (same read_at, key
		// order); k000 is in keep, so only k001 goes.
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, []string{"new:1", "new:2"}, []string{"k000"}, p.at); err != nil {
			p.t.Fatal(err)
		}
		got, err := p.st.NotificationReads(p.ctx, u.ID, all)
		if err != nil || len(got) != 501 || !got["k000"] || got["k001"] || !got["k002"] || !got["new:1"] || !got["new:2"] {
			p.t.Fatalf("cap %d k000=%v k001=%v k002=%v %v", len(got), got["k000"], got["k001"], got["k002"], err)
		}
		// Without keep, the next write trims back to 500 from the oldest.
		if err := p.st.MarkNotificationsRead(p.ctx, u.ID, []string{"new:3"}, nil, p.at.Add(time.Minute)); err != nil {
			p.t.Fatal(err)
		}
		got, _ = p.st.NotificationReads(p.ctx, u.ID, append(all, "new:3"))
		if len(got) != 500 || got["k000"] || got["k002"] || !got["k003"] || !got["new:3"] {
			p.t.Fatalf("trim %d k000=%v k002=%v k003=%v", len(got), got["k000"], got["k002"], got["k003"])
		}
		if got, _ := p.st.NotificationReads(p.ctx, other.ID, []string{"k000"}); !got["k000"] {
			p.t.Fatal("cap touched another user")
		}
	})
}

// TestParityMeProjectOrgName: every reachable project carries its org's
// name, also for a project member who is not in the org (v1.37 item 4).
func TestParityMeProjectOrgName(t *testing.T) {
	runParity(t, func(p parity) {
		owner, guest := p.user("on"), p.user("og")
		org := p.org(owner.ID)
		if _, err := p.st.RenameOrg(p.ctx, org, "Org "+p.tag); err != nil {
			p.t.Fatal(err)
		}
		pr := p.project(org, owner.ID, "onp")
		if _, err := p.st.AddMember(p.ctx, pr.ID, guest.ID, "viewer", owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		for _, u := range []store.User{owner, guest} {
			items, err := p.st.ListAllProjects(p.ctx, u.ID)
			if err != nil {
				p.t.Fatal(err)
			}
			found := false
			for _, it := range items {
				if it.ID == pr.ID {
					found = it.OrgName == "Org "+p.tag
				}
			}
			if !found {
				p.t.Fatalf("%s: org name missing %+v", u.Email, items)
			}
		}
	})
}

func TestNotificationReadsMigrationText(t *testing.T) {
	if !numberedSQL(notificationMigration) || numberedSQL("0024_notification_reads.down.sql") {
		t.Fatal("0024 numbering")
	}
	body, err := migrations.FS.ReadFile(notificationMigration)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"CREATE TABLE app.notification_reads",
		"PRIMARY KEY (user_id, key)",
		"REFERENCES app.users (id) ON DELETE CASCADE",
		"CHECK (char_length(key) BETWEEN 1 AND 200)",
		"REVOKE ALL ON TABLE app.notification_reads FROM deploybot_api",
		"GRANT UPDATE (read_at) ON TABLE app.notification_reads TO deploybot_api",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0024 missing %s", want)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "--") && strings.ContainsAny(trim, ";'") {
			t.Fatalf("comment contains a semicolon or an apostrophe: %s", trim)
		}
	}
	down, err := migrations.FS.ReadFile("0024_notification_reads.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	stmts := splitSQL(string(down))
	if len(stmts) != 3 || !strings.Contains(stmts[1], "DROP TABLE IF EXISTS app.notification_reads") ||
		!strings.Contains(string(down), "DELETE FROM app.schema_migrations WHERE name = '"+notificationMigration+"'") ||
		!strings.HasSuffix(strings.TrimSpace(string(down)), "COMMIT;") {
		t.Fatalf("down script %v", stmts)
	}
}

func notificationTable(t *testing.T, d *DB, ctx context.Context) int {
	t.Helper()
	var n int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.tables
WHERE table_schema = 'app' AND table_name = 'notification_reads'
`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestNotificationReadsMigration is up with the deploybot_api grants, a
// user delete cascading to the marks, down with marks present (read marks
// are disposable, so down does not refuse), and up again.
func TestNotificationReadsMigration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	lockMigrations(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { restoreNotificationMigration(url) })
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer d.Close()
	if !onTestDatabase(ctx, d) {
		t.Fatal("refusing a database that is not deploybot_test or deploybot_test_*")
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if notificationTable(t, d, ctx) != 1 {
		t.Fatal("0024 table missing")
	}
	assertTablePrivs(t, d, ctx, "notification_reads", true, true, false, true, []string{"read_at"})

	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	u := catalogUser(t, d, ctx, "nr-mig-"+tag, at)
	if err := d.MarkNotificationsRead(ctx, u.ID, []string{"release:0.2.40"}, nil, at); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `INSERT INTO app.notification_reads (user_id, key, read_at) VALUES ($1::uuid, '', now())`, u.ID.String()); err == nil {
		t.Fatal("empty key accepted")
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET ROLE deploybot_owner`); err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("0024_notification_reads.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQL(string(down)) {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			_, _ = conn.Exec(ctx, "ROLLBACK")
			t.Fatalf("down: %v", err)
		}
	}
	if notificationTable(t, d, ctx) != 0 {
		t.Fatal("down left the table")
	}
	if _, err := conn.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '`+notificationMigration+`'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("restore migrate: %v", err)
	}
	if notificationTable(t, d, ctx) != 1 {
		t.Fatal("up again")
	}
	assertTablePrivs(t, d, ctx, "notification_reads", true, true, false, true, []string{"read_at"})
}

// TestNotificationReadsCascade: deleting a user removes their marks.
func TestNotificationReadsCascade(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer d.Close()
	if !onTestDatabase(ctx, d) {
		t.Fatal("refusing a database that is not deploybot_test or deploybot_test_*")
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var fk string
	if err := d.pool.QueryRow(ctx, `
SELECT confdeltype::text FROM pg_constraint
WHERE conrelid = 'app.notification_reads'::regclass AND contype = 'f'
`).Scan(&fk); err != nil || fk != "c" {
		t.Fatalf("user fk on delete %q %v", fk, err)
	}
}

// restoreNotificationMigration re-applies 0024 when a failed run left it
// rolled back.
func restoreNotificationMigration(url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, err := Open(ctx, url)
	if err != nil {
		return
	}
	defer d.Close()
	if !onTestDatabase(ctx, d) {
		return
	}
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'app' AND table_name = 'notification_reads'`).Scan(&n); err != nil || n == 1 {
		return
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '`+notificationMigration+`'`); err != nil {
		return
	}
	_ = Migrate(ctx, url)
}

// TestParityHostUpdateNotices: the first notice of a host behind a version
// keeps its time; a newer version starts a new one; unknown hosts are
// skipped (migration 0035).
func TestParityHostUpdateNotices(t *testing.T) {
	runParity(t, func(p parity) {
		u := p.user("hun")
		org := p.org(u.ID)
		a, b := p.enroll(org, nil, u.ID, "hun-a"), p.enroll(org, nil, u.ID, "hun-b")
		ghost := uuid.New()
		got, err := p.st.NoticeHostUpdates(p.ctx, []uuid.UUID{a.ID, ghost}, "0.3.0", p.at)
		if err != nil || len(got) != 1 || !got[a.ID].Equal(p.at) {
			p.t.Fatalf("first %v %v", got, err)
		}
		later := p.at.Add(time.Hour)
		got, err = p.st.NoticeHostUpdates(p.ctx, []uuid.UUID{a.ID, b.ID}, "0.3.0", later)
		if err != nil || len(got) != 2 || !got[a.ID].Equal(p.at) || !got[b.ID].Equal(later) {
			p.t.Fatalf("kept %v %v", got, err)
		}
		// A newer version is noticed anew, and the old row goes.
		got, err = p.st.NoticeHostUpdates(p.ctx, []uuid.UUID{a.ID}, "0.3.1", later)
		if err != nil || !got[a.ID].Equal(later) {
			p.t.Fatalf("newer %v %v", got, err)
		}
		got, err = p.st.NoticeHostUpdates(p.ctx, []uuid.UUID{a.ID}, "0.3.0", later.Add(time.Hour))
		if err != nil || !got[a.ID].Equal(later.Add(time.Hour)) {
			p.t.Fatalf("old row not deleted %v %v", got, err)
		}
		if got, err := p.st.NoticeHostUpdates(p.ctx, nil, "0.3.0", p.at); err != nil || len(got) != 0 {
			p.t.Fatalf("none %v %v", got, err)
		}
	})
}

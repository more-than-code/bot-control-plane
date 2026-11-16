package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

func TestFileRequestsMigrationText(t *testing.T) {
	body, err := migrations.FS.ReadFile("0020_file_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"CREATE TABLE app.file_requests",
		"REFERENCES app.users (id) ON DELETE CASCADE",
		"REFERENCES app.projects (id) ON DELETE CASCADE",
		"REVOKE ALL ON TABLE app.file_requests FROM deploybot_api",
		"GRANT SELECT, INSERT, DELETE ON TABLE app.file_requests TO deploybot_api",
		"GRANT UPDATE (state, done_at, expires_at) ON TABLE app.file_requests TO deploybot_api",
		"CREATE INDEX file_requests_open_idx",
		"CREATE INDEX file_requests_expires_idx",
		"CREATE INDEX file_requests_project_idx",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0020 missing %s", want)
		}
	}
	// splitSQL does not know comments: a semicolon in one splits the file,
	// and an apostrophe opens a quote that swallows the next statements.
	for _, line := range strings.Split(text, "\n") {
		if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "--") && strings.ContainsAny(trim, ";'") {
			t.Fatalf("comment contains a semicolon or an apostrophe: %s", trim)
		}
	}
	// The table, three indexes, the revoke and two grants.
	if stmts := splitSQL(text); len(stmts) != 7 || !strings.Contains(stmts[0], "CREATE TABLE app.file_requests") {
		t.Fatalf("0020 splits into %d statements, want 7", len(stmts))
	}
	// No content column (api-v1 v1.30 item 8).
	for _, col := range []string{"content", "nonce", "cipher", "plain"} {
		if strings.Contains(strings.ToLower(strings.SplitN(text, "CREATE TABLE", 2)[1]), "\n    "+col) {
			t.Fatalf("0020 has a %q column", col)
		}
	}
	down, err := migrations.FS.ReadFile("0020_file_requests.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	if !strings.Contains(downText, "DELETE FROM app.schema_migrations WHERE name = '0020_file_requests.sql'") ||
		!strings.Contains(downText, "DROP TABLE IF EXISTS app.file_requests;") ||
		!strings.Contains(downText, "\nBEGIN;") || !strings.HasSuffix(strings.TrimSpace(downText), "COMMIT;") {
		t.Fatal("down script")
	}
}

// TestFileRequestsMigration runs 0020 up, down, and up again on a test database.
func TestFileRequestsMigration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { restoreFileRequests(url) })
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
	assertFileRequestTable(t, d, ctx, true)
	assertTablePrivs(t, d, ctx, "file_requests", true, true, false, true, []string{"done_at", "expires_at", "state"})

	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "fr-mig-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "FRMig", "frmig"+tag, at)
	_, hash, _ := secret.NewToken()
	if err := d.CreateFileRequest(ctx, hash, store.FileRequest{RequestMeta: store.RequestMeta{UserID: owner.ID, ProjectID: home.ID, CreatedAt: at, ExpiresAt: at.Add(store.RequestTTL)}, Name: "a.txt", Secret: true}); err != nil {
		t.Fatal(err)
	}
	// The CHECKs refuse a stored expired state, a pending row with done_at, and a bad name.
	if _, err := d.pool.Exec(ctx, `UPDATE app.file_requests SET state = 'expired' WHERE id_hash = $1`, hash); !checkViolation(err) {
		t.Fatalf("stored expired %v", err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE app.file_requests SET done_at = now() WHERE id_hash = $1`, hash); !checkViolation(err) {
		t.Fatalf("pending done_at %v", err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE app.file_requests SET name = 'Bad Name' WHERE id_hash = $1`, hash); !checkViolation(err) {
		t.Fatalf("bad name %v", err)
	}
	// A filled file stays after the down script.
	nonce, ciphertext, err := testFileBox(t).Seal(seal.FileAAD(home.ID.String(), "a.txt"), []byte("kept"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.FillFileRequest(ctx, hash, owner.ID, at, sealedWrite("kept", nonce, ciphertext)); err != nil {
		t.Fatal(err)
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET ROLE deploybot_owner`); err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("0020_file_requests.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQL(string(down)) {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	assertFileRequestTable(t, d, ctx, false)
	if files, err := d.ListProjectFiles(ctx, home.ID); err != nil || len(files) != 1 {
		t.Fatalf("files after down %#v %v", files, err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '0020_file_requests.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("restore migrate: %v", err)
	}
	assertFileRequestTable(t, d, ctx, true)
	assertTablePrivs(t, d, ctx, "file_requests", true, true, false, true, []string{"done_at", "expires_at", "state"})
}

func assertFileRequestTable(t *testing.T, d *DB, ctx context.Context, applied bool) {
	t.Helper()
	var table *string
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('app.file_requests')::text`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if applied != (table != nil) {
		t.Fatalf("0020 applied %v, table %v", applied, table)
	}
	if !applied {
		return
	}
	var cols int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'app' AND table_name = 'file_requests'
  AND column_name IN ('content', 'content_plain', 'content_cipher', 'nonce', 'id')
`).Scan(&cols); err != nil || cols != 0 {
		t.Fatalf("content or id columns %d %v", cols, err)
	}
}

// restoreFileRequests re-applies 0020 when a failed run left it rolled back.
func restoreFileRequests(url string) {
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
	var table *string
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('app.file_requests')::text`).Scan(&table); err != nil || table != nil {
		return
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '0020_file_requests.sql'`); err != nil {
		return
	}
	_ = Migrate(ctx, url)
}

func testFileBox(t *testing.T) *seal.Box {
	t.Helper()
	box, err := seal.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func shaOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func sealedWrite(content string, nonce, ciphertext []byte) store.FileWrite {
	return store.FileWrite{IsSecret: true, Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion, SHA256: shaOf(content), Size: len(content)}
}

func plainWrite(content string) store.FileWrite {
	return store.FileWrite{Plain: content, SHA256: shaOf(content), Size: len(content)}
}

// TestParityFileRequests is api-v1 v1.30 items 2 and 5–7 in both stores.
func TestParityFileRequests(t *testing.T) {
	box := testFileBox(t)
	runParity(t, func(p parity) {
		t := p.t
		owner, other := p.user("fro"), p.user("frx")
		proj := p.project(p.org(owner.ID), owner.ID, "fr")
		sealed := func(name, content string) store.FileWrite {
			nonce, ciphertext, err := box.Seal(seal.FileAAD(proj.ID.String(), name), []byte(content))
			if err != nil {
				t.Fatal(err)
			}
			return sealedWrite(content, nonce, ciphertext)
		}
		create := func(user uuid.UUID, name string, at time.Time) ([]byte, error) {
			_, hash, _ := secret.NewToken()
			return hash, p.st.CreateFileRequest(p.ctx, hash, store.FileRequest{
				RequestMeta: store.RequestMeta{UserID: user, ProjectID: proj.ID, Reason: "why", CreatedAt: at, ExpiresAt: at.Add(store.RequestTTL)},
				Name:        name,
				Secret:      true,
			})
		}
		mustCreate := func(name string) []byte {
			h, err := create(owner.ID, name, p.at)
			if err != nil {
				t.Fatal(err)
			}
			return h
		}
		if _, err := p.st.PutProjectFile(p.ctx, proj.ID, "taken.txt", plainWrite("x"), owner.ID, p.at); err != nil {
			t.Fatal(err)
		}

		// Create rules.
		if _, err := create(owner.ID, "taken.txt", p.at); !errors.Is(err, store.ErrFileExists) {
			t.Fatalf("exists %v", err)
		}
		if _, err := create(owner.ID, "Bad", p.at); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("bad name %v", err)
		}
		h := mustCreate("key.pem")
		got, err := p.st.GetFileRequest(p.ctx, h)
		if err != nil || got.UserID != owner.ID || got.Name != "key.pem" || !got.Secret || got.Reason != "why" || got.State != store.RequestPending || got.DoneAt != nil {
			t.Fatalf("get %#v %v", got, err)
		}
		if _, err := p.st.GetFileRequest(p.ctx, secret.Hash("unknown")); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown %v", err)
		}

		// Fill: another user is not_found; the owner writes a sealed file.
		if err := p.st.FillFileRequest(p.ctx, h, other.ID, p.at, sealed("key.pem", "pem")); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other user %v", err)
		}
		if err := p.st.FillFileRequest(p.ctx, h, owner.ID, p.at, sealed("key.pem", "pem")); err != nil {
			t.Fatalf("fill %v", err)
		}
		item, err := p.st.GetProjectFile(p.ctx, proj.ID, "key.pem")
		if err != nil || !item.IsSecret || item.SHA256 != nil || item.Size != 3 || item.Name != "key.pem" {
			t.Fatalf("fill %#v %v", item, err)
		}
		mats, err := p.st.ListFileMaterial(p.ctx, proj.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range mats {
			if m.Name == "key.pem" {
				plain, err := box.Open(seal.FileAAD(proj.ID.String(), "key.pem"), m.Nonce, m.Ciphertext)
				if err != nil || string(plain) != "pem" || m.Plain != "" {
					t.Fatalf("open %v", err)
				}
			}
		}
		if got, _ := p.st.GetFileRequest(p.ctx, h); got.State != store.RequestFilled || got.DoneAt == nil {
			t.Fatalf("after fill %#v", got)
		}
		if err := p.st.FillFileRequest(p.ctx, h, owner.ID, p.at, sealed("key.pem", "again")); !errors.Is(err, store.ErrRequestUsed) {
			t.Fatalf("second fill %v", err)
		}
		if err := p.st.DeclineFileRequest(p.ctx, h, owner.ID, p.at); !errors.Is(err, store.ErrRequestUsed) {
			t.Fatalf("decline filled %v", err)
		}

		// A plain fill.
		pl := mustCreate("app.conf")
		if err := p.st.FillFileRequest(p.ctx, pl, owner.ID, p.at, plainWrite("listen 80")); err != nil {
			t.Fatalf("plain fill %v", err)
		}
		if item, err := p.st.GetProjectFile(p.ctx, proj.ID, "app.conf"); err != nil || item.IsSecret || item.SHA256 == nil || item.Plain != "listen 80" {
			t.Fatalf("plain file %#v %v", item, err)
		}
		// ProjectFileExists reads no content.
		if ok, err := p.st.ProjectFileExists(p.ctx, proj.ID, "app.conf"); !ok || err != nil {
			t.Fatalf("exists %v %v", ok, err)
		}
		if ok, err := p.st.ProjectFileExists(p.ctx, proj.ID, "nope.conf"); ok || err != nil {
			t.Fatalf("absent %v %v", ok, err)
		}

		// Expiry: at the expiry time the request is expired.
		e := mustCreate("late.txt")
		late := p.at.Add(store.RequestTTL)
		if err := p.st.FillFileRequest(p.ctx, e, owner.ID, late, plainWrite("late")); !errors.Is(err, store.ErrRequestExpired) {
			t.Fatalf("expired fill %v", err)
		}
		if err := p.st.DeclineFileRequest(p.ctx, e, owner.ID, late); !errors.Is(err, store.ErrRequestExpired) {
			t.Fatalf("expired decline %v", err)
		}

		// A file created meanwhile: ErrFileExists, request stays pending, file untouched.
		m := mustCreate("meanwhile.txt")
		if _, err := p.st.PutProjectFile(p.ctx, proj.ID, "meanwhile.txt", plainWrite("console"), owner.ID, p.at); err != nil {
			t.Fatal(err)
		}
		if err := p.st.FillFileRequest(p.ctx, m, owner.ID, p.at, plainWrite("agent")); !errors.Is(err, store.ErrFileExists) {
			t.Fatalf("meanwhile %v", err)
		}
		if got, _ := p.st.GetFileRequest(p.ctx, m); got.State != store.RequestPending {
			t.Fatalf("meanwhile state %s", got.State)
		}
		if f, _ := p.st.GetProjectFile(p.ctx, proj.ID, "meanwhile.txt"); f.Plain != "console" {
			t.Fatal("console file replaced")
		}

		// Fill and decline race: exactly one wins.
		race := mustCreate("race.txt")
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(fill bool) {
				defer wg.Done()
				var err error
				if fill {
					err = p.st.FillFileRequest(p.ctx, race, owner.ID, p.at, plainWrite("r"))
				} else {
					err = p.st.DeclineFileRequest(p.ctx, race, owner.ID, p.at)
				}
				if err == nil {
					mu.Lock()
					wins++
					mu.Unlock()
				} else if !errors.Is(err, store.ErrRequestUsed) {
					t.Errorf("race %v", err)
				}
			}(i == 0)
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("race wins %d", wins)
		}

		// Decline.
		dcl := mustCreate("decl.txt")
		if err := p.st.DeclineFileRequest(p.ctx, dcl, other.ID, p.at); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other decline %v", err)
		}
		if err := p.st.DeclineFileRequest(p.ctx, dcl, owner.ID, p.at); err != nil {
			t.Fatal(err)
		}
		if got, _ := p.st.GetFileRequest(p.ctx, dcl); got.State != store.RequestDeclined || got.DoneAt == nil {
			t.Fatalf("declined %#v", got)
		}
		if _, err := p.st.GetProjectFile(p.ctx, proj.ID, "decl.txt"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("decline wrote a file")
		}

		// Open limit per user, counted apart from value requests; replace at the limit.
		limited := p.user("frl")
		lp := p.project(p.org(limited.ID), limited.ID, "frl")
		var first []byte
		for i := 0; i < store.MaxOpenRequests; i++ {
			_, hash, _ := secret.NewToken()
			if i == 0 {
				first = hash
			}
			if err := p.st.CreateFileRequest(p.ctx, hash, store.FileRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: lp.ID, CreatedAt: p.at, ExpiresAt: p.at.Add(store.RequestTTL)}, Name: fmt.Sprintf("f%d.txt", i)}); err != nil {
				t.Fatal(err)
			}
		}
		_, hash, _ := secret.NewToken()
		over := store.FileRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: lp.ID, CreatedAt: p.at, ExpiresAt: p.at.Add(store.RequestTTL)}, Name: "over.txt"}
		if err := p.st.CreateFileRequest(p.ctx, hash, over); !errors.Is(err, store.ErrTooManyFileRequests) {
			t.Fatalf("limit %v", err)
		}
		if got, _ := p.st.GetFileRequest(p.ctx, first); got.StateAt(p.at) != store.RequestPending {
			t.Fatalf("refused create expired f0: %s", got.StateAt(p.at))
		}
		if _, err := p.st.PutEnvSet(p.ctx, lp.ID, "svc", nil, limited.ID, p.at, false); err != nil {
			t.Fatal(err)
		}
		_, vh, _ := secret.NewToken()
		if err := p.st.CreateValueRequest(p.ctx, vh, store.ValueRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: lp.ID, CreatedAt: p.at, ExpiresAt: p.at.Add(store.RequestTTL)}, Set: "svc", Key: "K"}); err != nil {
			t.Fatalf("value request counted with file requests: %v", err)
		}
		_, again, _ := secret.NewToken()
		if err := p.st.CreateFileRequest(p.ctx, again, store.FileRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: lp.ID, CreatedAt: p.at, ExpiresAt: p.at.Add(store.RequestTTL)}, Name: "f0.txt"}); err != nil {
			t.Fatalf("replace at limit %v", err)
		}
		if got, _ := p.st.GetFileRequest(p.ctx, first); got.StateAt(p.at) != store.RequestExpired {
			t.Fatalf("f0 not replaced: %s", got.StateAt(p.at))
		}
		over.CreatedAt, over.ExpiresAt = late, late.Add(store.RequestTTL)
		if err := p.st.CreateFileRequest(p.ctx, hash, over); err != nil {
			t.Fatalf("after expiry %v", err)
		}

		// Rows 7 days past expiry are removed by the next create.
		week := p.at.Add(2*store.RequestTTL + 7*24*time.Hour + time.Minute)
		_, fresh, _ := secret.NewToken()
		if err := p.st.CreateFileRequest(p.ctx, fresh, store.FileRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: lp.ID, CreatedAt: week, ExpiresAt: week.Add(store.RequestTTL)}, Name: "fresh.txt"}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.st.GetFileRequest(p.ctx, hash); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("old row kept %v", err)
		}

		// The 64-file limit refuses create and fill.
		capped := mustCreate("cap.txt")
		files, err := p.st.ListProjectFiles(p.ctx, proj.ID)
		if err != nil {
			t.Fatal(err)
		}
		for i := len(files); i < store.MaxProjectFiles; i++ {
			if _, err := p.st.PutProjectFile(p.ctx, proj.ID, fmt.Sprintf("pad%02d.txt", i), plainWrite("x"), owner.ID, p.at); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := create(owner.ID, "nope.txt", p.at); !errors.Is(err, store.ErrTooManyFiles) {
			t.Fatalf("create at cap %v", err)
		}
		if err := p.st.FillFileRequest(p.ctx, capped, owner.ID, p.at, plainWrite("x")); !errors.Is(err, store.ErrTooManyFiles) {
			t.Fatalf("fill at cap %v", err)
		}

		// A fill racing a delete or a console put of the same file: no
		// deadlock and no error beyond the expected ones, and the file
		// rows stay consistent (one row or none).
		rp := p.project(p.org(limited.ID), limited.ID, "frr")
		for i := 0; i < 5; i++ {
			name := fmt.Sprintf("race%d.txt", i)
			_, rh, _ := secret.NewToken()
			if err := p.st.CreateFileRequest(p.ctx, rh, store.FileRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: rp.ID, CreatedAt: week, ExpiresAt: week.Add(store.RequestTTL)}, Name: name}); err != nil {
				t.Fatal(err)
			}
			var fillErr, otherErr error
			var rw sync.WaitGroup
			rw.Add(2)
			go func() {
				defer rw.Done()
				fillErr = p.st.FillFileRequest(p.ctx, rh, limited.ID, week, plainWrite("from-request"))
			}()
			go func() {
				defer rw.Done()
				if i%2 == 0 {
					otherErr = p.st.DeleteProjectFile(p.ctx, rp.ID, name)
				} else {
					_, otherErr = p.st.PutProjectFile(p.ctx, rp.ID, name, plainWrite("from-console"), limited.ID, week)
				}
			}()
			rw.Wait()
			if i%2 == 0 {
				// Delete before the fill: not found, the fill writes. After: the fill's file is deleted.
				if fillErr != nil || (otherErr != nil && !errors.Is(otherErr, store.ErrNotFound)) {
					t.Fatalf("fill/delete race fill %v delete %v", fillErr, otherErr)
				}
			} else {
				// Put first: the fill answers ErrFileExists. Fill first: the put replaces.
				if otherErr != nil || (fillErr != nil && !errors.Is(fillErr, store.ErrFileExists)) {
					t.Fatalf("fill/put race fill %v put %v", fillErr, otherErr)
				}
				got, _ := p.st.GetFileRequest(p.ctx, rh)
				if (fillErr == nil) != (got.State == store.RequestFilled) {
					t.Fatalf("state %s after fill %v", got.State, fillErr)
				}
			}
		}

		// Deleting the project hides its requests.
		if err := p.st.DeleteProject(p.ctx, lp.ID, week); err != nil {
			t.Fatal(err)
		}
		if _, err := p.st.GetFileRequest(p.ctx, fresh); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("after project delete %v", err)
		}
	})
}

// TestFileRequestsAsAPIRole runs create, replace, prune, fill and decline
// through a deploybot_api login, the way the server connects, so a missing
// grant fails here. DEPLOYBOT_TEST_API_DATABASE_URL is that login on the
// same test database; with the main URL set it is required (0 SKIP gate).
func TestFileRequestsAsAPIRole(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	apiURL := os.Getenv("DEPLOYBOT_TEST_API_DATABASE_URL")
	if apiURL == "" {
		t.Fatal("DEPLOYBOT_TEST_API_DATABASE_URL is not set: give the deploybot_api login on the same test database")
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
	api, err := Open(ctx, apiURL)
	if err != nil {
		t.Fatal("api login connect failed")
	}
	defer api.Close()
	var role, dbName, mainDB string
	if err := api.pool.QueryRow(ctx, `SELECT current_user, current_database()`).Scan(&role, &dbName); err != nil {
		t.Fatal(err)
	}
	if err := d.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&mainDB); err != nil {
		t.Fatal(err)
	}
	if role != "deploybot_api" || dbName != mainDB {
		t.Fatalf("api login is %s on %s, want deploybot_api on %s", role, dbName, mainDB)
	}

	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "fr-api-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "FRApi", "frapi"+tag, at)
	create := func(name string, when time.Time) []byte {
		t.Helper()
		_, hash, _ := secret.NewToken()
		if err := api.CreateFileRequest(ctx, hash, store.FileRequest{RequestMeta: store.RequestMeta{UserID: owner.ID, ProjectID: home.ID, CreatedAt: when, ExpiresAt: when.Add(store.RequestTTL)}, Name: name, Secret: true}); err != nil {
			t.Fatalf("create %s as deploybot_api: %v", name, err)
		}
		return hash
	}
	first := create("key.pem", at)
	second := create("key.pem", at) // replaces first: UPDATE expires_at
	if got, _ := api.GetFileRequest(ctx, first); got.StateAt(at) != store.RequestExpired {
		t.Fatalf("first not replaced: %s", got.StateAt(at))
	}
	nonce, ciphertext, err := testFileBox(t).Seal(seal.FileAAD(home.ID.String(), "key.pem"), []byte("pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := api.FillFileRequest(ctx, second, owner.ID, at, sealedWrite("pem", nonce, ciphertext)); err != nil {
		t.Fatalf("fill as deploybot_api: %v", err)
	}
	other := create("other.txt", at)
	if err := api.DeclineFileRequest(ctx, other, owner.ID, at); err != nil {
		t.Fatalf("decline as deploybot_api: %v", err)
	}
	// The prune of rows 7 days past expiry (DELETE ... FOR UPDATE SKIP LOCKED).
	later := at.Add(2*store.RequestTTL + 7*24*time.Hour + time.Minute)
	create("later.txt", later)
	if _, err := api.GetFileRequest(ctx, first); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old row kept as deploybot_api: %v", err)
	}
}

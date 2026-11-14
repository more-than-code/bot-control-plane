package db

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// testDBLockKey serializes whole test-package runs on one database (review
// D26). Two `go test` processes against deploybot_test_orgs would otherwise
// run each other's down scripts mid-migration. It differs from
// migrateLockKey, which Migrate takes on its own connection.
const testDBLockKey int64 = 0x64627465737473 // "dbtests"

// migrationMu serializes, inside this package, the tests that roll a
// migration back and forward (their down scripts change the shared schema).
// Tests here are not parallel; the mutex keeps it so if one becomes parallel.
var migrationMu sync.Mutex

func TestMain(m *testing.M) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		os.Exit(m.Run())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, "test database: connect failed")
		os.Exit(1)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, testDBLockKey); err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, "test database: cannot take the test lock")
		os.Exit(1)
	}
	cancel()
	code := m.Run()
	_ = conn.Close(context.Background())
	os.Exit(code)
}

// lockMigrations takes migrationMu for the rest of the test.
func lockMigrations(t *testing.T) {
	t.Helper()
	migrationMu.Lock()
	t.Cleanup(migrationMu.Unlock)
}

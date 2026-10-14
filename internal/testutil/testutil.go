// Package testutil holds the shared helpers for the api-first-kanban test
// suites (cmd/api and internal/mcp): bootstrapping the dedicated `kanban_test`
// database on the shared Postgres instance, and the strict-mode failure policy.
// Tests never touch the live `kanban` database that backs the coordination
// board.
package testutil

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"api-first-kanban/internal/config"
	"api-first-kanban/internal/events"
	"api-first-kanban/internal/httpapi"
	"api-first-kanban/internal/mcp"
	"api-first-kanban/internal/service"
	"api-first-kanban/internal/store"
)

// maintenanceDatabase is the always-present database used for the bootstrap
// connection: CREATE DATABASE cannot run against the database it creates.
const maintenanceDatabase = "postgres"

// duplicateDatabase is the Postgres SQLSTATE 42P04 (database already exists).
const duplicateDatabase = "42P04"

// duplicateDatabaseUnique is SQLSTATE 23505 on pg_database_datname_index, which
// a concurrent CREATE DATABASE can surface instead of 42P04.
const duplicateDatabaseUnique = "23505"

// migrationLockKey is an arbitrary advisory-lock key serializing migrations
// across the concurrent test packages (go test runs packages in parallel) so
// two goose `up` runs cannot race on a fresh database.
const migrationLockKey int64 = 0x6b616e62616e // "kanban"

// errUnreachable marks a bootstrap failure caused by the Postgres instance
// being unreachable (as opposed to a privilege or naming error), so OpenPool
// can apply the skip/strict-fail policy without hiding real bootstrap bugs.
var errUnreachable = errors.New("test postgres instance unreachable")

// Strict reports whether the suite must FAIL (rather than SKIP) when the test
// database is unreachable. Enable it with the env var KANBAN_TEST_STRICT=1
// (`KANBAN_TEST_STRICT=1 make test`); the default is to skip.
func Strict() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KANBAN_TEST_STRICT"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Unavailable reports an unusable test database. In strict mode
// (KANBAN_TEST_STRICT=1) it FAILS the test; otherwise it SKIPS it so the suite
// still passes on a machine without a database. It never returns.
func Unavailable(t *testing.T, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if Strict() {
		t.Fatalf("KANBAN_TEST_STRICT=1: %s", msg)
	}
	t.Skipf("%s (set KANBAN_TEST_STRICT=1 to fail instead of skip)", msg)
}

// OpenPool opens the dedicated test database (KANBAN_TEST_DATABASE_URL,
// defaulting to `kanban_test` on the shared instance), creating it first when
// absent, verifying it is reachable, and applying migrations. When the
// instance is down it skips — or fails in strict mode. Tests still tag every
// row they create with a unique id because the database is shared across
// concurrent test lanes.
//
// The DSN carries a 3s connect_timeout so an unreachable host (e.g. a
// firewalled localhost port that DROPs the SYN) lands in the skip/strict-fail
// path quickly instead of hanging until the go-test timeout.
func OpenPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	cfg := config.Load()
	dsn := withConnectTimeout(cfg.TestDatabaseURL, 3*time.Second)

	if err := bootstrapTestDatabase(ctx, dsn); err != nil {
		if errors.Is(err, errUnreachable) {
			Unavailable(t, "test database not available: %v", err)
			return nil // unreachable: Unavailable exits the test
		}
		t.Fatalf("bootstrap test database: %v", err)
	}

	pool, err := store.OpenPool(ctx, dsn)
	if err != nil {
		Unavailable(t, "test database not available: %v", err)
		return nil // unreachable: Unavailable exits the test
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		Unavailable(t, "test database not reachable: %v", err)
		return nil
	}
	if err := migrateUpLocked(ctx, pool, dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return pool
}

// migrateUpLocked applies migrations while holding a session advisory lock in
// the target database, so parallel test packages serialize instead of racing
// goose on a freshly-created database.
func migrateUpLocked(ctx context.Context, pool *pgxpool.Pool, dsn string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration-lock connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()
	return store.MigrateUp(ctx, dsn)
}

// bootstrapTestDatabase idempotently creates the database named by dsn when it
// does not exist, connecting to the maintenance database on the same instance.
// It reuses the credentials embedded in dsn (KANBAN_TEST_DATABASE_URL), so the
// role must be allowed to CREATE DATABASE; when the database already exists no
// special privilege is needed. The existence check is a plain SELECT against
// pg_database followed by CREATE DATABASE, and the duplicate-database SQLSTATE
// is tolerated so parallel test packages can race safely.
func bootstrapTestDatabase(ctx context.Context, dsn string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse test database url: %w", err)
	}
	target := cfg.Database
	if target == "" || target == maintenanceDatabase {
		return nil
	}
	admin := *cfg
	admin.Database = maintenanceDatabase
	conn, err := pgx.ConnectConfig(ctx, &admin)
	if err != nil {
		return fmt.Errorf("%w: %v", errUnreachable, err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, target,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check database %q exists: %w", target, err)
	}
	if exists {
		return nil
	}
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{target}.Sanitize()); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) &&
			(pgErr.Code == duplicateDatabase ||
				(pgErr.Code == duplicateDatabaseUnique && pgErr.ConstraintName == "pg_database_datname_index")) {
			return nil // lost a race with a parallel test package; already created
		}
		return fmt.Errorf("create database %q: %w", target, err)
	}
	return nil
}

// withConnectTimeout appends a connect_timeout (unless the DSN already sets
// one, e.g. a test overriding it) so TCP connects cannot hang forever.
func withConnectTimeout(dsn string, d time.Duration) string {
	if strings.Contains(dsn, "connect_timeout=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "connect_timeout=" + strconv.Itoa(int(d.Seconds()))
}

// NewMux mounts the REST API over pool, using hub for the SSE endpoint.
func NewMux(pool *pgxpool.Pool, hub *events.Hub) *http.ServeMux {
	mux := http.NewServeMux()
	httpapi.Register(mux, service.New(pool), hub)
	return mux
}

// NewMuxWithMCP mounts the REST API plus the stateless /mcp endpoint over pool.
func NewMuxWithMCP(pool *pgxpool.Pool, hub *events.Hub) *http.ServeMux {
	mux := NewMux(pool, hub)
	mcp.Register(mux, service.New(pool))
	return mux
}

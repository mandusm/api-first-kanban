// Command prune-events deletes events older than a retention window.
//
// events is append-only by design: the events_no_update/events_no_delete
// triggers (migrations/00001_init.sql) reject every mutation, and rows
// deliberately outlive the entities they describe (no foreign keys). Retention
// is therefore OPT-IN MANUAL MAINTENANCE — nothing prunes automatically, and
// this command is the only sanctioned path.
//
// The delete must bypass events_no_delete, so it runs in a single transaction
// that disables the trigger only for the duration of that transaction:
//
//	BEGIN
//	  ALTER TABLE events DISABLE TRIGGER events_no_delete
//	  DELETE FROM events WHERE created_at < cutoff
//	  ALTER TABLE events ENABLE TRIGGER events_no_delete
//	COMMIT
//
// ALTER TABLE ... DISABLE TRIGGER takes an ACCESS EXCLUSIVE lock that is held
// until COMMIT, so no concurrent insert can slip a row in while the trigger is
// off. A crash or error rolls the whole transaction back, restoring the
// append-only guarantee. This never runs against the live board DB in tests.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"api-first-kanban/internal/config"
	"api-first-kanban/internal/store"
)

func main() {
	days := flag.Int("days", 30, "delete events older than this many days (retention window)")
	flag.Parse()
	if err := run(context.Background(), *days); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, days int) error {
	// days < 1 would set cutoff = now and delete the ENTIRE events table in
	// one shot; require a real retention window (review P2).
	if days < 1 {
		return fmt.Errorf("days must be >= 1, got %d", days)
	}
	pool, err := store.OpenPool(ctx, config.Load().DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	cutoff := time.Now().AddDate(0, 0, -days)
	deleted, err := pruneEvents(ctx, pool, cutoff)
	if err != nil {
		return err
	}
	slog.Info("pruned events", "deleted", deleted, "cutoff", cutoff, "days", days)
	return nil
}

// pruneEvents deletes every event created before cutoff in one transaction,
// temporarily disabling the append-only delete trigger. It returns the number
// of rows deleted and is a graceful no-op (0 rows) when nothing is older than
// cutoff.
func pruneEvents(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit

	if _, err := tx.Exec(ctx, `ALTER TABLE events DISABLE TRIGGER events_no_delete`); err != nil {
		return 0, fmt.Errorf("disable events_no_delete: %w", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM events WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete events: %w", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE events ENABLE TRIGGER events_no_delete`); err != nil {
		return 0, fmt.Errorf("enable events_no_delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return tag.RowsAffected(), nil
}

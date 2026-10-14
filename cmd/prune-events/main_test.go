package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"api-first-kanban/internal/testutil"
)

// TestPruneEventsRetainsFreshRows pins the retention boundary against the
// dedicated kanban_test database (never the live board DB): events older than
// the cutoff are deleted, events inside the window survive. Rows are tagged
// with a unique type so concurrent test packages cannot skew the counts.
func TestPruneEventsRetainsFreshRows(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenPool(t)

	tag := fmt.Sprintf("prune-test-%d", time.Now().UnixNano())
	oldType := tag + "-old"
	recentType := tag + "-recent"

	insert := func(typ string, createdAt time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO events (type, payload, created_at) VALUES ($1, '{}'::jsonb, $2)`,
			typ, createdAt); err != nil {
			t.Fatalf("insert %s: %v", typ, err)
		}
	}
	count := func(typ string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE type = $1`, typ).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", typ, err)
		}
		return n
	}

	insert(oldType, time.Now().AddDate(0, 0, -40))
	insert(oldType, time.Now().AddDate(0, 0, -31))
	insert(recentType, time.Now().AddDate(0, 0, -1))

	if got := count(oldType); got != 2 {
		t.Fatalf("precondition: old rows = %d, want 2", got)
	}
	if got := count(recentType); got != 1 {
		t.Fatalf("precondition: recent rows = %d, want 1", got)
	}

	deleted, err := pruneEvents(ctx, pool, time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("pruneEvents: %v", err)
	}
	if deleted < 2 {
		t.Errorf("deleted = %d, want >= 2", deleted)
	}
	if got := count(oldType); got != 0 {
		t.Errorf("old rows after prune = %d, want 0", got)
	}
	if got := count(recentType); got != 1 {
		t.Errorf("recent rows after prune = %d, want 1 (fresh events must survive)", got)
	}

	// The append-only trigger must be re-enabled by the transaction: a direct
	// DELETE outside pruneEvents has to be rejected again.
	if _, err := pool.Exec(ctx, `DELETE FROM events WHERE type = $1`, recentType); err == nil {
		t.Errorf("direct DELETE after prune succeeded, want events_no_delete to reject it")
	}
}

// TestPruneEventsNoOp pins the graceful empty-window path: a cutoff in the far
// past matches nothing and reports 0 rows without error.
func TestPruneEventsNoOp(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenPool(t)

	got, err := pruneEvents(ctx, pool, time.Now().AddDate(0, 0, -36500))
	if err != nil {
		t.Fatalf("pruneEvents: %v", err)
	}
	if got != 0 {
		t.Errorf("deleted = %d, want 0 for a cutoff with no older events", got)
	}
}

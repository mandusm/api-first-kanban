package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"math/big"
)

// seedItemsColumn seeds a board and fills its first column with n items,
// returning the item ids.
func seedItemsColumn(t *testing.T, st boardStore, tag string, n int) (seeded, []string) {
	t.Helper()
	sd := seedBoard(t, st, tag)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
			fmt.Sprintf(`{"title":"it-%d","column_id":%q,"agent":"test"}`, i, sd.columns[0]), http.StatusCreated)
		ids = append(ids, out["item"].(map[string]any)["id"].(string))
	}
	return sd, ids
}

// TestMoveItemConcurrentNoDeadlock pins the PB-79 blocker (eng-solid2 P1-1,
// eng-solid1 P1-5): N concurrent POST /api/items/{id}/move calls on ONE board
// hit the collision renumber path (same-column index-0 moves force duplicate
// fractional positions → RenumberColumnPositions), which UPDATEs every item
// row in the column without a shared lock order → 40P01 deadlock (500 leak)
// and half-applied renumbers. The board-row FOR UPDATE anchor (queries.
// LockBoard, first-in-tx) serializes them: every request must succeed (or
// retryable 409) and final positions must stay strictly ordered, no
// duplicates.
func TestMoveItemConcurrentNoDeadlock(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	sd, items := seedItemsColumn(t, st, uniqTag(t), 6)
	col := sd.columns[0]

	const n = 5
	type result struct {
		status int
		body   string
	}
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			// Each goroutine moves a DIFFERENT item to index 0 of the SAME
			// column — every call collides fractionally and triggers the
			// renumber loop.
			req, _ := http.NewRequest("POST", "/api/items/"+items[i]+"/move",
				strings.NewReader(fmt.Sprintf(`{"column_id":%q,"index":0,"agent":"conc"}`, col)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			results <- result{status: rec.Code, body: rec.Body.String()}
		}(i)
	}
	moved := 0
	conflicts := 0
	for i := 0; i < n; i++ {
		r := <-results
		switch r.status {
		case http.StatusOK:
			moved++
		case http.StatusConflict: // would map from 40P01 — must NOT happen under the anchor
			conflicts++
		default:
			t.Fatalf("concurrent move returned %d: %s (500/40P01 leak)", r.status, r.body)
		}
	}
	if moved == 0 {
		t.Fatal("every concurrent move was rejected; serialization anchor not working")
	}
	if conflicts > 0 {
		t.Fatalf("%d/%d moves returned 409 (40P01 contention leak — anchor not serializing)", conflicts, n)
	}

	// Final state: exactly 6 items in the column, positions strictly
	// increasing with no duplicates.
	rows, err := pool.Query(context.Background(),
		`SELECT position FROM items WHERE column_id = $1::uuid ORDER BY position`, col)
	if err != nil {
		t.Fatalf("query positions: %v", err)
	}
	defer rows.Close()
	var prev *big.Rat
	count := 0
	for rows.Next() {
		var pos string
		if err := rows.Scan(&pos); err != nil {
			t.Fatalf("scan: %v", err)
		}
		r := rat(t, pos)
		if prev != nil && r.Cmp(prev) <= 0 {
			t.Fatalf("position %q not strictly above previous (duplicate/corrupt renumber)", pos)
		}
		prev = r
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if count != 6 {
		t.Fatalf("final item count = %d, want 6", count)
	}
}

// TestCreateItemConcurrentPositionsValid covers CreateItem's board-lock anchor
// (PB-79): N concurrent top-inserts into ONE column all take the collision
// renumber path; the anchor must serialize them so every create succeeds with
// a distinct, valid position (no renumber lost to a racing tx).
func TestCreateItemConcurrentPositionsValid(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedBoard(t, st, uniqTag(t))
	col := sd.columns[0]

	const n = 5
	type result struct {
		status int
		body   string
	}
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			req, _ := http.NewRequest("POST", "/api/boards/"+sd.boardID+"/items",
				strings.NewReader(fmt.Sprintf(`{"title":"conc-%d","column_id":%q,"agent":"conc"}`, i, col)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			results <- result{status: rec.Code, body: rec.Body.String()}
		}(i)
	}
	for i := 0; i < n; i++ {
		r := <-results
		if r.status != http.StatusCreated {
			t.Fatalf("concurrent create returned %d: %s (anchor not serializing)", r.status, r.body)
		}
	}

	// Final state: distinct positions, strictly ordered, count = n.
	rows, err := pool.Query(context.Background(),
		`SELECT position FROM items WHERE column_id = $1::uuid ORDER BY position`, col)
	if err != nil {
		t.Fatalf("query positions: %v", err)
	}
	defer rows.Close()
	var prev *big.Rat
	count := 0
	for rows.Next() {
		var pos string
		if err := rows.Scan(&pos); err != nil {
			t.Fatalf("scan: %v", err)
		}
		r := rat(t, pos)
		if prev != nil && r.Cmp(prev) <= 0 {
			t.Fatalf("position %q not strictly above previous (duplicate/corrupt renumber)", pos)
		}
		prev = r
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if count != n {
		t.Fatalf("final item count = %d, want %d", count, n)
	}
}

// TestMoveColumnConcurrentStress is the PB-79 companion to
// TestMoveColumnConcurrentNoDeadlock: same shape, higher contention and
// per-goroutine repeated moves to churn the renumber loop across many txs.
func TestMoveColumnConcurrentStress(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedColumnsBoard(t, st, uniqTag(t), 6)

	const rounds = 3
	const n = 5
	type result struct {
		status int
		body   string
	}
	ok := 0
	for round := 0; round < rounds; round++ {
		results := make(chan result, n)
		for i := 0; i < n; i++ {
			go func(i int) {
				req, _ := http.NewRequest("POST", "/api/columns/"+sd.columns[i]+"/move",
					strings.NewReader(`{"index":0,"agent":"conc"}`))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				results <- result{status: rec.Code, body: rec.Body.String()}
			}(i)
		}
		for i := 0; i < n; i++ {
			r := <-results
			switch r.status {
			case http.StatusOK:
				ok++
			case http.StatusConflict:
				t.Logf("contention (409) round %d trial %d — acceptable", round, i)
			default:
				t.Fatalf("round %d: concurrent move returned %d: %s (500/40P01 leak)", round, r.status, r.body)
			}
		}
	}
	if ok == 0 {
		t.Fatal("every concurrent column move was rejected; anchor not working")
	}

	// Final state: dense 0..n-1, no duplicates, exactly 6 columns.
	rows, err := pool.Query(context.Background(),
		`SELECT position FROM columns WHERE board_id = $1::uuid ORDER BY position`, sd.boardID)
	if err != nil {
		t.Fatalf("query positions: %v", err)
	}
	defer rows.Close()
	seen := map[int32]bool{}
	i := 0
	for rows.Next() {
		var pos int32
		if err := rows.Scan(&pos); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if seen[pos] {
			t.Fatalf("duplicate position %d in final state", pos)
		}
		if pos != int32(i) {
			t.Fatalf("position[%d] = %d, want %d (not dense)", i, pos, i)
		}
		seen[pos] = true
		i++
	}
	if i != 6 {
		t.Fatalf("final column count = %d, want 6", i)
	}
}

// uniqTag builds a seed tag whose board name yields a distinct key_prefix
// pool: "board-z<letters><nanos>" → initialism "BZ*…", kept off the exhausted
// shared "B*" pool the digit-only tags all collide into (no test cleanup runs
// against the shared DB, so suffixes accumulate run over run).
func uniqTag(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("z%s%x", t.Name(), nowNanos())
}

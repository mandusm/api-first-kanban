package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedColumnsBoard seeds a board with n named columns (the default 3 plus n-3
// created via the API) and returns their ids in board order.
func seedColumnsBoard(t *testing.T, st boardStore, tag string, n int) seeded {
	t.Helper()
	sd := seedBoard(t, st, tag)
	for i := len(sd.columns); i < n; i++ {
		out := st.do(t, "POST", "/api/boards/"+sd.boardID+"/columns",
			fmt.Sprintf(`{"name":"extra-%d","agent":"test"}`, i), http.StatusCreated)
		sd.columns = append(sd.columns, out["column"].(map[string]any)["id"].(string))
	}
	return sd
}

// columnOrder reads the board's column ids in position order.
func columnOrder(t *testing.T, st boardStore, boardID string) []string {
	t.Helper()
	board := st.do(t, "GET", "/api/boards/"+boardID, "", http.StatusOK)
	var ids []string
	for _, c := range board["columns"].([]any) {
		ids = append(ids, c.(map[string]any)["id"].(string))
	}
	return ids
}

// TestMoveColumnReordersAndRenumbers verifies POST /api/columns/{id}/move
// reorders columns with index 0 = first, clamps past-the-end, and keeps the
// stored positions dense 0..n-1.
func TestMoveColumnReordersAndRenumbers(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedColumnsBoard(t, st, fmt.Sprintf("%d", nowNanos()), 5)

	// Move column 3 (index 3) to index 0.
	st.do(t, "POST", "/api/columns/"+sd.columns[3]+"/move", `{"index":0,"agent":"test"}`, http.StatusOK)
	want := []string{sd.columns[3], sd.columns[0], sd.columns[1], sd.columns[2], sd.columns[4]}
	got := columnOrder(t, st, sd.boardID)
	if len(got) != len(want) {
		t.Fatalf("column count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}

	// Stored positions must be dense 0..n-1 in board order.
	rows, err := pool.Query(context.Background(),
		`SELECT position FROM columns WHERE board_id = $1::uuid ORDER BY position`, sd.boardID)
	if err != nil {
		t.Fatalf("query positions: %v", err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var pos int32
		if err := rows.Scan(&pos); err != nil {
			t.Fatalf("scan position: %v", err)
		}
		if pos != int32(i) {
			t.Fatalf("position[%d] = %d, want %d (positions not dense)", i, pos, i)
		}
		i++
	}
	if i != len(want) {
		t.Fatalf("scanned %d positions, want %d", i, len(want))
	}

	// Omitted index = 0 (move to first). Move the last column with no body.
	st.do(t, "POST", "/api/columns/"+sd.columns[4]+"/move", `{"agent":"test"}`, http.StatusOK)
	got = columnOrder(t, st, sd.boardID)
	if got[0] != sd.columns[4] {
		t.Fatalf("omitted index: first = %s, want %s", got[0], sd.columns[4])
	}

	// Past-the-end index clamps to the end.
	st.do(t, "POST", "/api/columns/"+sd.columns[0]+"/move", `{"index":99,"agent":"test"}`, http.StatusOK)
	got = columnOrder(t, st, sd.boardID)
	if got[len(got)-1] != sd.columns[0] {
		t.Fatalf("clamp: last = %s, want %s", got[len(got)-1], sd.columns[0])
	}

	// Negative index is rejected.
	st.do(t, "POST", "/api/columns/"+sd.columns[0]+"/move", `{"index":-1,"agent":"test"}`, http.StatusBadRequest)
}

// TestMoveColumnEventCarriesSiblings verifies the column.moved event payload
// carries the moved column under data.column AND the full reordered sibling
// list under data.columns (every position may change).
func TestMoveColumnEventCarriesSiblings(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedColumnsBoard(t, st, fmt.Sprintf("%d", nowNanos()), 4)

	st.do(t, "POST", "/api/columns/"+sd.columns[2]+"/move", `{"index":0,"agent":"bot-9"}`, http.StatusOK)

	var raw []byte
	err := pool.QueryRow(context.Background(),
		`SELECT payload FROM events WHERE type = 'column.moved' AND payload->>'board_id' = $1 ORDER BY id DESC LIMIT 1`,
		sd.boardID).Scan(&raw)
	if err != nil {
		t.Fatalf("load column.moved event: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["agent"] != "bot-9" {
		t.Errorf("event agent = %v, want bot-9", payload["agent"])
	}
	col, _ := payload["column"].(map[string]any)
	if col == nil || col["id"] != sd.columns[2] {
		t.Errorf("event column = %v, want id %s", payload["column"], sd.columns[2])
	}
	cols, _ := payload["columns"].([]any)
	if len(cols) != 4 {
		t.Fatalf("event columns length = %d, want 4", len(cols))
	}
	wantOrder := []string{sd.columns[2], sd.columns[0], sd.columns[1], sd.columns[3]}
	for i, c := range cols {
		cm := c.(map[string]any)
		if cm["id"] != wantOrder[i] {
			t.Errorf("event columns[%d] id = %v, want %s", i, cm["id"], wantOrder[i])
		}
		if int(cm["position"].(float64)) != i {
			t.Errorf("event columns[%d] position = %v, want %d", i, cm["position"], i)
		}
	}
}

// TestMoveColumnUnknownID verifies a malformed or unknown column id maps to
// 400 / 404 respectively.
func TestMoveColumnUnknownID(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedBoard(t, st, fmt.Sprintf("%d", nowNanos()))

	st.do(t, "POST", "/api/columns/not-a-uuid/move", `{"index":0}`, http.StatusBadRequest)
	st.do(t, "POST", "/api/columns/00000000-0000-0000-0000-000000000000/move", `{"index":0}`, http.StatusNotFound)
	_ = sd
}

// TestMoveColumnConcurrentNoDeadlock pins the eng-qa75 blocker: N concurrent
// POST /api/columns/{id}/move calls on ONE board used to deadlock (40P01 →
// 500) and leave duplicate positions, because the renumber loop took row
// locks in different orders. The board FOR UPDATE anchor serializes them: every
// request must succeed (200; contention is internally serialized, and any
// residual race surfaces as retryable 409) and final positions must stay
// dense 0..n-1.
func TestMoveColumnConcurrentNoDeadlock(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedColumnsBoard(t, st, fmt.Sprintf("%d", nowNanos()), 6)

	const n = 5
	type result struct {
		status int
		body   string
	}
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			// Each goroutine moves a DIFFERENT column to index 0 — the exact
			// shape the reviewer probed (multi-agent same-board moves).
			req, _ := http.NewRequest("POST", "/api/columns/"+sd.columns[i]+"/move",
				strings.NewReader(`{"index":0,"agent":"conc"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			results <- result{status: rec.Code, body: rec.Body.String()}
		}(i)
	}
	moved := 0
	for i := 0; i < n; i++ {
		r := <-results
		switch r.status {
		case http.StatusOK:
			moved++
		case http.StatusConflict: // retryable contention mapped from 40P01
			t.Logf("contention (409) on trial %d — acceptable, retryable", i)
		default:
			t.Fatalf("concurrent move returned %d: %s (500/40P01 leak)", r.status, r.body)
		}
	}
	if moved == 0 {
		t.Fatal("every concurrent move was rejected; serialization anchor not working")
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
			t.Fatalf("position[%d] = %d, want %d (not dense after concurrent moves)", i, pos, i)
		}
		seen[pos] = true
		i++
	}
	if i != 6 {
		t.Fatalf("final column count = %d, want 6", i)
	}
}

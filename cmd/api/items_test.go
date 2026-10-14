package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seeded names a workspace + board created by seedBoard.
type seeded struct {
	wsID    string
	boardID string
	columns []string
}

func seedBoard(t *testing.T, st boardStore, tag string) seeded {
	t.Helper()
	out := st.do(t, "POST", "/api/workspaces", `{"name":"ws-`+tag+`","agent":"test"}`, http.StatusCreated)
	ws := out["workspace"].(map[string]any)["id"].(string)
	bout := st.do(t, "POST", "/api/workspaces/"+ws+"/boards", `{"name":"board-`+tag+`","agent":"test"}`, http.StatusCreated)
	board := bout["board"].(map[string]any)
	var cols []string
	for _, c := range board["columns"].([]any) {
		cols = append(cols, c.(map[string]any)["id"].(string))
	}
	return seeded{wsID: ws, boardID: board["id"].(string), columns: cols}
}

// TestCreateItemFractionalPositionsTopInserts is the regression test for bug 1
// (renumber off-by-one): DuplicatePositionCount counted the just-inserted row,
// so every create renumbered the column to 1..n instead of keeping fractional
// midpoints. Consecutive top-inserts must stay distinct fractional values.
func TestCreateItemFractionalPositionsTopInserts(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedBoard(t, st, fmt.Sprintf("%d", nowNanos()))
	col := sd.columns[0]

	const n = 5
	positions := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
			fmt.Sprintf(`{"title":"top-%d","column_id":%q,"agent":"test"}`, i, col), http.StatusCreated)
		item := out["item"].(map[string]any)
		positions = append(positions, item["position"].(string))
	}

	// First top-insert into an empty column is "1"; each later one is a strict
	// fractional midpoint of the previous top (a renumber would make it 1..n).
	if positions[0] != "1" {
		t.Fatalf("first position = %q, want \"1\"", positions[0])
	}
	for i := 1; i < n; i++ {
		if !strings.Contains(positions[i], ".") {
			t.Errorf("position[%d] = %q; want fractional midpoint (column renumbered on every write?)", i, positions[i])
		}
		if rat(t, positions[i]).Cmp(rat(t, positions[i-1])) >= 0 {
			t.Errorf("position[%d] = %q not below previous top %q", i, positions[i], positions[i-1])
		}
	}
	if got := distinctCount(positions); got != n {
		t.Errorf("distinct positions = %d, want %d: %v", got, n, positions)
	}
}

// TestItemEventsCarryItemView is the regression test for bug 2: SSE item.*
// events emitted the raw db.Item row (closed_at, no labels) instead of the
// documented ItemView shape, crashing clients on item.labels.length.
func TestItemEventsCarryItemView(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)

	lout := st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/labels",
		`{"name":"lbl-`+tag+`","agent":"test"}`, http.StatusCreated)
	lid := lout["label"].(map[string]any)["id"].(string)

	cout := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"evt-%s","column_id":%q,"label_ids":[%q],"agent":"test"}`, tag, sd.columns[0], lid),
		http.StatusCreated)
	iid := cout["item"].(map[string]any)["id"].(string)

	created := latestItemEvent(t, pool, "item.created", sd.boardID)
	assertItemViewShape(t, "item.created", created)
	item, _ := created["item"].(map[string]any)
	labels, ok := item["labels"].([]any)
	if !ok {
		t.Errorf("item.created labels = %v, want an array", item["labels"])
	} else if len(labels) != 1 {
		t.Errorf("item.created labels = %v, want exactly the attached label", labels)
	}

	st.do(t, "PATCH", "/api/items/"+iid, `{"title":"evt-`+tag+`-u","agent":"test"}`, http.StatusOK)
	assertItemViewShape(t, "item.updated", latestItemEvent(t, pool, "item.updated", sd.boardID))

	st.do(t, "POST", "/api/items/"+iid+"/move",
		fmt.Sprintf(`{"column_id":%q,"agent":"test"}`, sd.columns[1]), http.StatusOK)
	assertItemViewShape(t, "item.moved", latestItemEvent(t, pool, "item.moved", sd.boardID))

	st.do(t, "POST", "/api/items/"+iid+"/transfer", `{"to_agent":"bob","agent":"test"}`, http.StatusOK)
	assertItemViewShape(t, "item.transferred", latestItemEvent(t, pool, "item.transferred", sd.boardID))
}

// TestMoveItemResponseCarriesRenumberedPosition is the regression test for the
// stale move response: when a collision renumbers the column, the response and
// event must carry the rewritten position, not the pre-renumber one.
func TestMoveItemResponseCarriesRenumberedPosition(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	ctx := context.Background()

	// Pin A to position "0" so moving C to the top of its column computes
	// midpoint("0","0") == "0" — a real duplicate that forces a renumber.
	aout := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"a-%s","column_id":%q,"agent":"test"}`, tag, sd.columns[0]), http.StatusCreated)
	aid := aout["item"].(map[string]any)["id"].(string)
	if _, err := pool.Exec(ctx, `UPDATE items SET position = 0 WHERE id = $1`, aid); err != nil {
		t.Fatalf("force duplicate position: %v", err)
	}

	cout := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"c-%s","column_id":%q,"agent":"test"}`, tag, sd.columns[1]), http.StatusCreated)
	cid := cout["item"].(map[string]any)["id"].(string)

	mout := st.do(t, "POST", "/api/items/"+cid+"/move",
		fmt.Sprintf(`{"column_id":%q,"index":0,"agent":"test"}`, sd.columns[0]), http.StatusOK)
	gotPos := mout["item"].(map[string]any)["position"].(string)

	// A renumbered column is 1..n, so the moved item can never end on "0".
	if gotPos == "0" {
		t.Fatalf("move response position = %q; want the renumbered value (stale pre-renumber position returned)", gotPos)
	}
	var dbPos string
	if err := pool.QueryRow(ctx, `SELECT position::text FROM items WHERE id = $1`, cid).Scan(&dbPos); err != nil {
		t.Fatalf("read moved row: %v", err)
	}
	if gotPos != dbPos {
		t.Fatalf("move response position = %q, db = %q; want equal", gotPos, dbPos)
	}

	rows, err := pool.Query(ctx, `SELECT position::text FROM items WHERE column_id = $1 ORDER BY position`, sd.columns[0])
	if err != nil {
		t.Fatalf("read column positions: %v", err)
	}
	defer rows.Close()
	var positions []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan position: %v", err)
		}
		positions = append(positions, p)
	}
	if len(positions) != 2 || positions[0] != "1" || positions[1] != "2" {
		t.Errorf("column positions = %v, want [1 2]", positions)
	}

	// The item.moved event must carry the same fresh position as the response.
	if pos, _ := latestItemEvent(t, pool, "item.moved", sd.boardID)["item"].(map[string]any)["position"].(string); pos != gotPos {
		t.Errorf("item.moved position = %q, response = %q; want equal", pos, gotPos)
	}
}

// latestItemEvent decodes the most recent item event of typ for boardID.
func latestItemEvent(t *testing.T, pool *pgxpool.Pool, typ, boardID string) map[string]any {
	t.Helper()
	var raw []byte
	err := pool.QueryRow(context.Background(),
		`SELECT payload FROM events WHERE type = $1 AND payload->'item'->>'board_id' = $2 ORDER BY id DESC LIMIT 1`,
		typ, boardID).Scan(&raw)
	if err != nil {
		t.Fatalf("load %s event: %v", typ, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode %s payload: %v", typ, err)
	}
	return payload
}

// assertItemViewShape checks a payload's item matches the REST ItemView shape:
// a labels array and a closed bool, and no raw closed_at field.
func assertItemViewShape(t *testing.T, typ string, payload map[string]any) {
	t.Helper()
	item, ok := payload["item"].(map[string]any)
	if !ok {
		t.Fatalf("%s: payload has no item object: %v", typ, payload)
	}
	if labels, ok := item["labels"]; !ok {
		t.Errorf("%s: item has no labels key", typ)
	} else if _, ok := labels.([]any); !ok {
		t.Errorf("%s: item.labels = %T (%v), want array", typ, labels, labels)
	}
	if closed, ok := item["closed"]; !ok {
		t.Errorf("%s: item has no closed key", typ)
	} else if _, ok := closed.(bool); !ok {
		t.Errorf("%s: item.closed = %T (%v), want bool", typ, closed, closed)
	}
	if _, ok := item["closed_at"]; ok {
		t.Errorf("%s: item.closed_at present; want the ItemView closed bool instead", typ)
	}
}

func rat(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("invalid decimal position %q", s)
	}
	return r
}

func distinctCount(vals []string) int {
	seen := map[string]bool{}
	for _, v := range vals {
		seen[v] = true
	}
	return len(seen)
}

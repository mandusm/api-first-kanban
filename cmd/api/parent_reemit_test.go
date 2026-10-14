package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PB-85 integration tests: SSE item.* entities carry closed_children/
// total_children, and every count-affecting mutation appends ONE item.updated
// event for the DIRECT parent with post-mutation counts computed in the SAME
// transaction (cascade depth exactly 1 — the parent's own parent is never
// re-emitted).

// itemEvents returns every event of the given type whose entity is the given
// item id, in emission order, decoded payloads.
func itemEvents(t *testing.T, pool *pgxpool.Pool, typ, itemID string) []map[string]any {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT payload FROM events WHERE type = $1 AND payload->'item'->>'id' = $2 ORDER BY id`, typ, itemID)
	if err != nil {
		t.Fatalf("query %s events: %v", typ, err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan event payload: %v", err)
		}
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("decode event payload: %v", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate events: %v", err)
	}
	return out
}

// entityOf pulls the item entity out of an event payload and asserts the
// progress fields are present (ints).
func entityOf(t *testing.T, ev map[string]any, where string) map[string]any {
	t.Helper()
	item, ok := ev["item"].(map[string]any)
	if !ok {
		t.Fatalf("%s: event has no item entity: %v", where, ev)
	}
	if _, ok := item["closed_children"].(float64); !ok {
		t.Fatalf("%s: entity closed_children missing/not int: %v", where, item["closed_children"])
	}
	if _, ok := item["total_children"].(float64); !ok {
		t.Fatalf("%s: entity total_children missing/not int: %v", item["total_children"], where)
	}
	return item
}

// counts reads closed_children/total_children off an entity.
func counts(item map[string]any) (float64, float64) {
	return item["closed_children"].(float64), item["total_children"].(float64)
}

// TestParentReEmittedPostMutationCounts is the same-tx pin (critique 3i):
// closing a child emits the child's item.updated AND one item.updated for the
// parent whose closed_children/total_children reflect the POST-mutation state,
// computed inside the same tx (not a stale pre-mutation read).
func TestParentReEmittedPostMutationCounts(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	epic := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"reemit-epic-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, col))
	epicID := epic["id"].(string)
	task1 := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"reemit-t1-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, epicID))
	task2 := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"reemit-t2-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, epicID))
	if task2["parent_item_id"] != epicID {
		t.Errorf("task2 parent = %v, want %v", task2["parent_item_id"], epicID)
	}

	// The create-with-parent events themselves: each child item.created entity
	// carries counts, and each create emitted a parent item.updated.
	for _, ev := range itemEvents(t, pool, "item.created", task1["id"].(string)) {
		if _, tc := counts(entityOf(t, ev, "item.created child")); tc != 0 {
			t.Errorf("created task entity total_children = %v, want 0", tc)
		}
	}
	parentCreated := itemEvents(t, pool, "item.updated", epicID)
	if len(parentCreated) != 2 {
		t.Fatalf("create-with-parent emitted %d parent item.updated events, want 2 (one per child)", len(parentCreated))
	}
	c, total := counts(entityOf(t, parentCreated[1], "parent re-emit after second create"))
	if c != 0 || total != 2 {
		t.Errorf("parent event after 2 creates = %v/%v, want 0/2", c, total)
	}

	// Close task1: child item.updated (closed) + parent item.updated with 1/2.
	st.do(t, "PATCH", "/api/items/"+task1["id"].(string), `{"closed":true}`, http.StatusOK)
	childEvs := itemEvents(t, pool, "item.updated", task1["id"].(string))
	if len(childEvs) != 1 {
		t.Fatalf("child emitted %d item.updated events, want 1", len(childEvs))
	}
	if got := entityOf(t, childEvs[0], "child close")["closed"].(bool); !got {
		t.Errorf("child close event entity closed = false, want true")
	}
	parentEvs := itemEvents(t, pool, "item.updated", epicID)
	if len(parentEvs) != 3 {
		t.Fatalf("parent emitted %d item.updated events after close, want 3 (2 creates + 1 close)", len(parentEvs))
	}
	last := entityOf(t, parentEvs[len(parentEvs)-1], "parent re-emit on close")
	if c, total := counts(last); c != 1 || total != 2 {
		t.Errorf("parent re-emit after close = %v/%v, want 1/2 (post-mutation, same-tx)", c, total)
	}

	// Reopen: parent event shows 0/2 again (recomputes every time).
	st.do(t, "PATCH", "/api/items/"+task1["id"].(string), `{"closed":false}`, http.StatusOK)
	parentEvs = itemEvents(t, pool, "item.updated", epicID)
	if c, total := counts(entityOf(t, parentEvs[len(parentEvs)-1], "parent re-emit on reopen")); c != 0 || total != 2 {
		t.Errorf("parent re-emit after reopen = %v/%v, want 0/2", c, total)
	}
	if len(parentEvs) != 4 {
		t.Errorf("parent emitted %d item.updated events after reopen, want 4", len(parentEvs))
	}
}

// TestParentReEmitCascadeDepth1 pins the depth: a task closing under an epic
// (itself under a milestone) re-emits the EPIC only — never the milestone.
func TestParentReEmitCascadeDepth1(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	ms := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"depth-ms-%s","column_id":%q,"kind":"milestone","agent":"test"}`, tag, col))
	epic := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"depth-epic-%s","column_id":%q,"kind":"epic","parent_item_id":%q,"agent":"test"}`, tag, col, ms["id"].(string)))
	task := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"depth-task-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, epic["id"].(string)))

	st.do(t, "PATCH", "/api/items/"+task["id"].(string), `{"closed":true}`, http.StatusOK)

	epicEvs := itemEvents(t, pool, "item.updated", epic["id"].(string))
	if len(epicEvs) != 2 {
		t.Fatalf("epic emitted %d item.updated events, want 2 (create-with-parent + close re-emit)", len(epicEvs))
	}
	if c, total := counts(entityOf(t, epicEvs[1], "epic re-emit")); c != 1 || total != 1 {
		t.Errorf("epic re-emit counts = %v/%v, want 1/1", c, total)
	}
	// Cascade depth 1: the milestone's counts did not move, and it must NOT be
	// re-emitted by a grandchild mutation.
	if msEvs := itemEvents(t, pool, "item.updated", ms["id"].(string)); len(msEvs) != 1 {
		t.Errorf("milestone received %d item.updated events, want 1 (its own create re-emit only — no grandchild cascade)", len(msEvs))
	}
}

// TestParentReEmitTitleOnlyNoEvent pins that a patch that cannot change any
// parent's counts (title-only) emits no parent re-emit.
func TestParentReEmitTitleOnlyNoEvent(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	epic := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"title-epic-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, col))
	task := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"title-task-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, epic["id"].(string)))

	st.do(t, "PATCH", "/api/items/"+task["id"].(string),
		fmt.Sprintf(`{"title":"renamed-%s","agent":"test"}`, tag), http.StatusOK)

	if got := len(itemEvents(t, pool, "item.updated", epic["id"].(string))); got != 1 {
		t.Errorf("title-only patch emitted %d parent item.updated events, want 1 (create re-emit only)", got)
	}
}

// TestParentReEmitParentChangeBothParents pins the reparent case: moving a
// child between parents emits item.updated for BOTH the old and the new
// parent, each with its post-mutation counts.
func TestParentReEmitParentChangeBothParents(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	epicA := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"rep-a-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, col))
	epicB := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"rep-b-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, col))
	task1 := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"rep-t1-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, epicA["id"].(string)))
	_ = task1
	task2 := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"rep-t2-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, epicA["id"].(string)))

	st.do(t, "PATCH", "/api/items/"+task2["id"].(string),
		fmt.Sprintf(`{"parent_item_id":%q,"agent":"test"}`, epicB["id"].(string)), http.StatusOK)

	aEvs := itemEvents(t, pool, "item.updated", epicA["id"].(string))
	bEvs := itemEvents(t, pool, "item.updated", epicB["id"].(string))
	// epicA: 2 create re-emits + 1 move-away = 3. epicB is top-level (no
	// create re-emit) and receives only the move-in re-emit = 1.
	if len(aEvs) != 3 {
		t.Errorf("old parent emitted %d item.updated events, want 3", len(aEvs))
	}
	if len(bEvs) != 1 {
		t.Errorf("new parent emitted %d item.updated events, want 1", len(bEvs))
	}
	if c, total := counts(entityOf(t, aEvs[len(aEvs)-1], "old parent after move")); c != 0 || total != 1 {
		t.Errorf("old parent counts after move = %v/%v, want 0/1 (task1 remains, open)", c, total)
	}
	if c, total := counts(entityOf(t, bEvs[len(bEvs)-1], "new parent after move")); c != 0 || total != 1 {
		t.Errorf("new parent counts after move = %v/%v, want 0/1", c, total)
	}
	// The child's own event carries the new parent_id.
	childEvs := itemEvents(t, pool, "item.updated", task2["id"].(string))
	if got := entityOf(t, childEvs[len(childEvs)-1], "moved child")["parent_item_id"]; got != epicB["id"].(string) {
		t.Errorf("moved child entity parent_item_id = %v, want %v", got, epicB["id"].(string))
	}
}

// TestParentReEmitArchiveUnarchive pins the archive path: archiving a child
// excludes it from the direct parent's counts (both numerator and
// denominator); unarchiving restores them. Each flip emits exactly one parent
// item.updated with post-mutation counts.
func TestParentReEmitArchiveUnarchive(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	epic := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"arch-epic-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, col))
	task := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"arch-task-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, epic["id"].(string)))
	st.do(t, "PATCH", "/api/items/"+task["id"].(string), `{"closed":true}`, http.StatusOK)

	st.do(t, "POST", "/api/items/"+task["id"].(string)+"/archive", ``, http.StatusOK)
	parentEvs := itemEvents(t, pool, "item.updated", epic["id"].(string))
	if len(parentEvs) != 3 { // create re-emit + close re-emit + archive re-emit
		t.Fatalf("parent emitted %d item.updated events after archive, want 3", len(parentEvs))
	}
	if c, total := counts(entityOf(t, parentEvs[2], "parent after archive")); c != 0 || total != 0 {
		t.Errorf("parent counts after archiving its only child = %v/%v, want 0/0 (archived excluded)", c, total)
	}
	// The child's own item.archived entity also carries the progress fields.
	archEvs := itemEvents(t, pool, "item.archived", task["id"].(string))
	if len(archEvs) != 1 {
		t.Fatalf("child emitted %d item.archived events, want 1", len(archEvs))
	}
	entityOf(t, archEvs[0], "item.archived entity")

	st.do(t, "POST", "/api/items/"+task["id"].(string)+"/unarchive", ``, http.StatusOK)
	parentEvs = itemEvents(t, pool, "item.updated", epic["id"].(string))
	if len(parentEvs) != 4 {
		t.Fatalf("parent emitted %d item.updated events after unarchive, want 4", len(parentEvs))
	}
	if c, total := counts(entityOf(t, parentEvs[3], "parent after unarchive")); c != 1 || total != 1 {
		t.Errorf("parent counts after unarchive = %v/%v, want 1/1", c, total)
	}
}

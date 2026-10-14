package main

import (
	"fmt"
	"net/http"
	"testing"
)

// PB-117 integration tests: a milestone/epic parent auto-transitions when its
// child starts (first child -> In Progress), finishes (all children closed ->
// Done + closed) or reverses (a child reopened/moved out -> reopened parent).
// Column roles are name-based (todo/backlog, doing/in-progress, done); "In
// Review" is never Done.

// autoGetItem re-reads an item through the API.
func autoGetItem(t *testing.T, st boardStore, id string) map[string]any {
	t.Helper()
	return itemOf(st.do(t, "GET", "/api/items/"+id, "", http.StatusOK))
}

// autoMoveChild moves an item to a column via POST /api/items/{id}/move.
func autoMoveChild(t *testing.T, st boardStore, id, columnID string) map[string]any {
	t.Helper()
	return itemOf(st.do(t, "POST", "/api/items/"+id+"/move",
		fmt.Sprintf(`{"column_id":%q,"index":0,"agent":"test"}`, columnID), http.StatusOK))
}

// autoClose flips an item's closed flag via PATCH.
func autoClose(t *testing.T, st boardStore, id string, closed bool) {
	t.Helper()
	st.do(t, "PATCH", "/api/items/"+id, fmt.Sprintf(`{"closed":%t,"agent":"test"}`, closed), http.StatusOK)
}

// autoEpicWithTasks seeds a board, a todo epic parent and two todo task
// children; returns the epic id, the two task ids and the todo/doing/done
// column ids (default board column order).
func autoEpicWithTasks(t *testing.T, st boardStore, tag string) (epicID, t1, t2, todo, doing, done string) {
	t.Helper()
	sd := seedBoard(t, st, tag)
	todo, doing, done = sd.columns[0], sd.columns[1], sd.columns[2]
	epic := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"at-epic-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, todo))
	epicID = epic["id"].(string)
	t1 = createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"at-t1-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, todo, epicID))["id"].(string)
	t2 = createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"at-t2-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, todo, epicID))["id"].(string)
	return
}

// TestAutoTransitionFirstChildStartsParent: the first child moved In Progress
// moves a Todo/Backlog milestone/epic parent In Progress (one item.moved).
func TestAutoTransitionFirstChildStartsParent(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	epicID, t1, _, todo, doing, _ := autoEpicWithTasks(t, st, tag)

	if got := autoGetItem(t, st, epicID)["column_id"]; got != todo {
		t.Fatalf("epic starts in %v, want todo %q", got, todo)
	}
	if evs := itemEvents(t, pool, "item.moved", epicID); len(evs) != 0 {
		t.Fatalf("epic had %d item.moved events before any child move, want 0", len(evs))
	}

	autoMoveChild(t, st, t1, doing)

	if got := autoGetItem(t, st, epicID)["column_id"]; got != doing {
		t.Fatalf("parent column after child start = %v, want doing %q", got, doing)
	}
	evs := itemEvents(t, pool, "item.moved", epicID)
	if len(evs) != 1 {
		t.Fatalf("parent item.moved events = %d, want 1", len(evs))
	}
	if entity := entityOf(t, evs[0], "parent auto start"); entity["column_id"] != doing {
		t.Errorf("parent move event column = %v, want doing %q", entity["column_id"], doing)
	}
}

// TestAutoTransitionAllChildrenClosedFinishesParent: once every direct child is
// closed the parent moves to Done AND closes (one item.moved carrying the final
// entity).
func TestAutoTransitionAllChildrenClosedFinishesParent(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	epicID, t1, t2, _, doing, done := autoEpicWithTasks(t, st, tag)

	autoMoveChild(t, st, t1, doing) // parent todo -> doing
	autoClose(t, st, t1, true)      // 1/2 closed: parent stays put

	if got := autoGetItem(t, st, epicID); got["column_id"] != doing || got["closed"] != false {
		t.Fatalf("parent after first close = %v/%v, want doing/open", got["column_id"], got["closed"])
	}
	before := len(itemEvents(t, pool, "item.moved", epicID))

	autoClose(t, st, t2, true) // all closed: parent -> done + closed

	got := autoGetItem(t, st, epicID)
	if got["column_id"] != done || got["closed"] != true {
		t.Fatalf("parent after all children closed = %v/%v, want done/closed", got["column_id"], got["closed"])
	}
	evs := itemEvents(t, pool, "item.moved", epicID)
	if len(evs) != before+1 {
		t.Fatalf("parent item.moved events = %d, want %d", len(evs), before+1)
	}
	entity := entityOf(t, evs[len(evs)-1], "parent auto finish")
	if entity["column_id"] != done || entity["closed"] != true {
		t.Errorf("finish event entity = %v/%v, want done/closed", entity["column_id"], entity["closed"])
	}
}

// TestAutoTransitionReopenReversesParent: reopening a child reopens a Done
// parent and moves it back In Progress.
func TestAutoTransitionReopenReversesParent(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	epicID, t1, t2, _, doing, done := autoEpicWithTasks(t, st, tag)

	autoMoveChild(t, st, t1, doing)
	autoClose(t, st, t1, true)
	autoClose(t, st, t2, true)
	if got := autoGetItem(t, st, epicID); got["column_id"] != done || got["closed"] != true {
		t.Fatalf("parent not done/closed before reopen: %v/%v", got["column_id"], got["closed"])
	}

	autoClose(t, st, t1, false) // reopen one child

	got := autoGetItem(t, st, epicID)
	if got["column_id"] != doing || got["closed"] != false {
		t.Fatalf("parent after reopen = %v/%v, want doing/open", got["column_id"], got["closed"])
	}
}

// TestAutoTransitionReopenChildMovedOutOfDone: a closed child moved out of the
// Done column also reverses a Done parent.
func TestAutoTransitionReopenChildMovedOutOfDone(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	epicID, t1, t2, _, doing, done := autoEpicWithTasks(t, st, tag)

	// Put both children in Done and closed via the auto rule, then move one to
	// Done manually so the "moved out of Done" path (not reopen) is exercised.
	autoMoveChild(t, st, t1, doing)
	autoClose(t, st, t1, true)
	autoMoveChild(t, st, t1, done)
	autoClose(t, st, t2, true)
	autoMoveChild(t, st, t2, done)
	if got := autoGetItem(t, st, epicID); got["column_id"] != done || got["closed"] != true {
		t.Fatalf("parent not done/closed before move-out: %v/%v", got["column_id"], got["closed"])
	}

	autoMoveChild(t, st, t1, doing) // still closed, but out of Done

	got := autoGetItem(t, st, epicID)
	if got["column_id"] != doing || got["closed"] != false {
		t.Fatalf("parent after child moved out of Done = %v/%v, want doing/open", got["column_id"], got["closed"])
	}
}

// TestAutoTransitionIdempotentNoOp: a second child starting In Progress when
// the parent is already In Progress emits no extra parent move; re-closing an
// already-closed child emits nothing.
func TestAutoTransitionIdempotentNoOp(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	epicID, t1, t2, _, doing, _ := autoEpicWithTasks(t, st, tag)

	autoMoveChild(t, st, t1, doing)
	after := len(itemEvents(t, pool, "item.moved", epicID))

	autoMoveChild(t, st, t2, doing) // parent already In Progress
	if got := len(itemEvents(t, pool, "item.moved", epicID)); got != after {
		t.Fatalf("parent item.moved events after second child start = %d, want %d (no-op)", got, after)
	}

	// Parent In Progress, one child closed + one open: closing again is a no-op
	// for the parent's column.
	autoClose(t, st, t1, true)
	autoClose(t, st, t1, true)
	if got := len(itemEvents(t, pool, "item.moved", epicID)); got != after {
		t.Fatalf("parent item.moved events after redundant closes = %d, want %d (no-op)", got, after)
	}
}

// TestAutoTransitionCascadeDepthOne: only the mutated child's DIRECT parent
// auto-transitions; its own parent (the milestone) is never touched.
func TestAutoTransitionCascadeDepthOne(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	todo, doing := sd.columns[0], sd.columns[1]

	ms := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"at-ms-%s","column_id":%q,"kind":"milestone","agent":"test"}`, tag, todo))
	msID := ms["id"].(string)
	epic := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"at-epic-%s","column_id":%q,"kind":"epic","parent_item_id":%q,"agent":"test"}`, tag, todo, msID))
	epicID := epic["id"].(string)
	task := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"at-task-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, todo, epicID))
	taskID := task["id"].(string)

	autoMoveChild(t, st, taskID, doing)

	if got := autoGetItem(t, st, epicID)["column_id"]; got != doing {
		t.Fatalf("direct parent epic column = %v, want doing %q", got, doing)
	}
	if got := autoGetItem(t, st, msID)["column_id"]; got != todo {
		t.Fatalf("grandparent milestone column = %v, want todo %q (cascade must stop at depth 1)", got, todo)
	}
	if evs := itemEvents(t, pool, "item.moved", msID); len(evs) != 0 {
		t.Fatalf("milestone had %d item.moved events, want 0 (no cascade)", len(evs))
	}
}

// TestAutoTransitionCrossBoardChildDoesNotDriveParent: a child on a DIFFERENT
// board counts for roll-up but never moves the parent's column.
func TestAutoTransitionCrossBoardChildDoesNotDriveParent(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)

	epic := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"at-xepic-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, sd.columns[0]))
	epicID := epic["id"].(string)

	bout := st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/boards",
		`{"name":"at-boardB-`+tag+`","agent":"test"}`, http.StatusCreated)
	boardB := bout["board"].(map[string]any)["id"].(string)
	colsB := bout["board"].(map[string]any)["columns"].([]any)
	colBTodo := colsB[0].(map[string]any)["id"].(string)
	colBDoing := colsB[1].(map[string]any)["id"].(string)

	task := createItem(t, st, boardB,
		fmt.Sprintf(`{"title":"at-xtask-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, colBTodo, epicID))
	taskID := task["id"].(string)

	autoMoveChild(t, st, taskID, colBDoing)

	if got := autoGetItem(t, st, epicID)["column_id"]; got != sd.columns[0] {
		t.Fatalf("cross-board child moved parent column to %v, want todo %q", got, sd.columns[0])
	}
	if evs := itemEvents(t, pool, "item.moved", epicID); len(evs) != 0 {
		t.Fatalf("parent had %d item.moved events, want 0", len(evs))
	}
}

// TestAutoTransitionTaskNotAParent: a task is never auto-transitioned by the
// rule (only its own user move is recorded).
func TestAutoTransitionTaskNotAParent(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	todo, doing := sd.columns[0], sd.columns[1]

	task := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"at-lone-%s","column_id":%q,"agent":"test"}`, tag, todo))
	taskID := task["id"].(string)

	autoMoveChild(t, st, taskID, doing)

	// Exactly the one user-initiated move, no phantom auto move.
	if evs := itemEvents(t, pool, "item.moved", taskID); len(evs) != 1 {
		t.Fatalf("task item.moved events = %d, want 1 (its own move only)", len(evs))
	}
	if got := autoGetItem(t, st, taskID)["column_id"]; got != doing {
		t.Fatalf("task column = %v, want doing %q", got, doing)
	}
}

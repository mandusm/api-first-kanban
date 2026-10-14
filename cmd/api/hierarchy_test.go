package main

import (
	"fmt"
	"net/http"
	"testing"
)

// itemOf unwraps {"item":{…}} from a decoded response.
func itemOf(out map[string]any) map[string]any {
	return out["item"].(map[string]any)
}

// createItem posts an item body and returns the item object.
func createItem(t *testing.T, st boardStore, boardID, body string) map[string]any {
	t.Helper()
	return itemOf(st.do(t, "POST", "/api/boards/"+boardID+"/items", body, http.StatusCreated))
}

// TestItemHierarchyCreateAndDefaults pins the create contract: kind defaults to
// "task", a milestone has no parent, an epic's parent is a milestone (or may be
// a top-level epic), and a task may sit under a milestone or an epic (or be
// top-level).
func TestItemHierarchyCreateAndDefaults(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	// Omitted kind → task, no parent.
	def := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"def-%s","column_id":%q,"agent":"test"}`, tag, col))
	if got := def["kind"]; got != "task" {
		t.Errorf("default kind = %v, want task", got)
	}
	if got := def["parent_item_id"]; got != nil {
		t.Errorf("default parent_item_id = %v, want null", got)
	}

	// Milestone: explicit kind, no parent.
	ms := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"ms-%s","column_id":%q,"kind":"milestone","agent":"test"}`, tag, col))
	msID := ms["id"].(string)
	if ms["kind"] != "milestone" || ms["parent_item_id"] != nil {
		t.Errorf("milestone = %v/%v, want milestone/null", ms["kind"], ms["parent_item_id"])
	}

	// Epic under a milestone.
	epic := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"epic-%s","column_id":%q,"kind":"epic","parent_item_id":%q,"agent":"test"}`, tag, col, msID))
	epicID := epic["id"].(string)
	if epic["kind"] != "epic" || epic["parent_item_id"] != msID {
		t.Errorf("epic = %v/%v, want epic/%s", epic["kind"], epic["parent_item_id"], msID)
	}

	// Top-level epic: no parent is required.
	topEpic := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"topepic-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, col))
	if topEpic["kind"] != "epic" || topEpic["parent_item_id"] != nil {
		t.Errorf("top-level epic = %v/%v, want epic/null", topEpic["kind"], topEpic["parent_item_id"])
	}

	// Task under an epic.
	task := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"task-%s","column_id":%q,"kind":"task","parent_item_id":%q,"agent":"test"}`, tag, col, epicID))
	if task["kind"] != "task" || task["parent_item_id"] != epicID {
		t.Errorf("task = %v/%v, want task/%s", task["kind"], task["parent_item_id"], epicID)
	}

	// Task under a milestone (skipping the epic level).
	taskMs := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"taskms-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, msID))
	if taskMs["kind"] != "task" || taskMs["parent_item_id"] != msID {
		t.Errorf("task-under-milestone = %v/%v, want task/%s", taskMs["kind"], taskMs["parent_item_id"], msID)
	}
}

// TestItemHierarchyInvalidCombos pins the kind/parent-kind rule: invalid
// combinations are 400, an unknown parent is 404, and a cross-workspace parent
// is 400.
func TestItemHierarchyInvalidCombos(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"ms-%s","column_id":%q,"kind":"milestone","agent":"test"}`, tag, col))
	plain := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"plain-%s","column_id":%q,"agent":"test"}`, tag, col))
	plainID := plain["id"].(string)
	parentEpic := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"parentepic-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, col))
	parentEpicID := parentEpic["id"].(string)

	bad := []struct{ name, body string }{
		{"milestone with parent", fmt.Sprintf(`{"title":"bad1-%s","column_id":%q,"kind":"milestone","parent_item_id":%q}`, tag, col, plainID)},
		{"epic with task parent", fmt.Sprintf(`{"title":"bad3-%s","column_id":%q,"kind":"epic","parent_item_id":%q}`, tag, col, plainID)},
		{"epic with epic parent", fmt.Sprintf(`{"title":"bad8-%s","column_id":%q,"kind":"epic","parent_item_id":%q}`, tag, col, parentEpicID)},
		{"task with task parent", fmt.Sprintf(`{"title":"bad4-%s","column_id":%q,"parent_item_id":%q}`, tag, col, plainID)},
		{"unknown kind", fmt.Sprintf(`{"title":"bad5-%s","column_id":%q,"kind":"theme"}`, tag, col)},
	}
	for _, tc := range bad {
		st.do(t, "POST", "/api/boards/"+sd.boardID+"/items", tc.body, http.StatusBadRequest)
	}

	// Unknown (well-formed uuid) parent → 404.
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"bad6-%s","column_id":%q,"parent_item_id":"00000000-0000-0000-0000-000000000000"}`, tag, col),
		http.StatusNotFound)

	// A milestone in another workspace cannot parent this board's task → 400.
	other := seedBoard(t, st, "other-"+tag)
	otherMs := createItem(t, st, other.boardID, fmt.Sprintf(`{"title":"otherms-%s","column_id":%q,"kind":"milestone","agent":"test"}`, tag, other.columns[0]))
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"bad7-%s","column_id":%q,"parent_item_id":%q}`, tag, col, otherMs["id"].(string)),
		http.StatusBadRequest)
}

// TestItemHierarchyPatchSemantics pins PATCH three-state parent handling:
// absent keeps, a string sets, null clears; and clearing/changing kind is
// rejected when it would break the kind/parent rule.
func TestItemHierarchyPatchSemantics(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	ms := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"ms-%s","column_id":%q,"kind":"milestone","agent":"test"}`, tag, col))
	msID := ms["id"].(string)
	epic := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"epic-%s","column_id":%q,"kind":"epic","parent_item_id":%q,"agent":"test"}`, tag, col, msID))
	epicID := epic["id"].(string)
	task := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"task-%s","column_id":%q,"agent":"test"}`, tag, col))
	taskID := task["id"].(string)

	// Set a parent.
	set := itemOf(st.do(t, "PATCH", "/api/items/"+taskID, fmt.Sprintf(`{"parent_item_id":%q,"agent":"test"}`, epicID), http.StatusOK))
	if set["parent_item_id"] != epicID {
		t.Fatalf("patch set parent = %v, want %s", set["parent_item_id"], epicID)
	}

	// Absent parent keeps it.
	kept := itemOf(st.do(t, "PATCH", "/api/items/"+taskID, fmt.Sprintf(`{"title":"renamed-%s","agent":"test"}`, tag), http.StatusOK))
	if kept["parent_item_id"] != epicID {
		t.Errorf("patch absent parent = %v, want kept %s", kept["parent_item_id"], epicID)
	}

	// Explicit null clears it.
	cleared := itemOf(st.do(t, "PATCH", "/api/items/"+taskID, `{"parent_item_id":null,"agent":"test"}`, http.StatusOK))
	if cleared["parent_item_id"] != nil {
		t.Errorf("patch null parent = %v, want null", cleared["parent_item_id"])
	}

	// A non-string parent_item_id is a validation error (the decode is shared
	// with the MCP transport; REST behavior is unchanged).
	bad := st.do(t, "PATCH", "/api/items/"+taskID, `{"parent_item_id":5,"agent":"test"}`, http.StatusBadRequest)
	if code := bad["error"].(map[string]any)["code"]; code != "validation" {
		t.Errorf("non-string parent code = %v, want validation", code)
	}

	// Clearing an epic's parent is allowed: a parentless epic is top-level.
	epicTop := itemOf(st.do(t, "PATCH", "/api/items/"+epicID, `{"parent_item_id":null,"agent":"test"}`, http.StatusOK))
	if epicTop["parent_item_id"] != nil {
		t.Errorf("epic parent after clear = %v, want null", epicTop["parent_item_id"])
	}

	// A parented item still cannot change its kind to milestone: re-parent the
	// epic, then reject the kind change since a milestone may not have a parent.
	st.do(t, "PATCH", "/api/items/"+epicID, fmt.Sprintf(`{"parent_item_id":%q,"agent":"test"}`, msID), http.StatusOK)
	st.do(t, "PATCH", "/api/items/"+epicID, `{"kind":"milestone","agent":"test"}`, http.StatusBadRequest)

	// Changing a parentless task to a top-level epic is allowed...
	upTop := itemOf(st.do(t, "PATCH", "/api/items/"+taskID, `{"kind":"epic","agent":"test"}`, http.StatusOK))
	if upTop["kind"] != "epic" || upTop["parent_item_id"] != nil {
		t.Errorf("parentless task->epic = %v/%v, want epic/null", upTop["kind"], upTop["parent_item_id"])
	}
	// ...and kind + parent may be changed together.
	up := itemOf(st.do(t, "PATCH", "/api/items/"+taskID, fmt.Sprintf(`{"kind":"epic","parent_item_id":%q,"agent":"test"}`, msID), http.StatusOK))
	if up["kind"] != "epic" || up["parent_item_id"] != msID {
		t.Errorf("kind+parent change = %v/%v, want epic/%s", up["kind"], up["parent_item_id"], msID)
	}

	// P0 repro A: M > E > T; changing E's kind to task would leave the task
	// child under a task parent. Rejected, and the stored kind is unchanged.
	msA := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"msA-%s","column_id":%q,"kind":"milestone","agent":"test"}`, tag, col))
	epicA := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"epicA-%s","column_id":%q,"kind":"epic","parent_item_id":%q,"agent":"test"}`, tag, col, msA["id"].(string)))
	createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"taskA-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, epicA["id"].(string)))
	epicAID := epicA["id"].(string)
	st.do(t, "PATCH", "/api/items/"+epicAID, `{"kind":"task","agent":"test"}`, http.StatusBadRequest)
	if got := itemOf(st.do(t, "GET", "/api/items/"+epicAID, "", http.StatusOK))["kind"]; got != "epic" {
		t.Errorf("epic kind after rejected kind change = %v, want epic", got)
	}

	// P0 repro B: A(milestone) > B(epic, parent=A). Changing A's kind to task is
	// rejected (its epic child would become invalid), so the A->B->A cycle
	// cannot be formed; the direct cycle attempt is rejected too.
	msB := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"msB-%s","column_id":%q,"kind":"milestone","agent":"test"}`, tag, col))
	msBID := msB["id"].(string)
	epicB := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"epicB-%s","column_id":%q,"kind":"epic","parent_item_id":%q,"agent":"test"}`, tag, col, msBID))
	st.do(t, "PATCH", "/api/items/"+msBID, `{"kind":"task","agent":"test"}`, http.StatusBadRequest)
	st.do(t, "PATCH", "/api/items/"+msBID, fmt.Sprintf(`{"parent_item_id":%q,"agent":"test"}`, epicB["id"].(string)), http.StatusBadRequest)
	if got := itemOf(st.do(t, "GET", "/api/items/"+msBID, "", http.StatusOK))["kind"]; got != "milestone" {
		t.Errorf("milestone kind after rejected changes = %v, want milestone", got)
	}
}

// TestItemHierarchyInBoardSnapshot pins that kind + parent_item_id are echoed
// in the composite board payload (the UI snapshot).
func TestItemHierarchyInBoardSnapshot(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	ms := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"snapms-%s","column_id":%q,"kind":"milestone","agent":"test"}`, tag, col))
	msID := ms["id"].(string)
	task := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"snaptask-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, msID))
	taskID := task["id"].(string)

	snap := st.do(t, "GET", "/api/boards/"+sd.boardID, "", http.StatusOK)
	byID := map[string]map[string]any{}
	for _, raw := range snap["items"].([]any) {
		it := raw.(map[string]any)
		byID[it["id"].(string)] = it
	}
	if got := byID[msID]["kind"]; got != "milestone" {
		t.Errorf("snapshot milestone kind = %v, want milestone", got)
	}
	if got := byID[msID]["parent_item_id"]; got != nil {
		t.Errorf("snapshot milestone parent = %v, want null", got)
	}
	if got := byID[taskID]["kind"]; got != "task" {
		t.Errorf("snapshot task kind = %v, want task", got)
	}
	if got := byID[taskID]["parent_item_id"]; got != msID {
		t.Errorf("snapshot task parent = %v, want %s", got, msID)
	}
}

// TestItemDetailChildren pins the children list on GET /api/items/{id}: full
// item shape (labels flattened), created_at ascending order, archived
// children included, empty array (not null) for a childless item, and the
// classic milestone → epic → task grouping readable in one call.
func TestItemDetailChildren(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	ms := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"ms-%s","column_id":%q,"kind":"milestone","agent":"test"}`, tag, col))
	msID := ms["id"].(string)
	epic := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"epic-%s","column_id":%q,"kind":"epic","parent_item_id":%q,"agent":"test"}`, tag, col, msID))
	epicID := epic["id"].(string)

	// Two tasks under the epic, in a different column so the status (column)
	// the UI derives differs; the second one is closed, the third archived.
	done := sd.columns[len(sd.columns)-1]
	t1 := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"t1-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, epicID))
	t2 := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"t2-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, done, epicID))
	st.do(t, "PATCH", "/api/items/"+t2["id"].(string), `{"closed":true,"agent":"test"}`, http.StatusOK)
	t3 := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"t3-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, msID))
	st.do(t, "POST", "/api/items/"+t3["id"].(string)+"/archive", `{"agent":"test"}`, http.StatusOK)

	// The epic's children: t1, t2 (closed) — created_at ascending, full shape.
	detail := st.do(t, "GET", "/api/items/"+epicID, "", http.StatusOK)
	kids, ok := detail["children"].([]any)
	if !ok {
		t.Fatalf("detail has no children array: %v", detail)
	}
	if len(kids) != 2 {
		t.Fatalf("epic children = %d, want 2 (t1, t2): %v", len(kids), kids)
	}
	want := []string{t1["id"].(string), t2["id"].(string)}
	for i, raw := range kids {
		c := raw.(map[string]any)
		if c["id"] != want[i] {
			t.Errorf("children[%d] = %v, want %s (created_at ascending)", i, c["id"], want[i])
		}
		if _, ok := c["labels"].([]any); !ok {
			t.Errorf("child %v has no labels array: %v", c["id"], c)
		}
		if c["parent_item_id"] != epicID {
			t.Errorf("child %v parent = %v, want %s", c["id"], c["parent_item_id"], epicID)
		}
		if c["column_id"] != t1["column_id"] && c["column_id"] != t2["column_id"] {
			t.Errorf("child %v column = %v, want a real column", c["id"], c["column_id"])
		}
	}
	closed := kids[1].(map[string]any)
	if closed["closed"] != true {
		t.Errorf("t2 closed = %v, want true (status carries the closed flag)", closed["closed"])
	}

	// The milestone sees the epic AND its direct task child (t3, archived —
	// still listed: the detail read is the full-truth read).
	detail = st.do(t, "GET", "/api/items/"+msID, "", http.StatusOK)
	kids = detail["children"].([]any)
	if len(kids) != 2 {
		t.Fatalf("milestone children = %d, want 2 (epic, archived t3): %v", len(kids), kids)
	}
	if kids[0].(map[string]any)["id"] != epicID {
		t.Errorf("milestone children[0] = %v, want the epic first (created_at)", kids[0])
	}
	if got := kids[1].(map[string]any)["archived_at"]; got == nil {
		t.Errorf("archived child t3 archived_at = %v, want a timestamp", got)
	}

	// A childless task: children is an empty array, never null.
	detail = st.do(t, "GET", "/api/items/"+t1["id"].(string), "", http.StatusOK)
	if got, ok := detail["children"].([]any); !ok || len(got) != 0 {
		t.Errorf("childless item children = %v (%T), want []", detail["children"], detail["children"])
	}
}

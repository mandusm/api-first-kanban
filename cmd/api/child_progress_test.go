package main

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// TestChildProgressCountsEveryEndpoint is the PB-84 fixture test: a parent
// with 2 closed / 5 total non-archived children — including one
// archived-closed child that must NOT count (excluded from numerator AND
// denominator) and one child on another board of the same workspace that MUST
// count — asserted through EVERY endpoint that serializes an ItemView:
// board snapshot (default, ?q= searched view, ?include_items=false), the
// per-column items page, GET /api/items/{id} (item + each child row),
// update/move/transfer/archive/unarchive responses, the relations edge
// other-item (detail + relations reads), and the MCP get_board/get_item.
func TestChildProgressCountsEveryEndpoint(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	// Second board in the SAME workspace for the cross-board child.
	bout := st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/boards",
		`{"name":"board-b-`+tag+`","agent":"test"}`, http.StatusCreated)
	boardB := bout["board"].(map[string]any)["id"].(string)
	bcols := bout["board"].(map[string]any)["columns"].([]any)
	colBID := bcols[0].(map[string]any)["id"].(string)

	parent := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"prog-parent-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, col))
	parentID := parent["id"].(string)

	// Child helper.
	mkChild := func(boardID, columnID, name string) map[string]any {
		return createItem(t, st, boardID,
			fmt.Sprintf(`{"title":"%s-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, name, tag, columnID, parentID))
	}
	closeItem := func(id string) {
		st.do(t, "PATCH", "/api/items/"+id, `{"closed":true}`, http.StatusOK)
	}

	c1 := mkChild(sd.boardID, col, "prog-c1") // closed
	closeItem(c1["id"].(string))
	c2 := mkChild(sd.boardID, col, "prog-c2") // closed
	closeItem(c2["id"].(string))
	c3 := mkChild(sd.boardID, col, "prog-c3") // open, later archived+unarchived
	c4 := mkChild(sd.boardID, col, "prog-c4") // closed AND archived: must not count
	closeItem(c4["id"].(string))
	mkChild(boardB, colBID, "prog-c5")        // cross-board, must count
	c6 := mkChild(sd.boardID, col, "prog-c6") // open
	// c6 stays open (2 closed + archived c4 excluded = 2/5)

	// A second epic with one closed task so a child row itself carries 1/1
	// (exercises the batched counts query with non-zero values).
	ep2 := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"prog-epic2-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, col))
	sub := createItem(t, st, sd.boardID,
		fmt.Sprintf(`{"title":"prog-sub-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, ep2["id"].(string)))
	closeItem(sub["id"].(string))

	// Archive c4 (closed + archived) → excluded from numerator AND denominator.
	st.do(t, "POST", "/api/items/"+c4["id"].(string)+"/archive", ``, http.StatusOK)

	assertCounts := func(item map[string]any, where string) {
		t.Helper()
		cc, ok := item["closed_children"].(float64)
		tc, tok := item["total_children"].(float64)
		if !ok || !tok {
			t.Fatalf("%s: closed_children/total_children not numeric ints: %v %v", where, item["closed_children"], item["total_children"])
		}
		if cc != 2 || tc != 5 {
			t.Errorf("%s: closed_children/total_children = %v/%v, want 2/5", where, item["closed_children"], item["total_children"])
		}
	}

	// 1. Board snapshot, default view.
	snap := st.do(t, "GET", "/api/boards/"+sd.boardID, ``, http.StatusOK)
	var parentSnap map[string]any
	for _, r := range snap["items"].([]any) {
		it := r.(map[string]any)
		if it["id"] == parentID {
			parentSnap = it
		}
	}
	if parentSnap == nil {
		t.Fatalf("parent not in board snapshot")
	}
	assertCounts(parentSnap, "board snapshot")

	// 2. Board snapshot, searched view (?q=).
	q := st.do(t, "GET", "/api/boards/"+sd.boardID+"?q="+url.QueryEscape("TITLE = 'prog-parent-"+tag+"'"), ``, http.StatusOK)
	qitems := q["items"].([]any)
	if len(qitems) != 1 {
		t.Fatalf("searched view items = %d, want 1", len(qitems))
	}
	assertCounts(qitems[0].(map[string]any), "searched snapshot")

	// 3. Lightweight snapshot: items array empty (nothing to count on).
	light := st.do(t, "GET", "/api/boards/"+sd.boardID+"?include_items=false", ``, http.StatusOK)
	if raw, ok := light["items"].([]any); !ok || len(raw) != 0 {
		t.Errorf("include_items=false items = %v, want empty", light["items"])
	}

	// 4. Per-column items page.
	page := st.do(t, "GET", fmt.Sprintf("/api/boards/%s/items?column_id=%s&limit=500", sd.boardID, col), ``, http.StatusOK)
	for _, r := range page["items"].([]any) {
		it := r.(map[string]any)
		if it["id"] == parentID {
			assertCounts(it, "column items page")
		}
	}

	// 5. Item detail: the parent AND each child row.
	detail := st.do(t, "GET", "/api/items/"+parentID, ``, http.StatusOK)
	assertCounts(itemOf(detail), "detail item")
	childIDs := map[string]string{c1["id"].(string): "c1", c2["id"].(string): "c2", c3["id"].(string): "c3", c6["id"].(string): "c6", ep2["id"].(string): "ep2"}
	for _, r := range detail["children"].([]any) {
		it := r.(map[string]any)
		id := it["id"].(string)
		name, isChild := childIDs[id]
		if !isChild {
			continue
		}
		if name == "ep2" {
			// ep2 has one closed task child.
			if it["closed_children"].(float64) != 1 || it["total_children"].(float64) != 1 {
				t.Errorf("child ep2 counts = %v/%v, want 1/1", it["closed_children"], it["total_children"])
			}
			continue
		}
		if it["closed_children"].(float64) != 0 || it["total_children"].(float64) != 0 {
			t.Errorf("child %s counts = %v/%v, want 0/0", name, it["closed_children"], it["total_children"])
		}
	}

	// 6. Update response (PATCH the parent).
	upd := itemOf(st.do(t, "PATCH", "/api/items/"+parentID, fmt.Sprintf(`{"title":"prog-parent2-%s"}`, tag), http.StatusOK))
	assertCounts(upd, "update response")

	// 7. Move response.
	mov := itemOf(st.do(t, "POST", "/api/items/"+parentID+"/move",
		fmt.Sprintf(`{"column_id":%q,"index":0}`, col), http.StatusOK))
	assertCounts(mov, "move response")

	// 8. Transfer response.
	tr := itemOf(st.do(t, "POST", "/api/items/"+parentID+"/transfer",
		`{"to_agent":"other","note":"pb84"}`, http.StatusOK))
	assertCounts(tr, "transfer response")

	// 9. Archive/unarchive the PARENT response carries its own counts.
	arc := itemOf(st.do(t, "POST", "/api/items/"+parentID+"/archive", ``, http.StatusOK))
	assertCounts(arc, "archive response")
	unarc := itemOf(st.do(t, "POST", "/api/items/"+parentID+"/unarchive", ``, http.StatusOK))
	assertCounts(unarc, "unarchive response")

	// 10. Relations edge other-item: relate parent → c3; c3's incoming edge
	// carries the parent's ItemView with 2/5.
	st.do(t, "POST", "/api/items/"+parentID+"/relations",
		fmt.Sprintf(`{"to_item_id":%q,"relation":"relates_to","agent":"test"}`, c3["id"]), http.StatusCreated)
	relDetail := st.do(t, "GET", "/api/items/"+c3["id"].(string), ``, http.StatusOK)
	incoming := relDetail["relations"].(map[string]any)["incoming"].([]any)
	if len(incoming) != 1 {
		t.Fatalf("incoming relations = %d, want 1", len(incoming))
	}
	assertCounts(incoming[0].(map[string]any)["item"].(map[string]any), "relations edge other-item (detail)")

	// 11. MCP get_board / get_item carry the same counts ( exercised in
	// internal/mcp TestChildProgressCountsMCP).
}

// TestChildProgressCountsCreateResponse pins the additive create contract: a
// newly created item always serializes closed_children/total_children (0/0 for
// a fresh leaf), never omitted.
func TestChildProgressCountsCreateResponse(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	it := createItem(t, st, sd.boardID, fmt.Sprintf(`{"title":"fresh-%s","column_id":%q,"agent":"test"}`, tag, col))
	if it["closed_children"] != float64(0) || it["total_children"] != float64(0) {
		t.Errorf("create response counts = %v/%v, want 0/0 (fields must be present)", it["closed_children"], it["total_children"])
	}
}

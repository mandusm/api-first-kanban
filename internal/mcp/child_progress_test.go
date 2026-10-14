package mcp_test

import (
	"fmt"
	"testing"
	"time"
)

// TestChildProgressCountsMCP is the PB-84 fixture through the MCP transport:
// a parent with 2 closed / 5 total non-archived children (one archived-closed
// child excluded from numerator AND denominator, one cross-board child
// included) surfaces closed_children/total_children on get_board snapshot
// items and get_item (item + children rows).
func TestChildProgressCountsMCP(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	ws := callTool(t, cs, "create_workspace", map[string]any{"name": "ws-pb84-" + tag, "agent": "eng-pct-test"})
	wsID := ws["workspace"].(map[string]any)["id"].(string)
	b := callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "board-a-" + tag, "agent": "eng-pct-test"})
	board := b["board"].(map[string]any)
	boardID := board["id"].(string)
	col := board["columns"].([]any)[0].(map[string]any)["id"].(string)

	mkItem := func(args map[string]any) map[string]any {
		args["agent"] = "eng-pct-test"
		out := callTool(t, cs, "create_item", args)
		return out["item"].(map[string]any)
	}
	parent := mkItem(map[string]any{"board_id": boardID, "column_id": col, "title": "prog-parent-" + tag, "kind": "epic"})
	parentID := parent["id"].(string)

	mkChild := func(bid, name string) string {
		return mkItem(map[string]any{"board_id": bid, "column_id": col, "title": name + "-" + tag, "parent_item_id": parentID})["id"].(string)
	}
	closeItem := func(id string) {
		callTool(t, cs, "update_item", map[string]any{"item_id": id, "closed": true, "agent": "eng-pct-test"})
	}
	c1 := mkChild(boardID, "c1")
	closeItem(c1)
	c2 := mkChild(boardID, "c2")
	closeItem(c2)
	mkChild(boardID, "c3") // open
	c4 := mkChild(boardID, "c4")
	closeItem(c4)
	mkChild(boardID, "c5") // open

	// Archive the closed c4: must drop out of numerator AND denominator.
	callTool(t, cs, "archive_item", map[string]any{"item_id": c4, "agent": "eng-pct-test"})

	// Cross-board child in the same workspace must count.
	bb := callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "board-b-" + tag, "agent": "eng-pct-test"})
	bboard := bb["board"].(map[string]any)
	bcol := bboard["columns"].([]any)[0].(map[string]any)["id"].(string)
	mkItem(map[string]any{"board_id": bboard["id"].(string), "column_id": bcol, "title": "c6-" + tag, "parent_item_id": parentID})

	// get_board snapshot: parent item carries 2/5.
	gb := callTool(t, cs, "get_board", map[string]any{"board_id": boardID})
	for _, r := range gb["items"].([]any) {
		it := r.(map[string]any)
		if it["id"] != parentID {
			continue
		}
		if it["closed_children"].(float64) != 2 || it["total_children"].(float64) != 5 {
			t.Errorf("get_board parent counts = %v/%v, want 2/5", it["closed_children"], it["total_children"])
		}
	}

	// get_item: the item AND each child row carry counts.
	gi := callTool(t, cs, "get_item", map[string]any{"item_id": parentID})
	item := gi["item"].(map[string]any)
	if item["closed_children"].(float64) != 2 || item["total_children"].(float64) != 5 {
		t.Errorf("get_item parent counts = %v/%v, want 2/5", item["closed_children"], item["total_children"])
	}
	kids, ok := gi["children"].([]any)
	if !ok {
		t.Fatalf("get_item has no children array: %v", gi)
	}
	for _, r := range kids {
		ch := r.(map[string]any)
		if ch["id"] == c1 && (ch["closed_children"].(float64) != 0 || ch["total_children"].(float64) != 0) {
			t.Errorf("get_item child row counts = %v/%v, want 0/0", ch["closed_children"], ch["total_children"])
		}
	}
}

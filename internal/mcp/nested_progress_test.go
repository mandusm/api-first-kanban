package mcp_test

import (
	"fmt"
	"testing"
	"time"
)

// TestNestedSubtreeProgressMCP is the PB-118 fixture through the MCP
// transport: milestone -> epic -> task with one fully closed epic (its own
// tasks closed) and one partial epic. get_board and get_item (item + children
// rows) must surface closed_descendants/total_descendants over the WHOLE
// subtree while closed_children/total_children stay the DIRECT pair; an
// archived closed task is excluded from the subtree counts; a leaf reads 0/0.
func TestNestedSubtreeProgressMCP(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	ws := callTool(t, cs, "create_workspace", map[string]any{"name": "ws-pb118-" + tag, "agent": "eng-rollup-test"})
	wsID := ws["workspace"].(map[string]any)["id"].(string)
	b := callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "board-pb118-" + tag, "agent": "eng-rollup-test"})
	boardID := b["board"].(map[string]any)["id"].(string)
	col := b["board"].(map[string]any)["columns"].([]any)[0].(map[string]any)["id"].(string)

	mkItem := func(args map[string]any) map[string]any {
		args["agent"] = "eng-rollup-test"
		return callTool(t, cs, "create_item", args)["item"].(map[string]any)
	}
	mk := func(title, kind, parent string) string {
		args := map[string]any{"board_id": boardID, "column_id": col, "title": title + "-" + tag}
		if kind != "" {
			args["kind"] = kind
		}
		if parent != "" {
			args["parent_item_id"] = parent
		}
		return mkItem(args)["id"].(string)
	}
	closeItem := func(id string) {
		callTool(t, cs, "update_item", map[string]any{"item_id": id, "closed": true, "agent": "eng-rollup-test"})
	}

	ms := mk("ms", "milestone", "")
	epA := mk("epicA", "epic", ms)
	epB := mk("epicB", "epic", ms)
	a1 := mk("a1", "", epA)
	a2 := mk("a2", "", epA)
	a3 := mk("a3", "", epA) // archived later
	b1 := mk("b1", "", epB)
	mk("b2", "", epB)
	closeItem(a1)
	closeItem(a2)
	closeItem(a3)
	closeItem(epA)
	closeItem(b1)
	callTool(t, cs, "archive_item", map[string]any{"item_id": a3, "agent": "eng-rollup-test"})

	// Milestone subtree: 4 closed (a1,a2,epA,b1) / 6 total (archived a3
	// excluded); direct children stay 1/2 (epics only, epA closed).
	// Epic A subtree: 2/2; epic B: 1/2; leaf a1: 0/0.
	assert := func(v map[string]any, where string, cd, td int) {
		t.Helper()
		gotCD, okCD := v["closed_descendants"].(float64)
		gotTD, okTD := v["total_descendants"].(float64)
		if !okCD || !okTD {
			t.Fatalf("%s: descendants fields missing/not numeric: %v %v", where, v["closed_descendants"], v["total_descendants"])
		}
		if int(gotCD) != cd || int(gotTD) != td {
			t.Errorf("%s: descendants = %v/%v, want %d/%d", where, gotCD, gotTD, cd, td)
		}
	}

	gb := callTool(t, cs, "get_board", map[string]any{"board_id": boardID})
	for _, r := range gb["items"].([]any) {
		it := r.(map[string]any)
		switch it["id"] {
		case ms:
			assert(it, "get_board milestone", 4, 6)
			if it["closed_children"].(float64) != 1 || it["total_children"].(float64) != 2 {
				t.Errorf("get_board milestone direct = %v/%v, want 1/2", it["closed_children"], it["total_children"])
			}
		case epA:
			assert(it, "get_board epic A", 2, 2)
		case epB:
			assert(it, "get_board epic B", 1, 2)
		case a1:
			assert(it, "get_board leaf", 0, 0)
		}
	}

	gi := callTool(t, cs, "get_item", map[string]any{"item_id": ms})
	assert(gi["item"].(map[string]any), "get_item milestone", 4, 6)
	for _, r := range gi["children"].([]any) {
		ch := r.(map[string]any)
		if ch["id"] == epA {
			assert(ch, "get_item child epic A", 2, 2)
		}
	}

	// Leaf detail via get_item.
	li := callTool(t, cs, "get_item", map[string]any{"item_id": a1})
	leaf := li["item"].(map[string]any)
	assert(leaf, "get_item leaf", 0, 0)
	if leaf["closed_children"].(float64) != 0 || leaf["total_children"].(float64) != 0 {
		t.Errorf("get_item leaf direct = %v/%v, want 0/0", leaf["closed_children"], leaf["total_children"])
	}
}

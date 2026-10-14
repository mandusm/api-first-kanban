package mcp_test

import (
	"fmt"
	"testing"
	"time"
)

// TestActivityAtMCP is the PB-122 fixture through the MCP transport: item
// payloads on get_board and get_item carry activity_at — equal to updated_at
// for a comment-less item and bumped to the newest comment time after
// add_comment.
func TestActivityAtMCP(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	ws := callTool(t, cs, "create_workspace", map[string]any{"name": "ws-pb122-" + tag, "agent": "eng-act-test"})
	wsID := ws["workspace"].(map[string]any)["id"].(string)
	b := callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "board-" + tag, "agent": "eng-act-test"})
	boardID := b["board"].(map[string]any)["id"].(string)
	col := b["board"].(map[string]any)["columns"].([]any)[0].(map[string]any)["id"].(string)

	item := callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "column_id": col, "title": "act-" + tag, "agent": "eng-act-test"})["item"].(map[string]any)
	id := item["id"].(string)

	parse := func(m map[string]any, key, where string) time.Time {
		t.Helper()
		raw, ok := m[key].(string)
		if !ok {
			t.Fatalf("%s: missing %s: %v", where, key, m)
		}
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			t.Fatalf("%s: %s %q: %v", where, key, raw, err)
		}
		return at
	}

	// Comment-less: activity_at == updated_at on get_board and get_item.
	gb := callTool(t, cs, "get_board", map[string]any{"board_id": boardID})
	var snapItem map[string]any
	for _, r := range gb["items"].([]any) {
		it := r.(map[string]any)
		if it["id"] == id {
			snapItem = it
		}
	}
	if snapItem == nil {
		t.Fatalf("item %s missing from get_board", id)
	}
	if got, want := parse(snapItem, "activity_at", "get_board"), parse(snapItem, "updated_at", "get_board"); !got.Equal(want) {
		t.Errorf("get_board activity_at = %v, want updated_at %v", got, want)
	}
	gi := callTool(t, cs, "get_item", map[string]any{"item_id": id})
	giItem := gi["item"].(map[string]any)
	if got, want := parse(giItem, "activity_at", "get_item"), parse(giItem, "updated_at", "get_item"); !got.Equal(want) {
		t.Errorf("get_item activity_at = %v, want updated_at %v", got, want)
	}

	// Newer comment bumps activity_at to the comment time.
	time.Sleep(5 * time.Millisecond)
	comment := callTool(t, cs, "add_comment", map[string]any{"item_id": id, "body": "bump", "agent": "eng-act-test"})["comment"].(map[string]any)
	commentAt := parse(comment, "created_at", "add_comment")

	gi = callTool(t, cs, "get_item", map[string]any{"item_id": id})
	if got := parse(gi["item"].(map[string]any), "activity_at", "get_item after comment"); !got.Equal(commentAt) {
		t.Errorf("get_item activity_at = %v, want comment time %v", got, commentAt)
	}
	gb = callTool(t, cs, "get_board", map[string]any{"board_id": boardID})
	for _, r := range gb["items"].([]any) {
		it := r.(map[string]any)
		if it["id"] == id {
			if got := parse(it, "activity_at", "get_board after comment"); !got.Equal(commentAt) {
				t.Errorf("get_board activity_at = %v, want comment time %v", got, commentAt)
			}
		}
	}
}

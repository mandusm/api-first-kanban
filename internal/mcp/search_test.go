package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// snapshotItems decodes the items array of a get_board snapshot.
func snapshotItems(t *testing.T, snap map[string]any) []map[string]any {
	t.Helper()
	raw, ok := snap["items"].([]any)
	if !ok {
		t.Fatalf("snapshot items = %T, want array", snap["items"])
	}
	items := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		items = append(items, r.(map[string]any))
	}
	return items
}

// columnCount reads item_count for a column name from a snapshot.
func columnCount(t *testing.T, snap map[string]any, name string) float64 {
	t.Helper()
	cols, ok := snap["columns"].([]any)
	if !ok {
		t.Fatalf("snapshot columns = %T, want array", snap["columns"])
	}
	for _, c := range cols {
		col := c.(map[string]any)
		if col["name"] == name {
			n, ok := col["item_count"].(float64)
			if !ok {
				t.Fatalf("column %s item_count = %v, want number", name, col["item_count"])
			}
			return n
		}
	}
	t.Fatalf("column %s not found in snapshot", name)
	return -1
}

// setupSearchBoard creates a workspace/board with two items differing by title
// and priority so TITLE/PRIORITY queries can discriminate.
func setupSearchBoard(t *testing.T) (boardID, bugID, featID string) {
	t.Helper()
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	wsID := nested(t, callTool(t, cs, "create_workspace", map[string]any{"name": "mcp-search-ws-" + tag, "agent": "mcp-test"}), "workspace")["id"].(string)
	boardID = nested(t, callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "mcp-search-board-" + tag, "agent": "mcp-test"}), "board")["id"].(string)
	bugID = nested(t, callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "fix bug", "priority": 0, "agent": "mcp-test"}), "item")["id"].(string)
	featID = nested(t, callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "add feature", "priority": 3, "agent": "mcp-test"}), "item")["id"].(string)
	return boardID, bugID, featID
}

// TestGetBoardQueryFilters mirrors the REST PB-58 cases over the MCP tool:
// a non-empty query returns only matching items with filtered item_count,
// no query keeps today's behavior, zero matches yield an empty items array.
func TestGetBoardQueryFilters(t *testing.T) {
	boardID, bugID, _ := setupSearchBoard(t)
	cs := connectMCP(t)

	// Query filters items: PRIORITY <= 1 matches only the bug item.
	q := callTool(t, cs, "get_board", map[string]any{"board_id": boardID, "query": "PRIORITY <= 1"})
	items := snapshotItems(t, q)
	if len(items) != 1 || items[0]["id"] != bugID {
		t.Errorf("queried snapshot items = %v, want only %s", items, bugID)
	}
	if got := columnCount(t, q, "todo"); got != 1 {
		t.Errorf("filtered item_count = %v, want 1", got)
	}

	// Zero matches: valid query that matches nothing (spec §5.2/§5.3).
	none := callTool(t, cs, "get_board", map[string]any{"board_id": boardID, "query": "KIND = 'story'"})
	if got := snapshotItems(t, none); len(got) != 0 {
		t.Errorf("zero-match query items = %v, want empty", got)
	}

	// No query = today's behavior: both items, unfiltered.
	def := callTool(t, cs, "get_board", map[string]any{"board_id": boardID})
	if got := snapshotItems(t, def); len(got) != 2 {
		t.Errorf("default snapshot items = %d, want 2", len(got))
	}
	if got := columnCount(t, def, "todo"); got != 2 {
		t.Errorf("default item_count = %v, want 2", got)
	}

	// TITLE is exact case-insensitive (spec §5.2), so the case-folded full
	// title matches while a bare substring does not.
	sub := callTool(t, cs, "get_board", map[string]any{"board_id": boardID, "query": "TITLE = 'FIX BUG'"})
	if got := snapshotItems(t, sub); len(got) != 1 || got[0]["id"] != bugID {
		t.Errorf("TITLE = 'FIX BUG' items = %v, want only %s", got, bugID)
	}
	sub2 := callTool(t, cs, "get_board", map[string]any{"board_id": boardID, "query": "TITLE = 'bug'"})
	if got := snapshotItems(t, sub2); len(got) != 0 {
		t.Errorf("TITLE = 'bug' (substring) items = %v, want empty (exact match only)", got)
	}
}

// TestGetBoardQueryInvalidVerbatim asserts an invalid query surfaces the spec
// §6 parse-error message verbatim as a validation tool error.
func TestGetBoardQueryInvalidVerbatim(t *testing.T) {
	cs := connectMCP(t)

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "get_board", Arguments: map[string]any{"board_id": "00000000-0000-0000-0000-000000000000", "query": "created < -1d"}})
	if err != nil {
		t.Fatalf("call get_board: %v", err)
	}
	if !res.IsError {
		t.Fatalf("invalid query: IsError = false, want tool error")
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(contentText(res)), &env); err != nil {
		t.Fatalf("decode error envelope %q: %v", contentText(res), err)
	}
	if env.Error.Code != "validation" {
		t.Errorf("code = %q, want validation", env.Error.Code)
	}
	// Spec §6 worked example: lowercase 'created' is an unknown token.
	want := "parse error at 1:1 (offset 0): unexpected input 'created'"
	if env.Error.Message != want {
		t.Errorf("message = %q, want %q verbatim", env.Error.Message, want)
	}
}

// TestGetBoardQueryIncludeArchived asserts archived handling is unchanged
// under query: excluded by default, opt-in with include_archived.
func TestGetBoardQueryIncludeArchived(t *testing.T) {
	boardID, bugID, featID := setupSearchBoard(t)
	cs := connectMCP(t)

	// Archive the bug item (PRIORITY 0) — it is the only P0.
	if _, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "archive_item", Arguments: map[string]any{"item_id": bugID, "agent": "mcp-test"}}); err != nil {
		t.Fatalf("archive_item: %v", err)
	}

	def := callTool(t, cs, "get_board", map[string]any{"board_id": boardID, "query": "PRIORITY <= 3"})
	if boardHasItem(t, def, bugID) {
		t.Error("queried default snapshot still contains archived item")
	}
	if got := snapshotItems(t, def); len(got) != 1 || got[0]["id"] != featID {
		t.Errorf("queried default items = %v, want only %s", got, featID)
	}

	inc := callTool(t, cs, "get_board", map[string]any{"board_id": boardID, "query": "PRIORITY <= 3", "include_archived": true})
	if !boardHasItem(t, inc, bugID) {
		t.Error("queried include_archived snapshot does not contain archived item")
	}
	if got := snapshotItems(t, inc); len(got) != 2 {
		t.Errorf("queried include_archived items = %d, want 2", len(got))
	}

	// The archived item must also be excluded from the filtered item_count.
	if got := columnCount(t, def, "todo"); got != 1 {
		t.Errorf("filtered item_count with archived = %v, want 1", got)
	}
}

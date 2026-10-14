package mcp_test

import (
	"fmt"
	"regexp"
	"testing"
	"time"
)

// mcpItemKeyRE mirrors the item-key contract: <board prefix>-<sequence>.
var mcpItemKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,4}-[1-9][0-9]*$`)

// TestMCPItemKeys asserts the MCP tools expose the item key: create_item's item
// carries it, get_item returns it, and get_board's snapshot items all carry it.
func TestMCPItemKeys(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	wsOut := callTool(t, cs, "create_workspace", map[string]any{"name": "mcp-key-ws-" + tag, "agent": "test"})
	wsID := wsOut["workspace"].(map[string]any)["id"].(string)

	bOut := callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "MCP Key Board " + tag, "agent": "test"})
	board := bOut["board"].(map[string]any)
	boardID := board["id"].(string)
	if p, _ := board["key_prefix"].(string); p == "" {
		t.Error("create_board board is missing key_prefix")
	}

	iOut := callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "keyed mcp", "agent": "test"})
	item := iOut["item"].(map[string]any)
	iid := item["id"].(string)
	key, _ := item["key"].(string)
	if !mcpItemKeyRE.MatchString(key) {
		t.Fatalf("mcp create_item key = %q, want match %s", key, mcpItemKeyRE)
	}

	gOut := callTool(t, cs, "get_item", map[string]any{"item_id": iid})
	if got := gOut["item"].(map[string]any)["key"]; got != key {
		t.Errorf("mcp get_item key = %v, want %q", got, key)
	}

	bSnap := callTool(t, cs, "get_board", map[string]any{"board_id": boardID})
	items, _ := bSnap["items"].([]any)
	if len(items) == 0 {
		t.Fatal("mcp get_board returned no items")
	}
	for _, it := range items {
		m := it.(map[string]any)
		if k, _ := m["key"].(string); !mcpItemKeyRE.MatchString(k) {
			t.Errorf("mcp get_board item %v key = %q, want match %s", m["id"], k, mcpItemKeyRE)
		}
	}
}

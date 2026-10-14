package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"api-first-kanban/internal/events"
	"api-first-kanban/internal/testutil"
)

// wantTools is the documented tool set (names mirror docs/api.md resources).
var wantTools = []string{
	"add_comment",
	"add_relation",
	"archive_item",
	"create_board",
	"create_column",
	"create_item",
	"create_label",
	"create_workspace",
	"get_board",
	"get_item",
	"list_boards",
	"list_comments",
	"list_labels",
	"list_workspaces",
	"move_column",
	"move_item",
	"remove_relation",
	"transfer_item",
	"unarchive_item",
	"update_item",
}

// connectMCP starts the full HTTP stack (REST + MCP) over the shared dev
// database and returns a connected MCP session. It skips when the dev DB is
// down (fails under KANBAN_TEST_STRICT=1).
func connectMCP(t *testing.T) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	pool := testutil.OpenPool(t)

	srv := httptest.NewServer(testutil.NewMuxWithMCP(pool, events.NewHub()))
	t.Cleanup(srv.Close)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mcp-test", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool invokes a tool and returns the decoded structured content.
func callTool(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("call %s: tool error: %s", name, contentText(res))
	}
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(sc, &out); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	return out
}

func contentText(res *mcpsdk.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// errorCode invokes a tool expected to fail and returns the error envelope's code.
func errorCode(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("call %s: IsError = false, want a tool error", name)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(contentText(res)), &env); err != nil {
		t.Fatalf("decode error envelope %q: %v", contentText(res), err)
	}
	return env.Error.Code
}

// TestListTools asserts tools/list exposes exactly the documented tool set and
// that initialize carries the agent instructions.
func TestListTools(t *testing.T) {
	cs := connectMCP(t)

	ir := cs.InitializeResult()
	if ir == nil || ir.ServerInfo == nil || ir.ServerInfo.Name != "api-first-kanban" {
		t.Fatalf("initialize result = %+v", ir)
	}
	// The go-sdk client negotiates via server/discover on the stateless
	// transport, landing on the 2026-07-28 protocol the brief requires.
	if ir.ProtocolVersion != "2026-07-28" {
		t.Errorf("protocol version = %q, want 2026-07-28", ir.ProtocolVersion)
	}
	if ir.Instructions == "" {
		t.Error("server instructions are empty")
	}

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var got []string
	for _, tl := range res.Tools {
		got = append(got, tl.Name)
	}
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(wantTools) {
		t.Errorf("tools = %v, want %v", got, wantTools)
	}
	for _, tl := range res.Tools {
		if tl.InputSchema == nil {
			t.Errorf("tool %s has no input schema", tl.Name)
		}
	}
}

// TestCreateItemReturnsWrappedItem drives a create-item tool call end to end
// and checks the response mirrors the REST wrapped shape.
func TestCreateItemReturnsWrappedItem(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	ws := callTool(t, cs, "create_workspace", map[string]any{"name": "mcp-ws-" + tag, "agent": "mcp-test"})
	wsID := nested(t, ws, "workspace")["id"].(string)

	board := callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "mcp-board-" + tag, "agent": "mcp-test"})
	b := nested(t, board, "board")
	boardID := b["id"].(string)
	cols := b["columns"].([]any)
	if len(cols) != 3 {
		t.Fatalf("default columns = %d, want 3", len(cols))
	}

	out := callTool(t, cs, "create_item", map[string]any{
		"board_id": boardID, "title": "first", "owner_agent": "mcp-test", "agent": "mcp-test",
	})
	item := nested(t, out, "item")
	for _, k := range []string{"id", "board_id", "column_id", "title", "position", "labels", "closed"} {
		if _, ok := item[k]; !ok {
			t.Errorf("item missing %q: %v", k, item)
		}
	}
	if item["title"] != "first" || item["closed"] != false {
		t.Errorf("item = %v", item)
	}
	if item["board_id"] != boardID {
		t.Errorf("board_id = %v, want %s", item["board_id"], boardID)
	}
	if _, ok := item["labels"].([]any); !ok {
		t.Errorf("labels = %v, want array", item["labels"])
	}

	// Unknown item maps the service not_found sentinel onto the error envelope.
	if got := errorCode(t, cs, "get_item", map[string]any{"item_id": "00000000-0000-0000-0000-000000000000"}); got != "not_found" {
		t.Errorf("error code = %q, want not_found", got)
	}
}

// TestUpdateItemPatchSemantics pins the PATCH contract of update_item: absent
// fields keep their value, description:"" clears to null, closed toggles, and
// label_ids replaces the full set.
func TestUpdateItemPatchSemantics(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	wsID := nested(t, callTool(t, cs, "create_workspace", map[string]any{"name": "mcp-patch-ws-" + tag, "agent": "mcp-test"}), "workspace")["id"].(string)
	boardID := nested(t, callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "mcp-patch-board-" + tag, "agent": "mcp-test"}), "board")["id"].(string)
	labelA := nested(t, callTool(t, cs, "create_label", map[string]any{"workspace_id": wsID, "name": "A-" + tag, "agent": "mcp-test"}), "label")["id"].(string)
	labelB := nested(t, callTool(t, cs, "create_label", map[string]any{"workspace_id": wsID, "name": "B-" + tag, "agent": "mcp-test"}), "label")["id"].(string)

	created := nested(t, callTool(t, cs, "create_item", map[string]any{
		"board_id": boardID, "title": "t", "description": "keep me", "label_ids": []string{labelA}, "agent": "mcp-test",
	}), "item")
	itemID := created["id"].(string)
	if got := labelIDs(t, created); fmt.Sprint(got) != fmt.Sprint([]string{labelA}) {
		t.Fatalf("created labels = %v, want [%s]", got, labelA)
	}

	// Absent title/description/label_ids keep their values: patch title only.
	patched := nested(t, callTool(t, cs, "update_item", map[string]any{"item_id": itemID, "title": "renamed", "agent": "mcp-test"}), "item")
	if patched["title"] != "renamed" {
		t.Errorf("title = %v, want renamed", patched["title"])
	}
	if patched["description"] != "keep me" {
		t.Errorf("description = %v, want \"keep me\" (absent must keep)", patched["description"])
	}
	if got := labelIDs(t, patched); fmt.Sprint(got) != fmt.Sprint([]string{labelA}) {
		t.Errorf("labels = %v, want [%s] (absent must keep)", got, labelA)
	}

	// description:"" clears to null.
	if got := nested(t, callTool(t, cs, "update_item", map[string]any{"item_id": itemID, "description": "", "agent": "mcp-test"}), "item")["description"]; got != nil {
		t.Errorf("description = %v, want null", got)
	}

	// closed:true closes, closed:false reopens.
	if got := nested(t, callTool(t, cs, "update_item", map[string]any{"item_id": itemID, "closed": true, "agent": "mcp-test"}), "item")["closed"]; got != true {
		t.Errorf("closed after close = %v, want true", got)
	}
	if got := nested(t, callTool(t, cs, "update_item", map[string]any{"item_id": itemID, "closed": false, "agent": "mcp-test"}), "item")["closed"]; got != false {
		t.Errorf("closed after reopen = %v, want false", got)
	}

	// label_ids REPLACES the full set.
	replaced := nested(t, callTool(t, cs, "update_item", map[string]any{"item_id": itemID, "label_ids": []string{labelB}, "agent": "mcp-test"}), "item")
	if got := labelIDs(t, replaced); fmt.Sprint(got) != fmt.Sprint([]string{labelB}) {
		t.Errorf("labels = %v, want [%s]", got, labelB)
	}
}

// TestConflictEnvelope asserts a duplicate resource name surfaces the conflict code.
func TestConflictEnvelope(t *testing.T) {
	cs := connectMCP(t)
	name := "mcp-conflict-" + fmt.Sprintf("%d", time.Now().UnixNano())
	callTool(t, cs, "create_workspace", map[string]any{"name": name, "agent": "mcp-test"})
	if got := errorCode(t, cs, "create_workspace", map[string]any{"name": name, "agent": "mcp-test"}); got != "conflict" {
		t.Errorf("duplicate workspace code = %q, want conflict", got)
	}
}

// TestMalformedUUIDIsValidation pins the service normalization of 22P02: a
// malformed id must surface as the documented validation code, not internal.
func TestMalformedUUIDIsValidation(t *testing.T) {
	cs := connectMCP(t)
	const bad = "not-a-uuid"
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"get_board", map[string]any{"board_id": bad}},
		{"create_board", map[string]any{"workspace_id": bad, "name": "x"}},
		{"create_column", map[string]any{"board_id": bad, "name": "x"}},
	}
	for _, tc := range cases {
		if got := errorCode(t, cs, tc.tool, tc.args); got != "validation" {
			t.Errorf("%s with malformed id: code = %q, want validation", tc.tool, got)
		}
	}
}

// TestItemPriorityRoundTrip pins the MCP create_item/update_item priority
// contract: omitted defaults to 2, valid values round-trip through get_item,
// and out-of-range is the validation error envelope.
func TestItemPriorityRoundTrip(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	wsID := nested(t, callTool(t, cs, "create_workspace", map[string]any{"name": "mcp-prio-ws-" + tag, "agent": "mcp-test"}), "workspace")["id"].(string)
	boardID := nested(t, callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "mcp-prio-board-" + tag, "agent": "mcp-test"}), "board")["id"].(string)

	// Omitted → default 2.
	created := nested(t, callTool(t, cs, "create_item", map[string]any{
		"board_id": boardID, "title": "default", "agent": "mcp-test",
	}), "item")
	if got := created["priority"]; got != float64(2) {
		t.Errorf("create without priority: priority = %v, want 2", got)
	}

	// Explicit 0 survives the get_item round-trip.
	p0 := nested(t, callTool(t, cs, "create_item", map[string]any{
		"board_id": boardID, "title": "p0", "priority": 0, "agent": "mcp-test",
	}), "item")
	itemID := p0["id"].(string)
	if got := p0["priority"]; got != float64(0) {
		t.Fatalf("create priority 0: priority = %v", got)
	}
	got := nested(t, callTool(t, cs, "get_item", map[string]any{"item_id": itemID}), "item")
	if got["priority"] != float64(0) {
		t.Errorf("get_item priority = %v, want 0", got["priority"])
	}

	// update_item replaces priority; absent keeps.
	patched := nested(t, callTool(t, cs, "update_item", map[string]any{"item_id": itemID, "priority": 3, "agent": "mcp-test"}), "item")
	if patched["priority"] != float64(3) {
		t.Errorf("update priority 3: priority = %v", patched["priority"])
	}
	kept := nested(t, callTool(t, cs, "update_item", map[string]any{"item_id": itemID, "title": "renamed", "agent": "mcp-test"}), "item")
	if kept["priority"] != float64(3) {
		t.Errorf("update without priority: priority = %v, want 3 (absent must keep)", kept["priority"])
	}

	// Out of range → validation envelope.
	if code := errorCode(t, cs, "update_item", map[string]any{"item_id": itemID, "priority": 4, "agent": "mcp-test"}); code != "validation" {
		t.Errorf("update priority 4: code = %q, want validation", code)
	}
	if code := errorCode(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "bad", "priority": -1, "agent": "mcp-test"}); code != "validation" {
		t.Errorf("create priority -1: code = %q, want validation", code)
	}
}

// TestItemHierarchyTools pins the MCP mirror of the hierarchy contract:
// default kind=task, create with a milestone/epic parent, and update_item
// parent_item_id null clearing (absent keeps) with the invalid-combo envelope.
func TestItemHierarchyTools(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	wsID := nested(t, callTool(t, cs, "create_workspace", map[string]any{"name": "mcp-hier-ws-" + tag, "agent": "mcp-test"}), "workspace")["id"].(string)
	boardID := nested(t, callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "mcp-hier-board-" + tag, "agent": "mcp-test"}), "board")["id"].(string)

	// Omitted kind → task, no parent.
	def := nested(t, callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "def", "agent": "mcp-test"}), "item")
	if def["kind"] != "task" || def["parent_item_id"] != nil {
		t.Errorf("default item = %v/%v, want task/null", def["kind"], def["parent_item_id"])
	}

	ms := nested(t, callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "ms", "kind": "milestone", "agent": "mcp-test"}), "item")
	msID := ms["id"].(string)
	epic := nested(t, callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "epic", "kind": "epic", "parent_item_id": msID, "agent": "mcp-test"}), "item")
	if epic["kind"] != "epic" || epic["parent_item_id"] != msID {
		t.Errorf("epic = %v/%v, want epic/%s", epic["kind"], epic["parent_item_id"], msID)
	}

	// Invalid combo → validation envelope.
	if code := errorCode(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "bad", "kind": "milestone", "parent_item_id": msID, "agent": "mcp-test"}); code != "validation" {
		t.Errorf("milestone with parent code = %q, want validation", code)
	}

	// update_item: absent keeps, null clears.
	task := nested(t, callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "task", "parent_item_id": msID, "agent": "mcp-test"}), "item")
	taskID := task["id"].(string)
	kept := nested(t, callTool(t, cs, "update_item", map[string]any{"item_id": taskID, "title": "renamed", "agent": "mcp-test"}), "item")
	if kept["parent_item_id"] != msID {
		t.Errorf("absent parent = %v, want kept %s", kept["parent_item_id"], msID)
	}
	cleared := nested(t, callTool(t, cs, "update_item", map[string]any{"item_id": taskID, "parent_item_id": nil, "agent": "mcp-test"}), "item")
	if cleared["parent_item_id"] != nil {
		t.Errorf("null parent = %v, want null", cleared["parent_item_id"])
	}

	// A non-string parent_item_id is now a validation error (the decode moved
	// into the service layer): it surfaces as the REST envelope code rather
	// than an MCP protocol-level "invalid arguments".
	if code := errorCode(t, cs, "update_item", map[string]any{"item_id": taskID, "parent_item_id": 5, "agent": "mcp-test"}); code != "validation" {
		t.Errorf("non-string parent code = %q, want validation", code)
	}

	// get_item mirrors the composite REST detail payload: children (oldest
	// first, full item shape) let an agent read an epic's sub-tasks in one call.
	epicID := epic["id"].(string)
	sub := nested(t, callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "sub", "parent_item_id": epicID, "agent": "mcp-test"}), "item")
	detail := callTool(t, cs, "get_item", map[string]any{"item_id": epicID})
	kids, ok := detail["children"].([]any)
	if !ok {
		t.Fatalf("get_item has no children array: %v", detail)
	}
	if len(kids) != 1 || kids[0].(map[string]any)["id"] != sub["id"] {
		t.Errorf("epic children = %v, want [sub]", kids)
	}
	if _, ok := kids[0].(map[string]any)["labels"].([]any); !ok {
		t.Errorf("child has no labels array: %v", kids[0])
	}
}

// TestRelationTools drives add_relation / get_item / remove_relation end to
// end and pins the conflict (duplicate) and validation (self) envelopes.
func TestRelationTools(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	wsID := nested(t, callTool(t, cs, "create_workspace", map[string]any{"name": "mcp-rel-ws-" + tag, "agent": "mcp-test"}), "workspace")["id"].(string)
	boardID := nested(t, callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "mcp-rel-board-" + tag, "agent": "mcp-test"}), "board")["id"].(string)
	a := nested(t, callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "a", "agent": "mcp-test"}), "item")["id"].(string)
	b := nested(t, callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "b", "agent": "mcp-test"}), "item")["id"].(string)

	edge := nested(t, callTool(t, cs, "add_relation", map[string]any{
		"from_item_id": a, "to_item_id": b, "relation": "blocks", "agent": "mcp-test",
	}), "relation")
	relID := edge["id"].(string)
	if edge["relation"] != "blocks" || edge["direction"] != "outgoing" {
		t.Errorf("edge = %v, want blocks/outgoing", edge)
	}

	// get_item carries both directions; the target sees the derived incoming edge.
	detail := callTool(t, cs, "get_item", map[string]any{"item_id": b})
	rels := nested(t, detail, "relations")
	if n := len(rels["incoming"].([]any)); n != 1 {
		t.Errorf("B incoming = %d, want 1", n)
	}

	// Duplicate -> conflict; self-relation and bad enum -> validation.
	if got := errorCode(t, cs, "add_relation", map[string]any{"from_item_id": a, "to_item_id": b, "relation": "blocks"}); got != "conflict" {
		t.Errorf("duplicate relation code = %q, want conflict", got)
	}
	if got := errorCode(t, cs, "add_relation", map[string]any{"from_item_id": a, "to_item_id": a, "relation": "blocks"}); got != "validation" {
		t.Errorf("self relation code = %q, want validation", got)
	}
	if got := errorCode(t, cs, "add_relation", map[string]any{"from_item_id": a, "to_item_id": b, "relation": "depends_on"}); got != "validation" {
		t.Errorf("unknown relation code = %q, want validation", got)
	}

	callTool(t, cs, "remove_relation", map[string]any{"relation_id": relID, "agent": "mcp-test"})
	if got := errorCode(t, cs, "remove_relation", map[string]any{"relation_id": relID}); got != "not_found" {
		t.Errorf("remove twice code = %q, want not_found", got)
	}
}

// TestArchiveToolsRoundTrip pins the MCP archive_item/unarchive_item mirror:
// item payloads carry archived_at, get_board hides archived items by default
// and restores them with include_archived, and the 409 conflicts surface as the
// conflict envelope.
func TestArchiveToolsRoundTrip(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	wsID := nested(t, callTool(t, cs, "create_workspace", map[string]any{"name": "mcp-arch-ws-" + tag, "agent": "mcp-test"}), "workspace")["id"].(string)
	boardID := nested(t, callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "mcp-arch-board-" + tag, "agent": "mcp-test"}), "board")["id"].(string)
	itemID := nested(t, callTool(t, cs, "create_item", map[string]any{"board_id": boardID, "title": "arch", "agent": "mcp-test"}), "item")["id"].(string)

	arch := nested(t, callTool(t, cs, "archive_item", map[string]any{"item_id": itemID, "agent": "mcp-test"}), "item")
	if arch["archived_at"] == nil {
		t.Errorf("archive_item archived_at = %v, want non-null", arch["archived_at"])
	}

	def := callTool(t, cs, "get_board", map[string]any{"board_id": boardID})
	if boardHasItem(t, def, itemID) {
		t.Errorf("get_board default still contains archived item")
	}
	inc := callTool(t, cs, "get_board", map[string]any{"board_id": boardID, "include_archived": true})
	if !boardHasItem(t, inc, itemID) {
		t.Errorf("get_board include_archived does not contain the archived item")
	}

	if got := errorCode(t, cs, "archive_item", map[string]any{"item_id": itemID, "agent": "mcp-test"}); got != "conflict" {
		t.Errorf("double archive code = %q, want conflict", got)
	}
	un := nested(t, callTool(t, cs, "unarchive_item", map[string]any{"item_id": itemID, "agent": "mcp-test"}), "item")
	if un["archived_at"] != nil {
		t.Errorf("unarchive_item archived_at = %v, want null", un["archived_at"])
	}
	if got := errorCode(t, cs, "unarchive_item", map[string]any{"item_id": itemID, "agent": "mcp-test"}); got != "conflict" {
		t.Errorf("unarchive of active code = %q, want conflict", got)
	}
}

// boardHasItem reports whether a get_board snapshot contains the item id.
func boardHasItem(t *testing.T, snap map[string]any, itemID string) bool {
	t.Helper()
	raw, ok := snap["items"].([]any)
	if !ok {
		t.Fatalf("snapshot items = %T, want array", snap["items"])
	}
	for _, r := range raw {
		if r.(map[string]any)["id"] == itemID {
			return true
		}
	}
	return false
}

// labelIDs returns the sorted label ids attached to an item.
func labelIDs(t *testing.T, item map[string]any) []string {
	t.Helper()
	raw, ok := item["labels"].([]any)
	if !ok {
		t.Fatalf("labels = %v, want array", item["labels"])
	}
	ids := make([]string, 0, len(raw))
	for _, l := range raw {
		ids = append(ids, l.(map[string]any)["id"].(string))
	}
	sort.Strings(ids)
	return ids
}

func nested(t *testing.T, out map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := out[key].(map[string]any)
	if !ok {
		t.Fatalf("response missing object %q: %v", key, out)
	}
	return v
}

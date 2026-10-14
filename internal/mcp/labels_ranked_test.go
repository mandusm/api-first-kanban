package mcp_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestListLabelsRankedTopAndQuery covers the PB-73 list_labels extensions:
// optional top (usage-ranked, ties by name) and query (case-insensitive
// substring, filter-then-rank), usage_count in the payload, no-params
// behavior, and top validation.
func TestListLabelsRankedTopAndQuery(t *testing.T) {
	cs := connectMCP(t)
	tag := fmt.Sprintf("%d", time.Now().UnixNano())

	wsID := nested(t, callTool(t, cs, "create_workspace", map[string]any{"name": "mcp-labelrank-ws-" + tag, "agent": "mcp-test"}), "workspace")["id"].(string)

	mkLabel := func(name string) string {
		return nested(t, callTool(t, cs, "create_label", map[string]any{"workspace_id": wsID, "name": name, "agent": "mcp-test"}), "label")["id"].(string)
	}
	boardID := nested(t, callTool(t, cs, "create_board", map[string]any{"workspace_id": wsID, "name": "board-" + tag, "agent": "mcp-test"}), "board")["id"].(string)

	// feat used twice, ui and bug once each, zeta/alpha zero.
	feat, bug := mkLabel("feat-"+tag), mkLabel("bug-"+tag) // usage: feat 2, bug 1
	mkLabel("zeta-" + tag)                                 // zero usage
	mkLabel("alpha-" + tag)                                // zero usage
	for i := 0; i < 2; i++ {
		callTool(t, cs, "create_item", map[string]any{
			"board_id": boardID, "title": fmt.Sprintf("it-%d-%s", i, tag),
			"label_ids": []string{feat}, "agent": "mcp-test",
		})
	}
	ui := mkLabel("ui-" + tag)
	callTool(t, cs, "create_item", map[string]any{
		"board_id": boardID, "title": "it-ui-" + tag,
		"label_ids": []string{ui}, "agent": "mcp-test",
	})
	callTool(t, cs, "create_item", map[string]any{
		"board_id": boardID, "title": "it-bug-" + tag,
		"label_ids": []string{bug}, "agent": "mcp-test",
	})
	// Without params: LEGACY shape, byte-identical to the REST no-param
	// response (see cmd/api/labels_ranked_test.go, which pins the same
	// contract): every label, name-ordered, exactly the v1 keys, no
	// usage_count. The legacy-vs-ranked branch lives in the service layer,
	// so both transports cannot diverge.
	all := labelsOf(t, callTool(t, cs, "list_labels", map[string]any{"workspace_id": wsID}))
	if len(all) != 5 {
		t.Fatalf("list_labels = %d labels, want 5", len(all))
	}
	if got := namesOf(all); !sort.StringsAreSorted(got) {
		t.Errorf("legacy order %v not name-sorted", got)
	}
	for _, l := range all {
		m := l.(map[string]any)
		keys := keysOf(m)
		if fmt.Sprint(keys) != fmt.Sprint([]string{"color", "id", "name", "workspace_id"}) {
			t.Errorf("legacy label keys = %v, want exactly [color id name workspace_id] (no usage_count)", keys)
		}
	}

	// Whitespace-only query counts as blank/absent → legacy shape (PB-80
	// review ruling N1; mirrors the REST ?q=%20 pin).
	blankQ := labelsOf(t, callTool(t, cs, "list_labels", map[string]any{"workspace_id": wsID, "query": " "}))
	if len(blankQ) != 5 {
		t.Fatalf("query=\" \" = %d labels, want 5", len(blankQ))
	}
	for _, l := range blankQ {
		keys := keysOf(l.(map[string]any))
		if fmt.Sprint(keys) != fmt.Sprint([]string{"color", "id", "name", "workspace_id"}) {
			t.Errorf("blank-query label keys = %v, want legacy shape (no usage_count)", keys)
		}
	}

	// Ranked path (top): usage desc / name asc, with usage_count.
	ranked := labelsOf(t, callTool(t, cs, "list_labels", map[string]any{"workspace_id": wsID, "top": 5}))
	if first := ranked[0].(map[string]any); first["name"].(string) != "feat-"+tag || first["usage_count"].(float64) != 2 {
		t.Errorf("first ranked label = %v, want feat with usage_count 2", first)
	}
	for _, l := range ranked {
		if _, ok := l.(map[string]any)["usage_count"]; !ok {
			t.Fatalf("label %v missing usage_count", l)
		}
	}

	// top=N truncates the ranked list; ui/bug tie at 1 usage, "bug" < "ui".
	top2 := labelsOf(t, callTool(t, cs, "list_labels", map[string]any{"workspace_id": wsID, "top": 2}))
	if got := namesOf(top2); fmt.Sprint(got) != fmt.Sprint([]string{"feat-" + tag, "bug-" + tag}) {
		t.Errorf("top=2 = %v, want [feat bug]", got)
	}

	// query filters case-insensitively before ranking; combines with top.
	q := labelsOf(t, callTool(t, cs, "list_labels", map[string]any{"workspace_id": wsID, "query": "FEAT"}))
	if got := namesOf(q); fmt.Sprint(got) != fmt.Sprint([]string{"feat-" + tag}) {
		t.Errorf("query=FEAT = %v, want [feat]", got)
	}
	qt := labelsOf(t, callTool(t, cs, "list_labels", map[string]any{"workspace_id": wsID, "query": tag, "top": 3}))
	if got := namesOf(qt); fmt.Sprint(got) != fmt.Sprint([]string{"feat-" + tag, "bug-" + tag, "ui-" + tag}) {
		t.Errorf("query+top = %v, want [feat bug ui]", got)
	}

	// top < 1 → validation error.
	if code := errorCode(t, cs, "list_labels", map[string]any{"workspace_id": wsID, "top": 0}); code != "validation" {
		t.Errorf("top=0 error code = %q, want validation", code)
	}

	// The DECLARED schema must match the Go handler types (PB-73 review
	// blocker): top is an integer, query a string. A schema-valid string top
	// used to fail unmarshaling.
	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range lt.Tools {
		if tool.Name != "list_labels" {
			continue
		}
		p := schemaProps(tool)
		topSchema, _ := p["top"].(map[string]any)
		if topSchema["type"] != "integer" {
			t.Errorf("list_labels top schema type = %v, want integer", topSchema["type"])
		}
		querySchema, _ := p["query"].(map[string]any)
		if querySchema["type"] != "string" {
			t.Errorf("list_labels query schema type = %v, want string", querySchema["type"])
		}
	}

	// unknown workspace → 404.
	if code := errorCode(t, cs, "list_labels", map[string]any{"workspace_id": "00000000-0000-0000-0000-000000000000", "top": 3}); code != "not_found" {
		t.Errorf("unknown workspace code = %q, want not_found", code)
	}
}

// schemaProps extracts a tool's declared input-schema properties map.
func schemaProps(tool *mcpsdk.Tool) map[string]any {
	m, _ := tool.InputSchema.(map[string]any)
	p, _ := m["properties"].(map[string]any)
	return p
}

// labelsOf extracts the labels array from a list_labels result.
func labelsOf(t *testing.T, out map[string]any) []any {
	t.Helper()
	raw, ok := out["labels"].([]any)
	if !ok {
		t.Fatalf("labels = %T, want array", out["labels"])
	}
	return raw
}

// namesOf maps a labels array to its names.
func namesOf(labels []any) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, l.(map[string]any)["name"].(string))
	}
	return out
}

// keysOf returns a label object's JSON keys (for legacy-shape assertions).
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

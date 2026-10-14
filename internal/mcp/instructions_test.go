package mcp

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestInstructionsCoverV11Capabilities pins the agent guidance returned on
// initialize: a fresh agent must be able to discover the v1.1 priority scale,
// hierarchy rules and relation semantics from the instructions alone. The
// instructions are a pure constant, so this test needs no database.
func TestInstructionsCoverV11Capabilities(t *testing.T) {
	wants := []string{
		// priority scale
		"P0 blocker", "P1 high", "P2 normal", "P3 low", "defaults to P2",
		// hierarchy
		"milestone", "epic", "task", "three levels deep", "cycle",
		"milestone has no parent", "epic's parent, when present, must be a milestone", "parentless epic is a valid top-level item",
		// relations
		"blocks", "relates_to", "duplicates", "blocked by",
		// comments
		"add_comment", "list_comments", "Markdown", "raw Markdown source", "rendered as sanitized Markdown", "headings, lists, code, links",
		// three-state PATCH
		"parent_item_id absent keeps", "null clears",
	}
	for _, w := range wants {
		if !strings.Contains(instructions, w) {
			t.Errorf("server instructions do not mention %q", w)
		}
	}
}

// TestInstructionsCoverArchive pins the archive lifecycle guidance in the MCP
// server instructions so a fresh agent can discover it from /mcp alone.
func TestInstructionsCoverArchive(t *testing.T) {
	wants := []string{
		"archive_item", "unarchive_item", "archived_at",
		"independent of closed", "include_archived", "never cascades",
	}
	for _, w := range wants {
		if !strings.Contains(instructions, w) {
			t.Errorf("server instructions do not mention %q", w)
		}
	}
}

// TestItemToolSchemasExposeV11Fields asserts create_item/update_item expose the
// v1.1 hierarchy + priority fields, and that update_item's three-state
// parent_item_id is declared nullable. Uses an in-memory transport with a nil
// service: listing tools never invokes the service, so no database is needed.
func TestItemToolSchemasExposeV11Fields(t *testing.T) {
	ctx := context.Background()
	ct, st := mcpsdk.NewInMemoryTransports()
	srv := NewServer(nil)
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "schema-test", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	tools := make(map[string]*mcpsdk.Tool, len(res.Tools))
	for _, tl := range res.Tools {
		tools[tl.Name] = tl
	}

	for _, name := range []string{"create_item", "update_item"} {
		tool := tools[name]
		if tool == nil {
			t.Fatalf("missing tool %s", name)
		}
		for _, field := range []string{"priority", "kind", "parent_item_id"} {
			if _, ok := props(tool)[field]; !ok {
				t.Errorf("%s schema missing %q property", name, field)
			}
		}
	}

	// update_item.parent_item_id is three-state (absent/null/string): the
	// schema must accept null, not just string.
	if !acceptsNull(props(tools["update_item"])["parent_item_id"]) {
		t.Errorf("update_item.parent_item_id schema does not accept null: %v",
			props(tools["update_item"])["parent_item_id"])
	}
}

func props(t *mcpsdk.Tool) map[string]any {
	m, _ := t.InputSchema.(map[string]any)
	p, _ := m["properties"].(map[string]any)
	return p
}

func acceptsNull(prop any) bool {
	m, ok := prop.(map[string]any)
	if !ok {
		return false
	}
	switch v := m["type"].(type) {
	case string:
		return v == "null"
	case []any:
		for _, t := range v {
			if t == "null" {
				return true
			}
		}
	case []string:
		for _, t := range v {
			if t == "null" {
				return true
			}
		}
	}
	return false
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// itemID creates an item on sd's first column and returns its id.
func itemID(t *testing.T, st boardStore, sd seeded, title string) string {
	t.Helper()
	out := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":%q,"column_id":%q,"agent":"test"}`, title, sd.columns[0]), http.StatusCreated)
	return out["item"].(map[string]any)["id"].(string)
}

// TestRelationLifecycle drives the REST relation endpoints end to end: create
// (outgoing + derived incoming views), duplicate 409, self 400, malformed enum
// 400, delete 204 and absence afterwards.
func TestRelationLifecycle(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	a := itemID(t, st, sd, "rel-a-"+tag)
	b := itemID(t, st, sd, "rel-b-"+tag)

	// A blocks B.
	cout := st.do(t, "POST", "/api/items/"+a+"/relations",
		fmt.Sprintf(`{"to_item_id":%q,"relation":"blocks","agent":"test"}`, b), http.StatusCreated)
	rel := cout["relation"].(map[string]any)
	relID := rel["id"].(string)
	if rel["relation"] != "blocks" || rel["direction"] != "outgoing" {
		t.Errorf("relation = %v, want relation=blocks direction=outgoing", rel)
	}
	if got := rel["item"].(map[string]any)["id"]; got != b {
		t.Errorf("outgoing edge item = %v, want target %s", got, b)
	}

	// GET A: outgoing edge; GET B: the DERIVED incoming ("blocked by") edge.
	detailA := st.do(t, "GET", "/api/items/"+a, "", http.StatusOK)
	relsA := detailA["relations"].(map[string]any)
	if n := len(relsA["outgoing"].([]any)); n != 1 {
		t.Errorf("A outgoing = %d, want 1", n)
	}
	if n := len(relsA["incoming"].([]any)); n != 0 {
		t.Errorf("A incoming = %d, want 0", n)
	}
	detailB := st.do(t, "GET", "/api/items/"+b, "", http.StatusOK)
	incoming := detailB["relations"].(map[string]any)["incoming"].([]any)
	if len(incoming) != 1 {
		t.Fatalf("B incoming = %d, want 1 (blocked-by view)", len(incoming))
	}
	edge := incoming[0].(map[string]any)
	if edge["direction"] != "incoming" || edge["relation"] != "blocks" {
		t.Errorf("incoming edge = %v, want direction=incoming relation=blocks", edge)
	}
	if got := edge["item"].(map[string]any)["id"]; got != a {
		t.Errorf("incoming edge item = %v, want source %s", got, a)
	}
	if _, ok := edge["item"].(map[string]any)["labels"].([]any); !ok {
		t.Errorf("incoming edge item has no labels array: %v", edge["item"])
	}

	// relation.created event carries the outgoing edge under the relation key.
	assertRelationEvent(t, pool, "relation.created", relID)

	// Exact duplicate -> 409/conflict.
	if status, code := errDo(t, mux, "POST", "/api/items/"+a+"/relations",
		fmt.Sprintf(`{"to_item_id":%q,"relation":"blocks"}`, b)); status != http.StatusConflict || code != "conflict" {
		t.Errorf("duplicate relation = %d/%q, want 409/conflict", status, code)
	}

	// Delete -> 204, then GET shows no edges.
	if status, _ := errDo(t, mux, "DELETE", "/api/relations/"+relID, ""); status != http.StatusNoContent {
		t.Fatalf("delete relation = %d, want 204", status)
	}
	assertRelationEvent(t, pool, "relation.deleted", relID)
	after := st.do(t, "GET", "/api/items/"+a, "", http.StatusOK)["relations"].(map[string]any)
	if len(after["outgoing"].([]any)) != 0 || len(after["incoming"].([]any)) != 0 {
		t.Errorf("relations after delete = %v, want empty", after)
	}
}

// TestRelationValidation pins the service guards: self-relations, unknown
// relation values, and cross-workspace pairs are all 400/validation.
func TestRelationValidation(t *testing.T) {
	mux := testMux(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	other := seedBoard(t, st, "other-"+tag)
	a := itemID(t, st, sd, "val-a-"+tag)
	b := itemID(t, st, sd, "val-b-"+tag)
	foreign := itemID(t, st, other, "val-x-"+tag)

	cases := []struct {
		name, path, body string
	}{
		{"self relation", "/api/items/" + a + "/relations", fmt.Sprintf(`{"to_item_id":%q,"relation":"blocks"}`, a)},
		{"unknown relation", "/api/items/" + a + "/relations", fmt.Sprintf(`{"to_item_id":%q,"relation":"depends_on"}`, b)},
		{"empty relation", "/api/items/" + a + "/relations", fmt.Sprintf(`{"to_item_id":%q}`, b)},
		{"cross workspace", "/api/items/" + a + "/relations", fmt.Sprintf(`{"to_item_id":%q,"relation":"blocks"}`, foreign)},
		{"malformed to_item_id", "/api/items/" + a + "/relations", `{"to_item_id":"nope","relation":"blocks"}`},
	}
	for _, tc := range cases {
		if status, code := errDo(t, mux, "POST", tc.path, tc.body); status != http.StatusBadRequest || code != "validation" {
			t.Errorf("%s = %d/%q, want 400/validation", tc.name, status, code)
		}
	}
}

// TestRelationSameWorkspaceAllowsCrossBoard pins that "same workspace" (not
// "same board") is the constraint: items on different boards of one workspace
// may relate.
func TestRelationSameWorkspaceAllowsCrossBoard(t *testing.T) {
	mux := testMux(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	bout := st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/boards",
		`{"name":"rel-board2-`+tag+`","agent":"test"}`, http.StatusCreated)
	board2 := bout["board"].(map[string]any)
	col2 := board2["columns"].([]any)[0].(map[string]any)["id"].(string)
	a := itemID(t, st, sd, "cross-a-"+tag)
	out := st.do(t, "POST", "/api/boards/"+board2["id"].(string)+"/items",
		fmt.Sprintf(`{"title":"cross-b-%s","column_id":%q,"agent":"test"}`, tag, col2), http.StatusCreated)
	b := out["item"].(map[string]any)["id"].(string)

	st.do(t, "POST", "/api/items/"+a+"/relations",
		fmt.Sprintf(`{"to_item_id":%q,"relation":"relates_to","agent":"test"}`, b), http.StatusCreated)
}

// assertRelationEvent loads the newest event of typ for the relation id and
// checks it carries the relation entity in the documented shape.
func assertRelationEvent(t *testing.T, pool *pgxpool.Pool, typ, relID string) {
	t.Helper()
	var raw []byte
	err := pool.QueryRow(context.Background(),
		`SELECT payload FROM events WHERE type = $1 AND payload->'relation'->>'id' = $2 ORDER BY id DESC LIMIT 1`,
		typ, relID).Scan(&raw)
	if err != nil {
		t.Fatalf("load %s event: %v", typ, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode %s payload: %v", typ, err)
	}
	rel, ok := payload["relation"].(map[string]any)
	if !ok {
		t.Fatalf("%s: payload has no relation object: %v", typ, payload)
	}
	if rel["id"] != relID {
		t.Errorf("%s: relation.id = %v, want %s", typ, rel["id"], relID)
	}
	if _, ok := rel["item"].(map[string]any); !ok {
		t.Errorf("%s: relation.item missing: %v", typ, rel)
	}
}

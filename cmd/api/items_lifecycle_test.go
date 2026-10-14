package main

import (
	"fmt"
	"net/http"
	"testing"
)

// TestItemLifecycleREST drives an item through the full v1 lifecycle over
// HTTP: create with labels, get, PATCH keep/clear semantics, move within and
// across columns, transfer (auto-handoff comment) and close/reopen.
func TestItemLifecycleREST(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	colTodo, colDoing := sd.columns[0], sd.columns[1]

	lout := st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/labels",
		`{"name":"life-`+tag+`","color":"#ff0000","agent":"test"}`, http.StatusCreated)
	lid := lout["label"].(map[string]any)["id"].(string)

	// --- create with labels (label_ids at create time) ---
	cout := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"life-%s","description":"desc","column_id":%q,"owner_agent":"alice","label_ids":[%q],"agent":"test"}`, tag, colTodo, lid),
		http.StatusCreated)
	item := cout["item"].(map[string]any)
	iid := item["id"].(string)
	if item["title"] != "life-"+tag || item["description"] != "desc" || item["owner_agent"] != "alice" {
		t.Fatalf("created item = %v", item)
	}
	if item["closed"] != false || item["position"] != "1" || item["column_id"] != colTodo {
		t.Errorf("created closed/position/column = %v/%v/%v, want false/1/%s", item["closed"], item["position"], item["column_id"], colTodo)
	}
	if got := itemLabelNames(t, item); fmt.Sprint(got) != fmt.Sprint([]string{"life-" + tag}) {
		t.Errorf("created labels = %v, want [life-%s]", got, tag)
	}

	// --- get: item + comments array ---
	gout := st.do(t, "GET", "/api/items/"+iid, "", http.StatusOK)
	gitem := gout["item"].(map[string]any)
	if gitem["id"] != iid {
		t.Fatalf("get item id = %v, want %s", gitem["id"], iid)
	}
	if _, ok := gout["comments"].([]any); !ok {
		t.Errorf("get comments = %T, want array", gout["comments"])
	}

	// --- PATCH: absent keeps, "" clears, empty title present -> 400 ---
	kept := st.do(t, "PATCH", "/api/items/"+iid, `{"title":"life-`+tag+`-renamed","agent":"test"}`, http.StatusOK)["item"].(map[string]any)
	if kept["title"] != "life-"+tag+"-renamed" {
		t.Errorf("patched title = %v, want renamed", kept["title"])
	}
	if kept["description"] != "desc" || kept["owner_agent"] != "alice" {
		t.Errorf("absent fields must keep: description=%v owner_agent=%v", kept["description"], kept["owner_agent"])
	}
	cleared := st.do(t, "PATCH", "/api/items/"+iid, `{"description":"","agent":"test"}`, http.StatusOK)["item"].(map[string]any)
	if cleared["description"] != nil {
		t.Errorf(`description:"" = %v, want null (cleared)`, cleared["description"])
	}
	// Once cleared, an absent description keeps the null (it does not resurrect).
	after := st.do(t, "PATCH", "/api/items/"+iid, `{"title":"life-`+tag+`","agent":"test"}`, http.StatusOK)["item"].(map[string]any)
	if after["description"] != nil {
		t.Errorf("absent description after clear = %v, want null", after["description"])
	}
	st.do(t, "PATCH", "/api/items/"+iid, `{"title":"   ","agent":"test"}`, http.StatusBadRequest)

	// --- move within the column: index 0 goes to the top ---
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"sib-%s","column_id":%q,"agent":"test"}`, tag, colTodo), http.StatusCreated)
	top := itemsOfColumn(t, st, sd.boardID, colTodo)
	if top[0]["id"] == iid {
		t.Fatalf("setup: item unexpectedly already at the top")
	}
	st.do(t, "POST", "/api/items/"+iid+"/move",
		fmt.Sprintf(`{"column_id":%q,"index":0,"agent":"test"}`, colTodo), http.StatusOK)
	if got := itemsOfColumn(t, st, sd.boardID, colTodo); got[0]["id"] != iid {
		t.Errorf("after move to index 0 top item = %v, want %s", got[0]["id"], iid)
	}

	// --- move across columns: into an empty column -> position "1" ---
	mout := st.do(t, "POST", "/api/items/"+iid+"/move",
		fmt.Sprintf(`{"column_id":%q,"agent":"test"}`, colDoing), http.StatusOK)["item"].(map[string]any)
	if mout["column_id"] != colDoing || mout["position"] != "1" {
		t.Errorf("moved item column/position = %v/%v, want %s/1", mout["column_id"], mout["position"], colDoing)
	}

	// --- move to a column of another board -> 400 ---
	other := seedBoard(t, st, "move-"+tag)
	st.do(t, "POST", "/api/items/"+iid+"/move",
		fmt.Sprintf(`{"column_id":%q,"agent":"test"}`, other.columns[0]), http.StatusBadRequest)

	// --- transfer: sets owner + auto-posts the handoff audit comment ---
	tout := st.do(t, "POST", "/api/items/"+iid+"/transfer",
		`{"to_agent":"bob","note":"please take","agent":"carol"}`, http.StatusOK)["item"].(map[string]any)
	if tout["owner_agent"] != "bob" {
		t.Errorf("transfer owner_agent = %v, want bob", tout["owner_agent"])
	}
	handoff := lastComment(t, st, iid)
	if handoff["body"] != "[handoff] alice → bob — please take" {
		t.Errorf("handoff body = %q, want %q", handoff["body"], "[handoff] alice → bob — please take")
	}
	if handoff["author_name"] != "carol" || handoff["author_type"] != "agent" {
		t.Errorf("handoff author = %v/%v, want carol/agent", handoff["author_name"], handoff["author_type"])
	}

	// Empty prior owner -> "→ <to>" without a from.
	nout := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"new-%s","column_id":%q,"agent":"test"}`, tag, colDoing), http.StatusCreated)["item"].(map[string]any)
	nid := nout["id"].(string)
	st.do(t, "POST", "/api/items/"+nid+"/transfer", `{"to_agent":"dave","agent":"test"}`, http.StatusOK)
	if got := lastComment(t, st, nid)["body"]; got != "[handoff] → dave" {
		t.Errorf("handoff without prior owner = %q, want %q", got, "[handoff] → dave")
	}
	// Missing to_agent -> 400.
	st.do(t, "POST", "/api/items/"+iid+"/transfer", `{"agent":"test"}`, http.StatusBadRequest)

	// --- close / reopen via PATCH closed ---
	if got := st.do(t, "PATCH", "/api/items/"+iid, `{"closed":true,"agent":"test"}`, http.StatusOK)["item"].(map[string]any)["closed"]; got != true {
		t.Errorf("closed after close = %v, want true", got)
	}
	reopened := st.do(t, "PATCH", "/api/items/"+iid, `{"closed":false,"agent":"test"}`, http.StatusOK)["item"].(map[string]any)
	if reopened["closed"] != false {
		t.Errorf("closed after reopen = %v, want false", reopened["closed"])
	}
	if reopened["title"] != "life-"+tag {
		t.Errorf("reopen clobbered title: %v", reopened["title"])
	}
}

// itemsOfColumn returns the board snapshot's items for one column, in the
// snapshot's documented order (column position, item position, created_at).
func itemsOfColumn(t *testing.T, st boardStore, boardID, columnID string) []map[string]any {
	t.Helper()
	snap := st.do(t, "GET", "/api/boards/"+boardID, "", http.StatusOK)
	raw, ok := snap["items"].([]any)
	if !ok {
		t.Fatalf("snapshot items = %T, want array", snap["items"])
	}
	var out []map[string]any
	for _, r := range raw {
		it := r.(map[string]any)
		if it["column_id"] == columnID {
			out = append(out, it)
		}
	}
	return out
}

// itemLabelNames returns the (ORDER BY name) label names attached to an item.
func itemLabelNames(t *testing.T, item map[string]any) []string {
	t.Helper()
	raw, ok := item["labels"].([]any)
	if !ok {
		t.Fatalf("item labels = %T, want array", item["labels"])
	}
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		out = append(out, l.(map[string]any)["name"].(string))
	}
	return out
}

// lastComment returns the newest comment of an item via GET /api/items/{id}.
func lastComment(t *testing.T, st boardStore, itemID string) map[string]any {
	t.Helper()
	out := st.do(t, "GET", "/api/items/"+itemID, "", http.StatusOK)
	comments, ok := out["comments"].([]any)
	if !ok || len(comments) == 0 {
		t.Fatalf("comments = %v, want at least one", out["comments"])
	}
	return comments[len(comments)-1].(map[string]any)
}

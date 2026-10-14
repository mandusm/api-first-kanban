package main

import (
	"fmt"
	"net/http"
	"testing"
)

// TestLabelsWorkspaceScoped covers workspace-scoped label CRUD: create
// (default + explicit color), duplicate 409, empty-name 400, unknown-workspace
// 404, list ordering, update, delete, and attach/detach through an item's
// label_ids (which REPLACES the set) including cross-workspace rejection.
func TestLabelsWorkspaceScoped(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)

	// A fresh workspace has no labels.
	if got := st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels", "", http.StatusOK)["labels"].([]any); len(got) != 0 {
		t.Errorf("fresh workspace labels = %v, want []", got)
	}

	// create: default color, then explicit color.
	a := st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/labels",
		`{"name":"lbl-a-`+tag+`","agent":"test"}`, http.StatusCreated)["label"].(map[string]any)
	if a["color"] != "#6366f1" || a["workspace_id"] != sd.wsID {
		t.Errorf("default label = %v, want color #6366f1 in %s", a, sd.wsID)
	}
	b := st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/labels",
		`{"name":"lbl-b-`+tag+`","color":"#00ff00","agent":"test"}`, http.StatusCreated)["label"].(map[string]any)
	if b["color"] != "#00ff00" {
		t.Errorf("label color = %v, want #00ff00", b["color"])
	}
	aID, bID := a["id"].(string), b["id"].(string)

	// duplicate name -> 409; empty name -> 400; unknown workspace -> 404.
	st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/labels", `{"name":"lbl-a-`+tag+`"}`, http.StatusConflict)
	st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/labels", `{"name":"   "}`, http.StatusBadRequest)
	const missing = "00000000-0000-0000-0000-000000000000"
	st.do(t, "GET", "/api/workspaces/"+missing+"/labels", "", http.StatusNotFound)
	st.do(t, "POST", "/api/workspaces/"+missing+"/labels", `{"name":"x"}`, http.StatusNotFound)

	// list is workspace-scoped and name-ordered.
	names := listLabelNames(t, st.do(t, "GET", "/api/workspaces/"+sd.wsID+"/labels", "", http.StatusOK))
	if fmt.Sprint(names) != fmt.Sprint([]string{"lbl-a-" + tag, "lbl-b-" + tag}) {
		t.Errorf("labels = %v, want [lbl-a-%s lbl-b-%s]", names, tag, tag)
	}

	// update both fields; duplicate rename -> 409; empty rename -> 400.
	upd := st.do(t, "PATCH", "/api/labels/"+bID,
		`{"name":"lbl-b2-`+tag+`","color":"#000000","agent":"test"}`, http.StatusOK)["label"].(map[string]any)
	if upd["name"] != "lbl-b2-"+tag || upd["color"] != "#000000" {
		t.Errorf("updated label = %v", upd)
	}
	st.do(t, "PATCH", "/api/labels/"+bID, `{"name":"lbl-a-`+tag+`"}`, http.StatusConflict)
	st.do(t, "PATCH", "/api/labels/"+bID, `{"name":""}`, http.StatusBadRequest)

	// attach/detach via item label_ids (replace semantics).
	item := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"lab-%s","label_ids":[%q],"agent":"test"}`, tag, aID), http.StatusCreated)["item"].(map[string]any)
	iid := item["id"].(string)
	if got := itemLabelIDs(t, item); fmt.Sprint(got) != fmt.Sprint([]string{aID}) {
		t.Fatalf("attached labels = %v, want [%s]", got, aID)
	}
	replaced := st.do(t, "PATCH", "/api/items/"+iid, fmt.Sprintf(`{"label_ids":[%q],"agent":"test"}`, bID), http.StatusOK)["item"].(map[string]any)
	if got := itemLabelIDs(t, replaced); fmt.Sprint(got) != fmt.Sprint([]string{bID}) {
		t.Errorf("replaced labels = %v, want [%s] (label_ids replaces the set)", got, bID)
	}
	detached := st.do(t, "PATCH", "/api/items/"+iid, `{"label_ids":[],"agent":"test"}`, http.StatusOK)["item"].(map[string]any)
	if got := itemLabelIDs(t, detached); len(got) != 0 {
		t.Errorf("detached labels = %v, want [] (empty array clears)", got)
	}

	// cross-workspace label -> 400 on both create and patch.
	foreign := seedBoard(t, st, "lab-"+tag)
	foreignLabel := st.do(t, "POST", "/api/workspaces/"+foreign.wsID+"/labels",
		`{"name":"foreign-`+tag+`","agent":"test"}`, http.StatusCreated)["label"].(map[string]any)["id"].(string)
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"x-%s","label_ids":[%q]}`, tag, foreignLabel), http.StatusBadRequest)
	st.do(t, "PATCH", "/api/items/"+iid, fmt.Sprintf(`{"label_ids":[%q]}`, foreignLabel), http.StatusBadRequest)

	// delete -> 204; second delete -> 404.
	st.do(t, "DELETE", "/api/labels/"+aID, "", http.StatusNoContent)
	st.do(t, "DELETE", "/api/labels/"+aID, "", http.StatusNotFound)
}

// listLabelNames returns the label names of a {"labels":[...]} response.
func listLabelNames(t *testing.T, out map[string]any) []string {
	t.Helper()
	raw, ok := out["labels"].([]any)
	if !ok {
		t.Fatalf("labels = %T, want array", out["labels"])
	}
	names := make([]string, 0, len(raw))
	for _, l := range raw {
		names = append(names, l.(map[string]any)["name"].(string))
	}
	return names
}

// itemLabelIDs returns the label ids attached to an item.
func itemLabelIDs(t *testing.T, item map[string]any) []string {
	t.Helper()
	raw, ok := item["labels"].([]any)
	if !ok {
		t.Fatalf("item labels = %T, want array", item["labels"])
	}
	ids := make([]string, 0, len(raw))
	for _, l := range raw {
		ids = append(ids, l.(map[string]any)["id"].(string))
	}
	return ids
}

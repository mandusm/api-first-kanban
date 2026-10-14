package main

import (
	"fmt"
	"net/http"
	"testing"
)

// TestItemPriorityCreateDefaultAndEnum pins the priority contract on REST
// create: omitted defaults to 2 (P2 normal), 0..3 are stored verbatim, and a
// value outside the enum is a 400 validation error.
func TestItemPriorityCreateDefaultAndEnum(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	// Omitted → default 2.
	def := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"default-%s","column_id":%q,"agent":"test"}`, tag, col), http.StatusCreated)
	if got := def["item"].(map[string]any)["priority"].(float64); got != 2 {
		t.Errorf("create without priority: priority = %v, want 2", got)
	}

	// Every enum value round-trips.
	for p := 0; p <= 3; p++ {
		out := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
			fmt.Sprintf(`{"title":"p%d-%s","column_id":%q,"priority":%d,"agent":"test"}`, p, tag, col, p), http.StatusCreated)
		if got := out["item"].(map[string]any)["priority"].(float64); got != float64(p) {
			t.Errorf("create priority %d: got %v", p, got)
		}
	}

	// Out of range → 400.
	for _, bad := range []int{-1, 4} {
		st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
			fmt.Sprintf(`{"title":"bad-%d-%s","column_id":%q,"priority":%d,"agent":"test"}`, bad, tag, col, bad),
			http.StatusBadRequest)
	}
}

// TestItemPriorityPatchSemantics pins REST PATCH: priority absent keeps its
// value, a valid value replaces it, and out-of-range is a 400.
func TestItemPriorityPatchSemantics(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	cout := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"patch-%s","column_id":%q,"priority":1,"agent":"test"}`, tag, col), http.StatusCreated)
	iid := cout["item"].(map[string]any)["id"].(string)

	// Absent priority keeps 1.
	keep := st.do(t, "PATCH", "/api/items/"+iid, fmt.Sprintf(`{"title":"patch2-%s","agent":"test"}`, tag), http.StatusOK)
	if got := keep["item"].(map[string]any)["priority"].(float64); got != 1 {
		t.Errorf("patch without priority: priority = %v, want 1 (absent must keep)", got)
	}

	// Present valid value replaces.
	set := st.do(t, "PATCH", "/api/items/"+iid, `{"priority":3,"agent":"test"}`, http.StatusOK)
	if got := set["item"].(map[string]any)["priority"].(float64); got != 3 {
		t.Errorf("patch priority 3: got %v", got)
	}

	// Out of range → 400, and the stored value is unchanged.
	for _, bad := range []int{-1, 4} {
		st.do(t, "PATCH", "/api/items/"+iid, fmt.Sprintf(`{"priority":%d,"agent":"test"}`, bad), http.StatusBadRequest)
	}
	after := st.do(t, "GET", "/api/items/"+iid, "", http.StatusOK)
	if got := after["item"].(map[string]any)["priority"].(float64); got != 3 {
		t.Errorf("priority after rejected patches = %v, want 3", got)
	}
}

// TestItemPriorityInBoardSnapshot pins that priority is part of the composite
// board payload (the UI snapshot).
func TestItemPriorityInBoardSnapshot(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)

	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"snap-%s","column_id":%q,"priority":0,"agent":"test"}`, tag, sd.columns[0]), http.StatusCreated)

	snap := st.do(t, "GET", "/api/boards/"+sd.boardID, "", http.StatusOK)
	items := snap["items"].([]any)
	found := false
	for _, raw := range items {
		it := raw.(map[string]any)
		if _, ok := it["priority"]; !ok {
			t.Fatalf("snapshot item missing priority: %v", it)
		}
		if it["priority"].(float64) == 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("snapshot items = %v, want one with priority 0", items)
	}
}

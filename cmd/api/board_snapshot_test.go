package main

import (
	"fmt"
	"net/http"
	"testing"
)

// columnByID returns the snapshot column map for a column id.
func columnByID(t *testing.T, snap map[string]any, columnID string) map[string]any {
	t.Helper()
	raw, ok := snap["columns"].([]any)
	if !ok {
		t.Fatalf("snapshot columns = %T, want array", snap["columns"])
	}
	for _, r := range raw {
		c := r.(map[string]any)
		if c["id"] == columnID {
			return c
		}
	}
	t.Fatalf("column %s not found in snapshot", columnID)
	return nil
}

// snapshotItemCount reads a column's item_count from a snapshot response.
func snapshotItemCount(t *testing.T, snap map[string]any, columnID string) int {
	t.Helper()
	c := columnByID(t, snap, columnID)
	v, ok := c["item_count"].(float64)
	if !ok {
		t.Fatalf("column %s item_count = %v (%T), want number", columnID, c["item_count"], c["item_count"])
	}
	return int(v)
}

// TestBoardSnapshotIncludeItems pins the lightweight snapshot contract:
// include_items defaults to true (full items, unchanged regression) and every
// column already carries item_count; include_items=false returns an empty items
// array with the same per-column counts. Counts and items both respect
// include_archived.
func TestBoardSnapshotIncludeItems(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	todo, doing, done := sd.columns[0], sd.columns[1], sd.columns[2]

	// Two items in todo (one archived), one in doing, none in done.
	a := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"light-a-%s","column_id":%q,"agent":"test"}`, tag, todo), http.StatusCreated)["item"].(map[string]any)
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"light-b-%s","column_id":%q,"agent":"test"}`, tag, todo), http.StatusCreated)
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"light-c-%s","column_id":%q,"agent":"test"}`, tag, doing), http.StatusCreated)
	st.do(t, "POST", "/api/items/"+a["id"].(string)+"/archive", `{"agent":"test"}`, http.StatusOK)

	// Default (include_items absent) is the historical full payload and now also
	// carries item_count on every column.
	def := st.do(t, "GET", "/api/boards/"+sd.boardID, "", http.StatusOK)
	if got := len(def["items"].([]any)); got != 2 {
		t.Errorf("default items = %d, want 2 (archived excluded)", got)
	}
	if got := snapshotItemCount(t, def, todo); got != 1 {
		t.Errorf("default todo item_count = %d, want 1", got)
	}
	if got := snapshotItemCount(t, def, doing); got != 1 {
		t.Errorf("default doing item_count = %d, want 1", got)
	}
	if got := snapshotItemCount(t, def, done); got != 0 {
		t.Errorf("default done item_count = %d, want 0", got)
	}

	// include_items=false: empty items, identical counts.
	lite := st.do(t, "GET", "/api/boards/"+sd.boardID+"?include_items=false", "", http.StatusOK)
	items, ok := lite["items"].([]any)
	if !ok {
		t.Fatalf("include_items=false items = %v (%T), want array", lite["items"], lite["items"])
	}
	if len(items) != 0 {
		t.Errorf("include_items=false items = %v, want empty", items)
	}
	if _, ok := lite["board"].(map[string]any); !ok {
		t.Errorf("include_items=false dropped board: %v", lite["board"])
	}
	if got := snapshotItemCount(t, lite, todo); got != 1 {
		t.Errorf("include_items=false todo item_count = %d, want 1", got)
	}
	if got := snapshotItemCount(t, lite, doing); got != 1 {
		t.Errorf("include_items=false doing item_count = %d, want 1", got)
	}
	if got := snapshotItemCount(t, lite, done); got != 0 {
		t.Errorf("include_items=false done item_count = %d, want 0", got)
	}

	// include_archived=true counts the archived todo item in both modes and, when
	// items are included, returns it.
	liteInc := st.do(t, "GET", "/api/boards/"+sd.boardID+"?include_items=false&include_archived=true", "", http.StatusOK)
	if len(liteInc["items"].([]any)) != 0 {
		t.Errorf("include_items=false&include_archived items = %v, want empty", liteInc["items"])
	}
	if got := snapshotItemCount(t, liteInc, todo); got != 2 {
		t.Errorf("include_items=false&include_archived todo item_count = %d, want 2", got)
	}

	fullInc := st.do(t, "GET", "/api/boards/"+sd.boardID+"?include_archived=true", "", http.StatusOK)
	if got := len(fullInc["items"].([]any)); got != 3 {
		t.Errorf("include_archived items = %d, want 3", got)
	}
	if got := snapshotItemCount(t, fullInc, todo); got != 2 {
		t.Errorf("include_archived todo item_count = %d, want 2", got)
	}

	// Counts must agree with an independent full paginated walk of each column.
	for _, col := range []string{todo, doing, done} {
		page := st.do(t, "GET", "/api/boards/"+sd.boardID+"/items?column_id="+col+"&include_archived=false", "", http.StatusOK)
		if got := len(page["items"].([]any)); got != snapshotItemCount(t, lite, col) {
			t.Errorf("column %s: page walk = %d, snapshot item_count = %d", col, got, snapshotItemCount(t, lite, col))
		}
	}
}

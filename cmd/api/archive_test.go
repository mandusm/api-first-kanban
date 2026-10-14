package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"api-first-kanban/internal/events"
	"api-first-kanban/internal/testutil"
)

// columnItems filters any board-snapshot response down to one column's items.
func columnItems(t *testing.T, snap map[string]any, columnID string) []map[string]any {
	t.Helper()
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

func itemIDs(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it["id"].(string))
	}
	return out
}

func containsID(items []map[string]any, id string) bool {
	for _, it := range items {
		if it["id"] == id {
			return true
		}
	}
	return false
}

// TestArchiveLifecycleREST pins the archive/unarchive contract over HTTP:
// archived_at on the item payload, default exclusion from the board snapshot
// AND the paginated items endpoint, include_archived=true on both, the
// double-archive / unarchive-of-active 409s, and malformed-id 400.
func TestArchiveLifecycleREST(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	// Two items in one column so pagination proves the filter, not just size 0.
	a := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"arch-a-%s","column_id":%q,"agent":"test"}`, tag, col), http.StatusCreated)["item"].(map[string]any)
	aid := a["id"].(string)
	b := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"arch-b-%s","column_id":%q,"agent":"test"}`, tag, col), http.StatusCreated)["item"].(map[string]any)
	bid := b["id"].(string)

	// Active item carries an explicit null archived_at.
	if v, present := a["archived_at"]; !present || v != nil {
		t.Fatalf("created archived_at = %v (present=%v), want null", v, present)
	}

	// Archive A -> 200 with an RFC3339 archived_at string.
	arch := st.do(t, "POST", "/api/items/"+aid+"/archive", `{"agent":"test"}`, http.StatusOK)["item"].(map[string]any)
	av, ok := arch["archived_at"].(string)
	if !ok || av == "" {
		t.Fatalf("archived_at = %v (%T), want a non-empty RFC3339 string", arch["archived_at"], arch["archived_at"])
	}
	if _, err := time.Parse(time.RFC3339, av); err != nil {
		t.Errorf("archived_at %q is not RFC3339: %v", av, err)
	}
	if arch["closed"] != false {
		t.Errorf("archive changed closed: %v", arch["closed"])
	}

	// Default snapshot excludes the archived item and keeps the active one.
	snap := st.do(t, "GET", "/api/boards/"+sd.boardID, "", http.StatusOK)
	if got := itemIDs(columnItems(t, snap, col)); len(got) != 1 || got[0] != bid {
		t.Errorf("default snapshot column items = %v, want [%s]", got, bid)
	}

	// include_archived=true returns both, A still carrying archived_at.
	inc := st.do(t, "GET", "/api/boards/"+sd.boardID+"?include_archived=true", "", http.StatusOK)
	incItems := columnItems(t, inc, col)
	if len(incItems) != 2 || !containsID(incItems, aid) || !containsID(incItems, bid) {
		t.Fatalf("include_archived snapshot items = %v, want both %s and %s", itemIDs(incItems), aid, bid)
	}
	for _, it := range incItems {
		if it["id"] == aid {
			if s, _ := it["archived_at"].(string); s != av {
				t.Errorf("include_archived archived_at = %v, want %s", it["archived_at"], av)
			}
		}
	}

	// Pagination default excludes; include_archived=true restores it.
	base := "/api/boards/" + sd.boardID + "/items?column_id=" + col
	if got := pageItemIDs(t, st, base); len(got) != 1 || got[0] != bid {
		t.Errorf("default page items = %v, want [%s]", got, bid)
	}
	if got := pageItemIDs(t, st, base+"&include_archived=true"); len(got) != 2 {
		t.Errorf("include_archived page items = %v, want 2", got)
	}

	// Double archive -> 409; unarchive -> 200 cleared; unarchive again -> 409.
	st.do(t, "POST", "/api/items/"+aid+"/archive", `{"agent":"test"}`, http.StatusConflict)
	un := st.do(t, "POST", "/api/items/"+aid+"/unarchive", `{"agent":"test"}`, http.StatusOK)["item"].(map[string]any)
	if un["archived_at"] != nil {
		t.Errorf("unarchived archived_at = %v, want null", un["archived_at"])
	}
	if got := itemIDs(columnItems(t, st.do(t, "GET", "/api/boards/"+sd.boardID, "", http.StatusOK), col)); len(got) != 2 {
		t.Errorf("after unarchive snapshot items = %v, want 2", got)
	}
	st.do(t, "POST", "/api/items/"+aid+"/unarchive", `{"agent":"test"}`, http.StatusConflict)

	// Malformed id -> 400 for both routes.
	st.do(t, "POST", "/api/items/zzz/archive", `{}`, http.StatusBadRequest)
	st.do(t, "POST", "/api/items/zzz/unarchive", `{}`, http.StatusBadRequest)
}

// pageItemIDs fetches a paginated items URL and returns the item ids in order.
func pageItemIDs(t *testing.T, st boardStore, url string) []string {
	t.Helper()
	out := st.do(t, "GET", url, "", http.StatusOK)
	raw, ok := out["items"].([]any)
	if !ok {
		t.Fatalf("items = %v (%T), want array", out["items"], out["items"])
	}
	ids := make([]string, 0, len(raw))
	for _, r := range raw {
		ids = append(ids, r.(map[string]any)["id"].(string))
	}
	return ids
}

// TestArchiveIndependentOfClosed pins that archive and close are orthogonal: a
// closed item archives, an archived item keeps its closed badge, and neither
// transition changes the other flag.
func TestArchiveIndependentOfClosed(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)

	iid := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"closed-arch-%s","column_id":%q,"agent":"test"}`, tag, sd.columns[0]), http.StatusCreated)["item"].(map[string]any)["id"].(string)

	// Close, then archive: closed stays true.
	closed := st.do(t, "PATCH", "/api/items/"+iid, `{"closed":true,"agent":"test"}`, http.StatusOK)["item"].(map[string]any)
	if closed["closed"] != true {
		t.Fatalf("closed = %v, want true", closed["closed"])
	}
	arch := st.do(t, "POST", "/api/items/"+iid+"/archive", `{"agent":"test"}`, http.StatusOK)["item"].(map[string]any)
	if arch["closed"] != true || arch["archived_at"] == nil {
		t.Errorf("archived closed=%v archived_at=%v, want true/non-null", arch["closed"], arch["archived_at"])
	}
	// Unarchive keeps it closed.
	un := st.do(t, "POST", "/api/items/"+iid+"/unarchive", `{"agent":"test"}`, http.StatusOK)["item"].(map[string]any)
	if un["closed"] != true || un["archived_at"] != nil {
		t.Errorf("unarchived closed=%v archived_at=%v, want true/null", un["closed"], un["archived_at"])
	}
}

// TestArchiveNoCascade pins the hierarchy/relation non-cascade rule: archiving
// a parent leaves its children untouched and it may still be used as a parent,
// and archiving one endpoint of a relation leaves the relation readable.
func TestArchiveNoCascade(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)

	parent := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"ms-%s","kind":"milestone","column_id":%q,"agent":"test"}`, tag, sd.columns[0]), http.StatusCreated)["item"].(map[string]any)["id"].(string)
	child := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"task-%s","parent_item_id":%q,"column_id":%q,"agent":"test"}`, tag, parent, sd.columns[0]), http.StatusCreated)["item"].(map[string]any)["id"].(string)

	// A second item blocked by the parent.
	blocker := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"blocker-%s","column_id":%q,"agent":"test"}`, tag, sd.columns[1]), http.StatusCreated)["item"].(map[string]any)["id"].(string)
	st.do(t, "POST", "/api/items/"+blocker+"/relations",
		fmt.Sprintf(`{"to_item_id":%q,"relation":"blocks","agent":"test"}`, parent), http.StatusCreated)

	// Archive the parent: children/relations untouched.
	st.do(t, "POST", "/api/items/"+parent+"/archive", `{"agent":"test"}`, http.StatusOK)

	childView := st.do(t, "GET", "/api/items/"+child, "", http.StatusOK)["item"].(map[string]any)
	if childView["parent_item_id"] != parent {
		t.Errorf("child parent_item_id = %v, want %s (archive must not cascade)", childView["parent_item_id"], parent)
	}
	if childView["archived_at"] != nil {
		t.Errorf("child archived_at = %v, want null (archive must not cascade)", childView["archived_at"])
	}

	// An archived item may still be referenced as a parent.
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"task2-%s","parent_item_id":%q,"column_id":%q,"agent":"test"}`, tag, parent, sd.columns[0]), http.StatusCreated)

	// The relation is still readable and the archived parent still GETs by id.
	rels := st.do(t, "GET", "/api/items/"+blocker, "", http.StatusOK)["relations"].(map[string]any)
	if n := len(rels["outgoing"].([]any)); n != 1 {
		t.Errorf("blocker outgoing relations = %d, want 1", n)
	}
	if got := st.do(t, "GET", "/api/items/"+parent, "", http.StatusOK)["item"].(map[string]any); got["archived_at"] == nil {
		t.Errorf("archived parent GET archived_at = nil, want non-null")
	}
}

// TestArchiveSSEEvents drives archive/unarchive through a live SSE stream and
// asserts the item.archived / item.unarchived events carry the item entity with
// archived_at.
func TestArchiveSSEEvents(t *testing.T) {
	pool := testutil.OpenPool(t)
	hub := events.NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = hub.Listen(ctx, pool) }()

	mux := testutil.NewMux(pool, hub)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)

	waitForHubListen(t, pool, hub)
	stream := openSSE(t, srv.URL+"/api/events?board_id="+sd.boardID)

	iid := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"sse-arch-%s","column_id":%q,"agent":"test"}`, tag, sd.columns[0]), http.StatusCreated)["item"].(map[string]any)["id"].(string)
	st.do(t, "POST", "/api/items/"+iid+"/archive", `{"agent":"sse-arch"}`, http.StatusOK)
	st.do(t, "POST", "/api/items/"+iid+"/unarchive", `{"agent":"sse-arch"}`, http.StatusOK)

	evs := stream.collect(t, 1200*time.Millisecond)
	archived := mustEvent(t, evs, "item.archived")
	if archived["board_id"] != sd.boardID || archived["agent"] != "sse-arch" {
		t.Errorf("item.archived scope/agent = %v/%v, want %s/sse-arch", archived["board_id"], archived["agent"], sd.boardID)
	}
	item, ok := archived["item"].(map[string]any)
	if !ok {
		t.Fatalf("item.archived has no item entity: %v", archived)
	}
	if item["id"] != iid || item["archived_at"] == nil {
		t.Errorf("item.archived item = %v, want id %s with archived_at", item, iid)
	}
	unarchived := mustEvent(t, evs, "item.unarchived")
	if item, ok := unarchived["item"].(map[string]any); !ok || item["archived_at"] != nil {
		t.Errorf("item.unarchived item = %v, want archived_at null", unarchived["item"])
	}
}

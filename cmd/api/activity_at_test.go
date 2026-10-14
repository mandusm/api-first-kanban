package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// activityAtOf reads and parses an item's activity_at field, failing when the
// field is missing or unparseable.
func activityAtOf(t *testing.T, item map[string]any, where string) time.Time {
	t.Helper()
	raw, ok := item["activity_at"].(string)
	if !ok {
		t.Fatalf("%s: item has no activity_at string: %v", where, item)
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("%s: activity_at %q unparseable: %v", where, raw, err)
	}
	return at
}

func updatedAtOf(t *testing.T, item map[string]any, where string) time.Time {
	t.Helper()
	raw, ok := item["updated_at"].(string)
	if !ok {
		t.Fatalf("%s: item has no updated_at string: %v", where, item)
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("%s: updated_at %q unparseable: %v", where, raw, err)
	}
	return at
}

// TestActivityAtNoComments is PB-122 (a): a comment-less item's activity_at
// equals its updated_at on every item payload — board snapshot, column page,
// get_item, and a mutation (PATCH) response.
func TestActivityAtNoComments(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	created := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"act-plain-%s","column_id":%q,"agent":"test"}`, tag, col), http.StatusCreated)
	item := created["item"].(map[string]any)
	id := item["id"].(string)
	want := updatedAtOf(t, item, "create response")

	if got := activityAtOf(t, item, "create response"); !got.Equal(want) {
		t.Errorf("create response activity_at = %v, want updated_at %v", got, want)
	}

	// Board snapshot.
	snap := st.do(t, "GET", "/api/boards/"+sd.boardID, "", http.StatusOK)
	var found bool
	for _, r := range snap["items"].([]any) {
		it := r.(map[string]any)
		if it["id"] == id {
			found = true
			if got := activityAtOf(t, it, "board snapshot"); !got.Equal(want) {
				t.Errorf("board snapshot activity_at = %v, want %v", got, want)
			}
		}
	}
	if !found {
		t.Fatalf("item %s missing from board snapshot", id)
	}

	// Column page.
	page := st.do(t, "GET", "/api/boards/"+sd.boardID+"/items?column_id="+col, "", http.StatusOK)
	var pageFound bool
	for _, r := range page["items"].([]any) {
		it := r.(map[string]any)
		if it["id"] == id {
			pageFound = true
			if got := activityAtOf(t, it, "column page"); !got.Equal(want) {
				t.Errorf("column page activity_at = %v, want %v", got, want)
			}
		}
	}
	if !pageFound {
		t.Fatalf("item %s missing from column page", id)
	}

	// get_item detail.
	detail := st.do(t, "GET", "/api/items/"+id, "", http.StatusOK)
	if got := activityAtOf(t, detail["item"].(map[string]any), "get_item"); !got.Equal(want) {
		t.Errorf("get_item activity_at = %v, want %v", got, want)
	}

	// Mutation response (PATCH).
	patched := st.do(t, "PATCH", "/api/items/"+id, `{"description":"now with text"}`, http.StatusOK)
	pItem := patched["item"].(map[string]any)
	if got := activityAtOf(t, pItem, "patch response"); !got.Equal(updatedAtOf(t, pItem, "patch response")) {
		t.Errorf("patch response activity_at = %v, want its updated_at", got)
	}
}

// TestActivityAtCommentNewer is PB-122 (b)+(d): a comment added after the
// item's last update bumps activity_at to the comment time on the board
// snapshot, the column page, get_item, and a fresh mutation response.
func TestActivityAtCommentNewer(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	created := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"act-newer-%s","column_id":%q,"agent":"test"}`, tag, col), http.StatusCreated)
	item := created["item"].(map[string]any)
	id := item["id"].(string)
	base := updatedAtOf(t, item, "create")

	// Give the comment a distinct (later) timestamp floor: sleep a hair, then
	// add the comment through the API.
	time.Sleep(5 * time.Millisecond)
	comment := st.do(t, "POST", "/api/items/"+id+"/comments",
		`{"body":"bump me","agent":"test"}`, http.StatusCreated)["comment"].(map[string]any)
	commentAt, err := time.Parse(time.RFC3339Nano, comment["created_at"].(string))
	if err != nil {
		t.Fatalf("comment created_at: %v", err)
	}
	if !commentAt.After(base) {
		t.Fatalf("test setup: comment %v not after item updated_at %v", commentAt, base)
	}

	assertAt := func(item map[string]any, where string) {
		t.Helper()
		got := activityAtOf(t, item, where)
		if !got.Equal(commentAt) {
			t.Errorf("%s: activity_at = %v, want comment time %v", where, got, commentAt)
		}
	}

	// Board snapshot.
	snap := st.do(t, "GET", "/api/boards/"+sd.boardID, "", http.StatusOK)
	found := false
	for _, r := range snap["items"].([]any) {
		it := r.(map[string]any)
		if it["id"] == id {
			found = true
			assertAt(it, "board snapshot")
		}
	}
	if !found {
		t.Fatalf("item %s missing from board snapshot", id)
	}

	// Column page.
	page := st.do(t, "GET", "/api/boards/"+sd.boardID+"/items?column_id="+col, "", http.StatusOK)
	found = false
	for _, r := range page["items"].([]any) {
		it := r.(map[string]any)
		if it["id"] == id {
			found = true
			assertAt(it, "column page")
		}
	}
	if !found {
		t.Fatalf("item %s missing from column page", id)
	}

	// get_item.
	assertAt(st.do(t, "GET", "/api/items/"+id, "", http.StatusOK)["item"].(map[string]any), "get_item")

	// Mutation response: activity_at must survive (and not regress to the new
	// updated_at when the comment is still the later of the two — here they
	// are distinct times, comment later).
	patched := st.do(t, "PATCH", "/api/items/"+id, `{"description":"post-comment edit"}`, http.StatusOK)
	pItem := patched["item"].(map[string]any)
	if got := activityAtOf(t, pItem, "patch response"); got.Before(commentAt) {
		t.Errorf("patch response activity_at = %v, want >= comment time %v", got, commentAt)
	}
}

// TestActivityAtCommentOlder is PB-122 (c): a comment created BEFORE the
// item's updated_at does not drag activity_at back — it stays updated_at.
// The comment is backdated directly in the store (the API always stamps now).
func TestActivityAtCommentOlder(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	created := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"act-older-%s","column_id":%q,"agent":"test"}`, tag, col), http.StatusCreated)
	item := created["item"].(map[string]any)
	id := item["id"].(string)

	// Bump updated_at with a real mutation so the item's updated_at is well
	// after the backdated comment.
	patched := st.do(t, "PATCH", "/api/items/"+id, `{"description":"bump"}`, http.StatusOK)
	want := updatedAtOf(t, patched["item"].(map[string]any), "patch")

	_, err := pool.Exec(context.Background(),
		`INSERT INTO comments (item_id, author_type, author_name, body, created_at)
		 VALUES ($1, 'agent', 'test', 'stale note', $2)`, id, want.Add(-time.Hour))
	if err != nil {
		t.Fatalf("backdated comment insert: %v", err)
	}

	detail := st.do(t, "GET", "/api/items/"+id, "", http.StatusOK)
	if got := activityAtOf(t, detail["item"].(map[string]any), "get_item"); !got.Equal(want) {
		t.Errorf("get_item activity_at = %v, want updated_at %v (older comment must not win)", got, want)
	}
}

// TestActivityAtGetItemChildren is PB-122 (e): get_item children rows carry
// their own activity_at — a commented child reads the comment time while the
// comment-less parent reads its updated_at.
func TestActivityAtGetItemChildren(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	parent := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"act-parent-%s","column_id":%q,"kind":"epic","agent":"test"}`, tag, col), http.StatusCreated)["item"].(map[string]any)
	child := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"act-child-%s","column_id":%q,"parent_item_id":%q,"agent":"test"}`, tag, col, parent["id"].(string)), http.StatusCreated)["item"].(map[string]any)
	childID := child["id"].(string)

	time.Sleep(5 * time.Millisecond)
	comment := st.do(t, "POST", "/api/items/"+childID+"/comments",
		`{"body":"child note","agent":"test"}`, http.StatusCreated)["comment"].(map[string]any)
	commentAt, err := time.Parse(time.RFC3339Nano, comment["created_at"].(string))
	if err != nil {
		t.Fatalf("comment created_at: %v", err)
	}

	detail := st.do(t, "GET", "/api/items/"+parent["id"].(string), "", http.StatusOK)
	parentAt := activityAtOf(t, detail["item"].(map[string]any), "get_item parent")
	if !parentAt.Equal(updatedAtOf(t, detail["item"].(map[string]any), "get_item parent")) {
		t.Errorf("parent activity_at = %v, want its updated_at (no comments)", parentAt)
	}
	var childRow map[string]any
	for _, r := range detail["children"].([]any) {
		it := r.(map[string]any)
		if it["id"] == childID {
			childRow = it
		}
	}
	if childRow == nil {
		t.Fatalf("child %s missing from get_item children", childID)
	}
	if got := activityAtOf(t, childRow, "get_item child"); !got.Equal(commentAt) {
		t.Errorf("child activity_at = %v, want comment time %v", got, commentAt)
	}
	// Sanity: raw JSON decode keeps the field present on children rows.
	raw, _ := json.Marshal(childRow)
	if _, ok := childRow["activity_at"]; !ok {
		t.Errorf("child row JSON missing activity_at: %s", raw)
	}
}

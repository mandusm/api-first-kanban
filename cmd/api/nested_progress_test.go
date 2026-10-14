package main

import (
	"fmt"
	"net/http"
	"testing"
)

// TestNestedSubtreeProgress is the PB-118 fixture test: a milestone with two
// epics — epic A fully closed WITH closed tasks of its own, epic B partial —
// must read its WHOLE subtree in closed_descendants/total_descendants while
// closed_children/total_children stay the DIRECT counts (unchanged PB-84
// contract). An archived closed task is excluded from the subtree pair;
// a leaf reads 0/0. Asserted through the board snapshot and
// GET /api/items/{id} (item + children rows).
func TestNestedSubtreeProgress(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]

	mk := func(args string) map[string]any {
		return createItem(t, st, sd.boardID, args)
	}
	closeItem := func(id string) {
		st.do(t, "PATCH", "/api/items/"+id, `{"closed":true}`, http.StatusOK)
	}
	item := func(args string) string {
		return mk(fmt.Sprintf(args, tag, col))["id"].(string)
	}

	ms := item(`{"title":"nest-ms-%s","column_id":%q,"kind":"milestone","agent":"test"}`)
	epA := item(`{"title":"nest-epicA-%s","column_id":%q,"kind":"epic","parent_item_id":"` + ms + `","agent":"test"}`)
	epB := item(`{"title":"nest-epicB-%s","column_id":%q,"kind":"epic","parent_item_id":"` + ms + `","agent":"test"}`)

	// Epic A: 2 closed tasks (+1 closed task archived later) → fully closed.
	a1 := item(`{"title":"nest-a1-%s","column_id":%q,"parent_item_id":"` + epA + `","agent":"test"}`)
	a2 := item(`{"title":"nest-a2-%s","column_id":%q,"parent_item_id":"` + epA + `","agent":"test"}`)
	a3 := item(`{"title":"nest-a3-%s","column_id":%q,"parent_item_id":"` + epA + `","agent":"test"}`) // archived later
	closeItem(a1)
	closeItem(a2)
	closeItem(a3)
	closeItem(epA)

	// Epic B: 1 closed / 2 total tasks, epic itself open.
	b1 := item(`{"title":"nest-b1-%s","column_id":%q,"parent_item_id":"` + epB + `","agent":"test"}`)
	item(`{"title":"nest-b2-%s","column_id":%q,"parent_item_id":"` + epB + `","agent":"test"}`)
	closeItem(b1)

	// Archive the closed a3: excluded from subtree numerator AND denominator
	// everywhere (epic A reads 2/2, milestone subtree drops it).
	st.do(t, "POST", "/api/items/"+a3+"/archive", ``, http.StatusOK)

	// Expected subtree: milestone 4 closed / 6 total (a1,a2,epA,b1 closed;
	// b2+epB open; archived a3 excluded). Direct children stay 1/2 (epics,
	// epA closed). Epic A subtree: 2/2 (its own tasks; direct also 2/2 —
	// subtree equals direct when its children are leaves).

	assertDesc := func(v map[string]any, where string, closedDesc, totalDesc int) {
		t.Helper()
		cd, ok := v["closed_descendants"].(float64)
		td, tok := v["total_descendants"].(float64)
		if !ok || !tok {
			t.Fatalf("%s: closed_descendants/total_descendants not numeric ints: %v %v", where, v["closed_descendants"], v["total_descendants"])
		}
		if int(cd) != closedDesc || int(td) != totalDesc {
			t.Errorf("%s: descendants = %v/%v, want %d/%d", where, cd, td, closedDesc, totalDesc)
		}
	}
	assertDirect := func(v map[string]any, where string, closedKids, totalKids int) {
		t.Helper()
		cc := int(v["closed_children"].(float64))
		tc := int(v["total_children"].(float64))
		if cc != closedKids || tc != totalKids {
			t.Errorf("%s: closed_children/total_children = %d/%d, want %d/%d", where, cc, tc, closedKids, totalKids)
		}
	}

	// 1. Board snapshot carries the subtree fields.
	snap := st.do(t, "GET", "/api/boards/"+sd.boardID, ``, http.StatusOK)
	byID := map[string]map[string]any{}
	for _, r := range snap["items"].([]any) {
		it := r.(map[string]any)
		byID[it["id"].(string)] = it
	}
	if v := byID[ms]; v == nil {
		t.Fatalf("milestone not in board snapshot")
	} else {
		assertDesc(v, "snapshot milestone", 4, 6)
		assertDirect(v, "snapshot milestone", 1, 2)
	}
	assertDesc(byID[epA], "snapshot epic A", 2, 2)
	assertDirect(byID[epA], "snapshot epic A", 2, 2)
	assertDesc(byID[epB], "snapshot epic B", 1, 2)
	assertDesc(byID[a1], "snapshot leaf task", 0, 0)

	// 2. Item detail: item + children rows.
	detail := st.do(t, "GET", "/api/items/"+ms, ``, http.StatusOK)
	mv := itemOf(detail)
	assertDesc(mv, "detail milestone", 4, 6)
	assertDirect(mv, "detail milestone", 1, 2)
	sawEpA := false
	for _, r := range detail["children"].([]any) {
		it := r.(map[string]any)
		if it["id"] == epA {
			sawEpA = true
			assertDesc(it, "detail child epic A", 2, 2)
			assertDirect(it, "detail child epic A", 2, 2)
		}
	}
	if !sawEpA {
		t.Fatalf("epic A missing from milestone children rows")
	}

	// 3. Detail of a leaf task: 0/0 both pairs.
	ld := st.do(t, "GET", "/api/items/"+a1, ``, http.StatusOK)
	lv := itemOf(ld)
	assertDesc(lv, "leaf detail", 0, 0)
	if lv["closed_children"].(float64) != 0 || lv["total_children"].(float64) != 0 {
		t.Errorf("leaf detail direct counts = %v/%v, want 0/0", lv["closed_children"], lv["total_children"])
	}

	// 4. Mutation response (PATCH the milestone) carries the subtree pair.
	upd := itemOf(st.do(t, "PATCH", "/api/items/"+ms, fmt.Sprintf(`{"title":"nest-ms2-%s"}`, tag), http.StatusOK))
	assertDesc(upd, "update response", 4, 6)

	// 5. Column items page.
	page := st.do(t, "GET", fmt.Sprintf("/api/boards/%s/items?column_id=%s&limit=500", sd.boardID, col), ``, http.StatusOK)
	for _, r := range page["items"].([]any) {
		it := r.(map[string]any)
		if it["id"] == ms {
			assertDesc(it, "column page milestone", 4, 6)
		}
	}
}

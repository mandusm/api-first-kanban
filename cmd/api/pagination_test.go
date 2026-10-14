package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// seedColumnItems creates n items in col and returns their ids in snapshot
// order (position ascending), i.e. the order pagination must reproduce.
func seedColumnItems(t *testing.T, st boardStore, boardID, colID, tag string, n int) []string {
	t.Helper()
	for i := 0; i < n; i++ {
		st.do(t, "POST", "/api/boards/"+boardID+"/items",
			fmt.Sprintf(`{"title":"p-%s-%d","column_id":%q,"agent":"test"}`, tag, i, colID), http.StatusCreated)
	}
	snap := st.do(t, "GET", "/api/boards/"+boardID, "", http.StatusOK)
	var ids []string
	for _, it := range snap["items"].([]any) {
		item := it.(map[string]any)
		if item["column_id"].(string) == colID {
			ids = append(ids, item["id"].(string))
		}
	}
	if len(ids) != n {
		t.Fatalf("seeded %d items, snapshot has %d in column", n, len(ids))
	}
	return ids
}

type pageOut struct {
	ids  []string
	next *string
}

// getPage fetches one page and returns the item ids plus next_cursor (nil when
// the API returned JSON null).
func getPage(t *testing.T, st boardStore, boardID, colID string, limit int, cursor string) pageOut {
	t.Helper()
	url := fmt.Sprintf("/api/boards/%s/items?column_id=%s&limit=%d", boardID, colID, limit)
	if cursor != "" {
		url += "&cursor=" + cursor
	}
	out := st.do(t, "GET", url, "", http.StatusOK)
	raw, ok := out["items"].([]any)
	if !ok {
		t.Fatalf("items = %v (%T), want array", out["items"], out["items"])
	}
	po := pageOut{}
	for _, it := range raw {
		item, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("item = %v, want object", it)
		}
		if _, ok := item["labels"].([]any); !ok {
			t.Errorf("page item %v missing labels array", item["id"])
		}
		if _, ok := item["closed"].(bool); !ok {
			t.Errorf("page item %v closed = %T, want bool", item["id"], item["closed"])
		}
		po.ids = append(po.ids, item["id"].(string))
	}
	if nc, present := out["next_cursor"]; present && nc != nil {
		s, ok := nc.(string)
		if !ok {
			t.Fatalf("next_cursor = %v (%T), want string or null", nc, nc)
		}
		po.next = &s
	}
	return po
}

func assertPageIDs(t *testing.T, got pageOut, want []string) {
	t.Helper()
	if len(got.ids) != len(want) {
		t.Fatalf("page has %d items %v, want %d %v", len(got.ids), got.ids, len(want), want)
	}
	for i := range want {
		if got.ids[i] != want[i] {
			t.Fatalf("page item[%d] = %s, want %s (page=%v want=%v)", i, got.ids[i], want[i], got.ids, want)
		}
	}
}

// TestPaginationPagesThroughColumn crosses several limits and checks both the
// item ordering and the next_cursor handoff; the full snapshot stays complete.
func TestPaginationPagesThroughColumn(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]
	ids := seedColumnItems(t, st, sd.boardID, col, tag, 7)

	p1 := getPage(t, st, sd.boardID, col, 3, "")
	assertPageIDs(t, p1, ids[0:3])
	if p1.next == nil {
		t.Fatal("page1 next_cursor = null, want a cursor")
	}

	p2 := getPage(t, st, sd.boardID, col, 3, *p1.next)
	assertPageIDs(t, p2, ids[3:6])
	if p2.next == nil {
		t.Fatal("page2 next_cursor = null, want a cursor")
	}

	p3 := getPage(t, st, sd.boardID, col, 3, *p2.next)
	assertPageIDs(t, p3, ids[6:7])
	if p3.next != nil {
		t.Fatalf("page3 next_cursor = %q, want null (last page)", *p3.next)
	}

	// The snapshot remains the complete read path, unaffected by pagination.
	snap := st.do(t, "GET", "/api/boards/"+sd.boardID, "", http.StatusOK)
	count := 0
	for _, it := range snap["items"].([]any) {
		if it.(map[string]any)["column_id"].(string) == col {
			count++
		}
	}
	if count != 7 {
		t.Fatalf("snapshot has %d items in column, want 7", count)
	}
}

// TestPaginationCursorStableUnderInserts inserts a new top item between page
// fetches; the second page must be unaffected (the insert lands before the
// cursor, so it is not shifted into the already-consumed range).
func TestPaginationCursorStableUnderInserts(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]
	ids := seedColumnItems(t, st, sd.boardID, col, tag, 6)

	p1 := getPage(t, st, sd.boardID, col, 3, "")
	assertPageIDs(t, p1, ids[0:3])
	if p1.next == nil {
		t.Fatal("page1 next_cursor = null, want a cursor")
	}

	// Insert after fetching page 1; a create lands at the TOP of the column.
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"inserted-%s","column_id":%q,"agent":"test"}`, tag, col), http.StatusCreated)

	p2 := getPage(t, st, sd.boardID, col, 3, *p1.next)
	assertPageIDs(t, p2, ids[3:6])
	if p2.next != nil {
		t.Fatalf("page2 next_cursor = %q, want null (insert sits before the cursor and is not re-served)", *p2.next)
	}
}

// TestPaginationValidation pins the 400s: bad limit, missing/malformed/unknown
// column, foreign column, and malformed cursors.
func TestPaginationValidation(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	col := sd.columns[0]
	base := "/api/boards/" + sd.boardID + "/items"
	q := func(extra string) string { return base + "?column_id=" + col + extra }

	t.Run("limit", func(t *testing.T) {
		st.do(t, "GET", q("&limit=0"), "", http.StatusBadRequest)
		st.do(t, "GET", q("&limit=-1"), "", http.StatusBadRequest)
		st.do(t, "GET", q("&limit=501"), "", http.StatusBadRequest)
		st.do(t, "GET", q("&limit=abc"), "", http.StatusBadRequest)
		// bounds are inclusive and valid
		st.do(t, "GET", q("&limit=1"), "", http.StatusOK)
		st.do(t, "GET", q("&limit=500"), "", http.StatusOK)
	})

	t.Run("column", func(t *testing.T) {
		st.do(t, "GET", base, "", http.StatusBadRequest)                                                   // missing
		st.do(t, "GET", base+"?column_id=zzz", "", http.StatusBadRequest)                                  // malformed
		st.do(t, "GET", base+"?column_id=00000000-0000-0000-0000-000000000000", "", http.StatusBadRequest) // unknown
		other := seedBoard(t, st, tag+"x")
		st.do(t, "GET", base+"?column_id="+other.columns[0], "", http.StatusBadRequest) // foreign board
	})

	t.Run("cursor", func(t *testing.T) {
		st.do(t, "GET", q("&cursor=garbage"), "", http.StatusBadRequest) // bad base64/json
		missing, _ := json.Marshal(map[string]string{"p": "1"})
		st.do(t, "GET", q("&cursor="+base64.RawURLEncoding.EncodeToString(missing)), "", http.StatusBadRequest)
		badID, _ := json.Marshal(map[string]string{"cp": "0", "p": "1", "id": "not-a-uuid"})
		st.do(t, "GET", q("&cursor="+base64.RawURLEncoding.EncodeToString(badID)), "", http.StatusBadRequest)
	})

	t.Run("board", func(t *testing.T) {
		st.do(t, "GET", "/api/boards/zzz/items?column_id="+col, "", http.StatusBadRequest)
	})
}

// TestPaginationEmptyColumn returns an empty (non-null) items array and a null
// next_cursor.
func TestPaginationEmptyColumn(t *testing.T) {
	st := boardStore{mux: testMux(t)}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	empty := sd.columns[1]

	out := st.do(t, "GET", fmt.Sprintf("/api/boards/%s/items?column_id=%s", sd.boardID, empty), "", http.StatusOK)
	items, ok := out["items"].([]any)
	if !ok {
		t.Fatalf("items = %v (%T), want array", out["items"], out["items"])
	}
	if len(items) != 0 {
		t.Fatalf("empty column items = %v, want []", items)
	}
	if nc, present := out["next_cursor"]; !present || nc != nil {
		t.Fatalf("empty column next_cursor = %v (present=%v), want null", nc, present)
	}
}

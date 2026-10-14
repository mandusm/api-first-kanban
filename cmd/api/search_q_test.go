package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"
)

// qDo performs a GET with the given raw query string (unencoded) and returns
// the raw response body plus status, without the automatic status assertion of
// boardStore.do (needed for byte-identity comparisons and 400 assertions).
func qDo(t *testing.T, mux *http.ServeMux, path string, q url.Values, wantStatus int) []byte {
	t.Helper()
	full := path
	if enc := q.Encode(); enc != "" {
		full += "?" + enc
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, full, nil))
	if rec.Code != wantStatus {
		t.Fatalf("GET %s: status=%d want %d body=%s", full, rec.Code, wantStatus, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// TestBoardSnapshotQueryFilters pins the PB-58 searched-view contract: a valid
// q returns the full composite with FILTERED items and counts derived from the
// filtered set; q composes with include_archived; zero-match queries return
// 200 with empty results; invalid q → 400 validation with the exact §6 message;
// empty/absent q are byte-identical to today's behavior.
func TestBoardSnapshotQueryFilters(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	todo, doing := sd.columns[0], sd.columns[1]

	lbl := st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/labels",
		`{"name":"bug-`+tag+`","agent":"test"}`, http.StatusCreated)["label"].(map[string]any)["id"].(string)

	// t-bug: labeled, P1, old (backdated created_at); t-plain: unlabeled, P2, fresh.
	bug := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"old-bug-%s","column_id":%q,"priority":1,"label_ids":[%q],"agent":"test"}`, tag, todo, lbl),
		http.StatusCreated)["item"].(map[string]any)
	_ = st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"plain-%s","column_id":%q,"agent":"test"}`, tag, todo), http.StatusCreated)
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"doing-%s","column_id":%q,"agent":"test"}`, tag, doing), http.StatusCreated)
	if _, err := pool.Exec(context.Background(),
		"UPDATE items SET created_at = $2 WHERE id = $1", bug["id"].(string), time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}

	boardPath := "/api/boards/" + sd.boardID

	// LABEL filter: only the labeled item survives; todo's item_count is 1.
	got := st.do(t, "GET", boardPath+"?q="+url.QueryEscape("LABEL IN ('bug-"+tag+"')"), "", http.StatusOK)
	var ids []string
	for _, it := range got["items"].([]any) {
		ids = append(ids, it.(map[string]any)["id"].(string))
	}
	if !slices.Equal(ids, []string{bug["id"].(string)}) {
		t.Errorf("LABEL query items = %v, want [%s]", ids, bug["id"])
	}
	if n := snapshotItemCount(t, got, todo); n != 1 {
		t.Errorf("filtered todo item_count = %d, want 1", n)
	}

	// CREATED filter over the backdated item.
	got = st.do(t, "GET", boardPath+"?q="+url.QueryEscape("CREATED < -1d"), "", http.StatusOK)
	ids = nil
	for _, it := range got["items"].([]any) {
		ids = append(ids, it.(map[string]any)["id"].(string))
	}
	if !slices.Equal(ids, []string{bug["id"].(string)}) {
		t.Errorf("CREATED query items = %v, want [%s]", ids, bug["id"])
	}

	// Zero matches: valid query, empty items, zero counts (200, not an error).
	got = st.do(t, "GET", boardPath+"?q="+url.QueryEscape("PRIORITY = 3"), "", http.StatusOK)
	if n := len(got["items"].([]any)); n != 0 {
		t.Errorf("zero-match items = %d, want 0", n)
	}
	if n := snapshotItemCount(t, got, todo); n != 0 {
		t.Errorf("zero-match todo item_count = %d, want 0", n)
	}

	// q + include_archived: archived matching item excluded by default,
	// included with include_archived=true.
	arch := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"arch-bug-%s","column_id":%q,"label_ids":[%q],"agent":"test"}`, tag, todo, lbl),
		http.StatusCreated)["item"].(map[string]any)
	st.do(t, "POST", "/api/items/"+arch["id"].(string)+"/archive", `{"agent":"test"}`, http.StatusOK)
	lblQ := url.QueryEscape("LABEL IN ('bug-" + tag + "')")
	got = st.do(t, "GET", boardPath+"?q="+lblQ, "", http.StatusOK)
	if n := len(got["items"].([]any)); n != 1 {
		t.Errorf("archived-excluded items = %d, want 1", n)
	}
	got = st.do(t, "GET", boardPath+"?q="+lblQ+"&include_archived=true", "", http.StatusOK)
	if n := len(got["items"].([]any)); n != 2 {
		t.Errorf("include_archived items = %d, want 2", n)
	}
	if n := snapshotItemCount(t, got, todo); n != 2 {
		t.Errorf("include_archived filtered todo item_count = %d, want 2", n)
	}

	// q and include_items: a queried view always returns the items array.
	got = st.do(t, "GET", boardPath+"?q="+lblQ+"&include_items=false", "", http.StatusOK)
	if n := len(got["items"].([]any)); n != 1 {
		t.Errorf("q+include_items=false items = %d, want 1 (full composite under q)", n)
	}

	// Invalid q → 400 validation with the exact §6 message (lowercase field).
	body := qDo(t, mux, boardPath, url.Values{"q": {"created < -1d"}}, http.StatusBadRequest)
	want := `{"error":{"code":"validation","message":"parse error at 1:1 (offset 0): unexpected input 'created'"}}` + "\n"
	if string(body) != want {
		t.Errorf("bad q body = %q, want %q", body, want)
	}
	// Unterminated string: position points at the opening quote.
	body = qDo(t, mux, boardPath, url.Values{"q": {"TITLE = 'x"}}, http.StatusBadRequest)
	want = `{"error":{"code":"validation","message":"parse error at 1:9 (offset 8): unterminated string literal"}}` + "\n"
	if string(body) != want {
		t.Errorf("unterminated q body = %q, want %q", body, want)
	}

	// Regression: absent q, empty q ("q=" and "q=%20%20") are byte-identical.
	base := qDo(t, mux, boardPath, nil, http.StatusOK)
	for _, q := range []string{"", "  "} {
		gotB := qDo(t, mux, boardPath, url.Values{"q": {q}}, http.StatusOK)
		if string(gotB) != string(base) {
			t.Errorf("q=%q response differs from absent-q response", q)
		}
	}
	// And the lazy path is untouched: include_items=false with no q keeps
	// empty items while counts stay unfiltered (3 non-archived items total
	// minus… assert relative to the plain snapshot counts).
	lite := qDo(t, mux, boardPath, url.Values{"include_items": {"false"}}, http.StatusOK)
	liteM := st.do(t, "GET", boardPath+"?include_items=false", "", http.StatusOK)
	if len(liteM["items"].([]any)) != 0 {
		t.Errorf("include_items=false items not empty without q")
	}
	if string(lite) == string(base) {
		t.Errorf("include_items=false should differ from the full snapshot")
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// boardStore drives the API via mux directly.
type boardStore struct {
	mux *http.ServeMux
}

func (b boardStore) do(t *testing.T, method, path, body string, wantStatus int) map[string]any {
	t.Helper()
	var rd *bytes.Reader
	if body == "" {
		rd = bytes.NewReader(nil)
	} else {
		rd = bytes.NewReader([]byte(body))
	}
	rec := httptest.NewRecorder()
	b.mux.ServeHTTP(rec, httptest.NewRequest(method, path, rd))
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: status=%d want %d body=%s", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func nowNanos() int64 { return time.Now().UnixNano() }

// TestBoardsColumnsCRUD covers board + column CRUD incl. wrapped responses,
// 409s, and unknown-parent 404s.
func TestBoardsColumnsCRUD(t *testing.T) {
	st := boardStore{mux: testMux(t)}

	// workspaces list responds (smoke check)
	st.do(t, "GET", "/api/workspaces", "", http.StatusOK)

	name := fmt.Sprintf("ws-crud-%d", nowNanos())
	out := st.do(t, "POST", "/api/workspaces", `{"name":"`+name+`","agent":"test"}`, http.StatusCreated)
	ws := out["workspace"].(map[string]any)
	wsid := ws["id"].(string)

	// boards
	bout := st.do(t, "POST", "/api/workspaces/"+wsid+"/boards", `{"name":"crud-board","agent":"test"}`, http.StatusCreated)
	board := bout["board"].(map[string]any)
	bid := board["id"].(string)
	cols := board["columns"].([]any)
	if len(cols) != 3 {
		t.Fatalf("default columns = %d, want 3", len(cols))
	}
	// duplicate board name → 409
	st.do(t, "POST", "/api/workspaces/"+wsid+"/boards", `{"name":"crud-board"}`, http.StatusConflict)
	// unknown workspace → 404
	st.do(t, "GET", "/api/workspaces/00000000-0000-0000-0000-000000000000/boards", "", http.StatusNotFound)
	// boards list → []
	lout := st.do(t, "GET", "/api/workspaces/"+wsid+"/boards", "", http.StatusOK)
	if lout["boards"] == nil {
		t.Fatal("boards key missing")
	}

	// snapshot
	snap := st.do(t, "GET", "/api/boards/"+bid, "", http.StatusOK)
	if _, ok := snap["items"]; !ok {
		t.Fatal("snapshot missing items")
	}

	// columns: create (default end), patch both fields, dup 409, delete 204/409
	cout := st.do(t, "POST", "/api/boards/"+bid+"/columns", `{"name":"extra","agent":"test"}`, http.StatusCreated)
	col := cout["column"].(map[string]any)
	cid := col["id"].(string)
	if col["position"].(float64) != 3 {
		t.Fatalf("column position = %v, want 3", col["position"])
	}
	st.do(t, "POST", "/api/boards/"+bid+"/columns", `{"name":"extra"}`, http.StatusConflict)
	pout := st.do(t, "PATCH", "/api/columns/"+cid, `{"name":"extra2","position":9}`, http.StatusOK)
	pcol := pout["column"].(map[string]any)
	if pcol["name"] != "extra2" || pcol["position"].(float64) != 9 {
		t.Fatalf("patched column = %v", pcol)
	}
	st.do(t, "PATCH", "/api/columns/"+cid, `{"name":""}`, http.StatusBadRequest)
	st.do(t, "DELETE", "/api/columns/"+cid, "", http.StatusNoContent)

	// delete column holding items → 409
	todo := cols[0].(map[string]any)["id"].(string)
	st.do(t, "POST", "/api/boards/"+bid+"/items", `{"title":"x","column_id":"`+todo+`","agent":"test"}`, http.StatusCreated)
	st.do(t, "DELETE", "/api/columns/"+todo, "", http.StatusConflict)

	// malformed uuid → 400
	st.do(t, "GET", "/api/boards/zzz", "", http.StatusBadRequest)
}

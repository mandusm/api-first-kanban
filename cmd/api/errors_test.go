package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const zeroUUID = "00000000-0000-0000-0000-000000000000"

// errDo issues a request directly through mux and returns the status plus the
// error envelope's code ("" when the body carries no envelope).
func errDo(t *testing.T, mux *http.ServeMux, method, path, body string) (int, string) {
	t.Helper()
	var rd io.Reader = strings.NewReader(body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, rd))
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.Error.Code
}

// TestMalformedIDValidation pins, for EVERY {id} route, that a malformed UUID
// is rejected before any lookup with the documented 400/validation envelope.
func TestMalformedIDValidation(t *testing.T) {
	mux := testMux(t)
	cases := []struct{ method, path, body string }{
		{"GET", "/api/workspaces/zzz/boards", ""},
		{"POST", "/api/workspaces/zzz/boards", `{"name":"x"}`},
		{"GET", "/api/boards/zzz", ""},
		{"POST", "/api/boards/zzz/columns", `{"name":"x"}`},
		{"PATCH", "/api/columns/zzz", `{"name":"x"}`},
		{"DELETE", "/api/columns/zzz", ""},
		{"POST", "/api/boards/zzz/items", `{"title":"x"}`},
		{"GET", "/api/items/zzz", ""},
		{"PATCH", "/api/items/zzz", `{"title":"x"}`},
		{"POST", "/api/items/zzz/move", `{"column_id":"` + zeroUUID + `"}`},
		{"POST", "/api/items/zzz/transfer", `{"to_agent":"x"}`},
		{"POST", "/api/items/zzz/archive", ""},
		{"POST", "/api/items/zzz/unarchive", ""},
		{"POST", "/api/items/zzz/relations", `{"to_item_id":"` + zeroUUID + `","relation":"blocks"}`},
		{"DELETE", "/api/relations/zzz", ""},
		{"GET", "/api/items/zzz/comments", ""},
		{"POST", "/api/items/zzz/comments", `{"body":"x"}`},
		{"GET", "/api/workspaces/zzz/labels", ""},
		{"POST", "/api/workspaces/zzz/labels", `{"name":"x"}`},
		{"PATCH", "/api/labels/zzz", `{"name":"x"}`},
		{"DELETE", "/api/labels/zzz", ""},
		{"GET", "/api/events?board_id=zzz", ""},
	}
	for _, tc := range cases {
		status, code := errDo(t, mux, tc.method, tc.path, tc.body)
		if status != http.StatusBadRequest || code != "validation" {
			t.Errorf("%s %s = %d/%q, want 400/validation", tc.method, tc.path, status, code)
		}
	}
}

// TestUnknownIDNotFound pins, for EVERY {id} route, the documented
// 404/not_found envelope for a well-formed but absent UUID.
func TestUnknownIDNotFound(t *testing.T) {
	mux := testMux(t)
	cases := []struct{ method, path, body string }{
		{"GET", "/api/workspaces/" + zeroUUID + "/boards", ""},
		{"POST", "/api/workspaces/" + zeroUUID + "/boards", `{"name":"x"}`},
		{"GET", "/api/boards/" + zeroUUID, ""},
		{"POST", "/api/boards/" + zeroUUID + "/columns", `{"name":"x"}`},
		{"PATCH", "/api/columns/" + zeroUUID, `{"name":"x"}`},
		{"DELETE", "/api/columns/" + zeroUUID, ""},
		{"POST", "/api/boards/" + zeroUUID + "/items", `{"title":"x"}`},
		{"GET", "/api/items/" + zeroUUID, ""},
		{"PATCH", "/api/items/" + zeroUUID, `{"title":"x"}`},
		{"POST", "/api/items/" + zeroUUID + "/move", `{"column_id":"` + zeroUUID + `"}`},
		{"POST", "/api/items/" + zeroUUID + "/transfer", `{"to_agent":"x"}`},
		{"POST", "/api/items/" + zeroUUID + "/archive", ""},
		{"POST", "/api/items/" + zeroUUID + "/unarchive", ""},
		{"POST", "/api/items/" + zeroUUID + "/relations", `{"to_item_id":"11111111-1111-1111-1111-111111111111","relation":"blocks"}`},
		{"DELETE", "/api/relations/" + zeroUUID, ""},
		{"GET", "/api/items/" + zeroUUID + "/comments", ""},
		{"POST", "/api/items/" + zeroUUID + "/comments", `{"body":"x"}`},
		{"GET", "/api/workspaces/" + zeroUUID + "/labels", ""},
		{"POST", "/api/workspaces/" + zeroUUID + "/labels", `{"name":"x"}`},
		{"PATCH", "/api/labels/" + zeroUUID, `{"name":"x"}`},
		{"DELETE", "/api/labels/" + zeroUUID, ""},
		{"GET", "/api/events?board_id=" + zeroUUID, ""},
	}
	for _, tc := range cases {
		status, code := errDo(t, mux, tc.method, tc.path, tc.body)
		if status != http.StatusNotFound || code != "not_found" {
			t.Errorf("%s %s = %d/%q, want 404/not_found", tc.method, tc.path, status, code)
		}
	}
}

// TestConflictEnvelopes pins the 409/conflict envelope for every duplicate-name
// resource and for deleting a column that still holds items.
func TestConflictEnvelopes(t *testing.T) {
	mux := testMux(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)

	// workspace name is globally unique.
	if status, code := errDo(t, mux, "POST", "/api/workspaces", `{"name":"ws-`+tag+`"}`); status != http.StatusConflict || code != "conflict" {
		t.Errorf("duplicate workspace = %d/%q, want 409/conflict", status, code)
	}
	// board name unique per workspace.
	if status, code := errDo(t, mux, "POST", "/api/workspaces/"+sd.wsID+"/boards", `{"name":"board-`+tag+`"}`); status != http.StatusConflict || code != "conflict" {
		t.Errorf("duplicate board = %d/%q, want 409/conflict", status, code)
	}
	// column name unique per board.
	if status, code := errDo(t, mux, "POST", "/api/boards/"+sd.boardID+"/columns", `{"name":"todo"}`); status != http.StatusConflict || code != "conflict" {
		t.Errorf("duplicate column = %d/%q, want 409/conflict", status, code)
	}
	// label name unique per workspace.
	st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/labels", `{"name":"dup-`+tag+`"}`, http.StatusCreated)
	if status, code := errDo(t, mux, "POST", "/api/workspaces/"+sd.wsID+"/labels", `{"name":"dup-`+tag+`"}`); status != http.StatusConflict || code != "conflict" {
		t.Errorf("duplicate label = %d/%q, want 409/conflict", status, code)
	}
	// deleting a column that holds items is a conflict, not a 500.
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"held-%s","column_id":%q}`, tag, sd.columns[0]), http.StatusCreated)
	if status, code := errDo(t, mux, "DELETE", "/api/columns/"+sd.columns[0], ""); status != http.StatusConflict || code != "conflict" {
		t.Errorf("delete non-empty column = %d/%q, want 409/conflict", status, code)
	}
}

// TestValidationEnvelopes pins 400/validation for the create/patch input sweep
// (empty required fields, malformed JSON, and the move/transfer guards).
func TestValidationEnvelopes(t *testing.T) {
	mux := testMux(t)
	st := boardStore{mux: mux}
	tag := fmt.Sprintf("%d", nowNanos())
	sd := seedBoard(t, st, tag)
	iid := st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"v-%s","column_id":%q}`, tag, sd.columns[0]), http.StatusCreated)["item"].(map[string]any)["id"].(string)

	cases := []struct {
		name, method, path, body string
	}{
		{"empty workspace name", "POST", "/api/workspaces", `{"name":"   "}`},
		{"empty board name", "POST", "/api/workspaces/" + sd.wsID + "/boards", `{"name":""}`},
		{"empty column name", "POST", "/api/boards/" + sd.boardID + "/columns", `{"name":" "}`},
		{"negative column position", "POST", "/api/boards/" + sd.boardID + "/columns", `{"name":"neg-` + tag + `","position":-1}`},
		{"empty label name", "POST", "/api/workspaces/" + sd.wsID + "/labels", `{"name":" "}`},
		{"empty item title", "POST", "/api/boards/" + sd.boardID + "/items", `{"title":"  "}`},
		{"empty patch title", "PATCH", "/api/items/" + iid, `{"title":" "}`},
		{"empty comment body", "POST", "/api/items/" + iid + "/comments", `{"body":" "}`},
		{"transfer without to_agent", "POST", "/api/items/" + iid + "/transfer", `{"note":"x"}`},
		{"move without column_id", "POST", "/api/items/" + iid + "/move", `{}`},
		{"malformed JSON", "POST", "/api/workspaces", `{"name":`},
	}
	for _, tc := range cases {
		status, code := errDo(t, mux, tc.method, tc.path, tc.body)
		if status != http.StatusBadRequest || code != "validation" {
			t.Errorf("%s: %s %s = %d/%q, want 400/validation", tc.name, tc.method, tc.path, status, code)
		}
	}
}

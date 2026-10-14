package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"api-first-kanban/internal/events"
	"api-first-kanban/internal/store"
	"api-first-kanban/internal/testutil"
)

func TestHealthz(t *testing.T) {
	pool := testutil.OpenPool(t)
	srv := httptest.NewServer(testutil.NewMux(pool, events.NewHub()))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var got struct {
		Status string `json:"status"`
		DB     bool   `json:"db"`
		Commit string `json:"commit"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "ok" || !got.DB {
		t.Errorf("got %+v, want {ok,true}", got)
	}
	// The commit field must always be present (a VCS revision, "<sha>-dirty",
	// or "unknown"); the exact value varies with how the test binary was built.
	if got.Commit == "" {
		t.Errorf("commit = empty, want a non-empty build identifier")
	}
}

// TestWorkspacesCRUD pins the workspace create/list/error envelopes.
func TestWorkspacesCRUD(t *testing.T) {
	mux := testutil.NewMux(testutil.OpenPool(t), events.NewHub())

	name := "ws-test-" + time.Now().Format("150405.000000000")
	// create
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/workspaces",
		strings.NewReader(`{"name":"`+name+`","description":"d"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create workspace: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// duplicate → 409
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest("POST", "/api/workspaces",
		strings.NewReader(`{"name":"`+name+`"}`)))
	if rec2.Code != http.StatusConflict {
		t.Errorf("dup workspace: status=%d want 409; body=%s", rec2.Code, rec2.Body.String())
	}
	// validation → 400
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, httptest.NewRequest("POST", "/api/workspaces", strings.NewReader(`{}`)))
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("empty name: status=%d want 400", rec3.Code)
	}
}

// TestHealthzNoDB — handler must still respond when the DB is unreachable.
func TestHealthzNoDB(t *testing.T) {
	pool, err := store.OpenPool(context.Background(), "postgres://kanban:kanban@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	srv := httptest.NewServer(testutil.NewMux(pool, events.NewHub()))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", res.StatusCode)
	}
	var got struct {
		Status string `json:"status"`
		DB     bool   `json:"db"`
	}
	_ = json.NewDecoder(res.Body).Decode(&got)
	if got.Status != "ok" || got.DB {
		t.Errorf("got %+v, want {ok,false}", got)
	}
}

// testEnv is the shared stack builder: the REST mux over the shared dev DB plus
// the raw pool used to assert on side-effects (the events table, direct row
// updates). It skips when the dev DB is down and fails under
// KANBAN_TEST_STRICT=1 (see internal/testutil).
func testEnv(t *testing.T) (*http.ServeMux, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.OpenPool(t)
	return testutil.NewMux(pool, events.NewHub()), pool
}

// testMux is testEnv for tests that need no raw pool.
func testMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux, _ := testEnv(t)
	return mux
}

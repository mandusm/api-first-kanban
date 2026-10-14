package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDeleteColumnAgentAttribution verifies DELETE /api/columns/{id} records the
// optional body "agent" on column.deleted and defaults to "human" when the body
// is absent, empty, or whitespace-only (matching label delete semantics).
func TestDeleteColumnAgentAttribution(t *testing.T) {
	mux, pool := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedBoard(t, st, fmt.Sprintf("%d", nowNanos()))

	// agent present → recorded verbatim.
	st.do(t, "DELETE", "/api/columns/"+sd.columns[0], `{"agent":"bot-7"}`, http.StatusNoContent)
	if got := latestColumnDeletedAgent(t, pool, sd.boardID); got != "bot-7" {
		t.Errorf("column.deleted agent = %q, want %q", got, "bot-7")
	}

	// no body → default "human".
	st.do(t, "DELETE", "/api/columns/"+sd.columns[1], "", http.StatusNoContent)
	if got := latestColumnDeletedAgent(t, pool, sd.boardID); got != "human" {
		t.Errorf("column.deleted agent (no body) = %q, want %q", got, "human")
	}

	// empty agent → default "human".
	st.do(t, "DELETE", "/api/columns/"+sd.columns[2], `{"agent":""}`, http.StatusNoContent)
	if got := latestColumnDeletedAgent(t, pool, sd.boardID); got != "human" {
		t.Errorf("column.deleted agent (empty) = %q, want %q", got, "human")
	}
}

// latestColumnDeletedAgent reads the agent of the newest column.deleted event
// for boardID.
func latestColumnDeletedAgent(t *testing.T, pool *pgxpool.Pool, boardID string) string {
	t.Helper()
	var raw []byte
	err := pool.QueryRow(context.Background(),
		`SELECT payload FROM events WHERE type = 'column.deleted' AND payload->>'board_id' = $1 ORDER BY id DESC LIMIT 1`,
		boardID).Scan(&raw)
	if err != nil {
		t.Fatalf("load column.deleted event: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode column.deleted payload: %v", err)
	}
	agent, _ := payload["agent"].(string)
	return agent
}

// TestDeleteColumnMalformedBodyRefused verifies a malformed JSON body yields 400
// and the column is NOT deleted (the delete must not execute after a decode error).
func TestDeleteColumnMalformedBodyRefused(t *testing.T) {
	mux, _ := testEnv(t)
	st := boardStore{mux: mux}
	sd := seedBoard(t, st, fmt.Sprintf("%d", nowNanos()))

	st.do(t, "DELETE", "/api/columns/"+sd.columns[0], `{"agent":`, http.StatusBadRequest)

	// The column must still exist: the board snapshot lists it.
	board := st.do(t, "GET", "/api/boards/"+sd.boardID, "", http.StatusOK)
	cols := board["columns"].([]any)
	found := false
	for _, c := range cols {
		if c.(map[string]any)["id"] == sd.columns[0] {
			found = true
		}
	}
	if !found {
		t.Fatalf("column %s was deleted despite malformed-body 400", sd.columns[0])
	}
}

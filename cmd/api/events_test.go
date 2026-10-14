package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"api-first-kanban/internal/events"
	"api-first-kanban/internal/testutil"
)

const (
	// hubAttachTimeout bounds how long we wait for the hub's LISTEN to attach;
	// hubAttachPoll is the probe interval.
	hubAttachTimeout = 5 * time.Second
	hubAttachPoll    = 10 * time.Millisecond
)

// TestEventsSSEFilters drives GET /api/events end to end over a live server and
// the LISTEN hub: a workspace-filtered subscriber sees the workspace's own
// events, and a board-filtered subscriber sees the board's events PLUS its
// workspace-scoped label.* events, while events from another board (same
// workspace) and another workspace never leak in.
func TestEventsSSEFilters(t *testing.T) {
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
	otherBoard := seedBoard(t, st, "other-"+tag)

	// Wait for the hub's LISTEN to attach before the first mutation under test
	// is published.
	waitForHubListen(t, pool, hub)

	boardStream := openSSE(t, srv.URL+"/api/events?board_id="+sd.boardID)
	wsStream := openSSE(t, srv.URL+"/api/events?workspace_id="+sd.wsID)

	// One mutation per scope:
	//   label.created  workspace-scoped (board_id null)
	//   item.created   sd's board
	//   item.created   another workspace's board (must not leak)
	//   board.created  a second board in sd's workspace (must not reach the
	//                  board-filtered subscriber)
	label := st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/labels",
		`{"name":"evt-lbl-`+tag+`","agent":"test"}`, http.StatusCreated)["label"].(map[string]any)["id"].(string)
	st.do(t, "POST", "/api/boards/"+sd.boardID+"/items",
		fmt.Sprintf(`{"title":"evt-%s","label_ids":[%q],"agent":"test"}`, tag, label), http.StatusCreated)
	st.do(t, "POST", "/api/boards/"+otherBoard.boardID+"/items",
		fmt.Sprintf(`{"title":"evt-other-%s","agent":"test"}`, tag), http.StatusCreated)
	st.do(t, "POST", "/api/workspaces/"+sd.wsID+"/boards",
		`{"name":"second-`+tag+`","agent":"test"}`, http.StatusCreated)

	boardEvents := boardStream.collect(t, 1200*time.Millisecond)
	wsEvents := wsStream.collect(t, 1200*time.Millisecond)

	// --- board-filtered subscriber ---
	ev := mustEvent(t, boardEvents, "label.created")
	if ev["workspace_id"] != sd.wsID || ev["board_id"] != nil {
		t.Errorf("label.created scope = %v/%v, want %s/null", ev["workspace_id"], ev["board_id"], sd.wsID)
	}
	if ev["agent"] != "test" {
		t.Errorf("label.created agent = %v, want test", ev["agent"])
	}
	if _, ok := ev["label"].(map[string]any); !ok {
		t.Errorf("label.created has no label entity: %v", ev)
	}
	if item := mustEvent(t, boardEvents, "item.created"); item["board_id"] != sd.boardID {
		t.Errorf("board stream item.created board_id = %v, want %s", item["board_id"], sd.boardID)
	}
	if hasEvent(boardEvents, "board.created") {
		t.Errorf("board-filtered stream received another board's board.created: %s", evTypes(boardEvents))
	}
	assertNoBoard(t, boardEvents, otherBoard.boardID, "board-filtered stream")

	// --- workspace-filtered subscriber ---
	mustEvent(t, wsEvents, "label.created")
	mustEvent(t, wsEvents, "item.created")
	mustEvent(t, wsEvents, "board.created")
	assertNoBoard(t, wsEvents, otherBoard.boardID, "workspace-filtered stream")
}

// waitForHubListen blocks until hub's LISTEN has attached to the kanban_events
// channel. The hub exposes no "ready" signal, so instead of a fixed sleep we
// poll: subscribe directly and resend a pg_notify reference until one is
// round-tripped back. A notification delivered before the hub attaches is
// dropped, so any delivery to our subscriber proves this hub's LISTEN is live
// (notifications are global to the channel, so a concurrent lane's event also
// proves it). Failing loudly here beats a lost first mutation surfacing later as
// a confusing missing-event assertion.
func waitForHubListen(t *testing.T, pool *pgxpool.Pool, hub *events.Hub) {
	t.Helper()
	ctx := context.Background()

	var eventID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM events ORDER BY id DESC LIMIT 1`).Scan(&eventID); err != nil {
		t.Fatalf("hub attach: no event row to probe with: %v", err)
	}
	ref, err := json.Marshal(map[string]int64{"event_id": eventID})
	if err != nil {
		t.Fatalf("hub attach: marshal probe ref: %v", err)
	}

	probe, cancel := hub.Subscribe(events.Filter{})
	defer cancel()

	deadline := time.Now().Add(hubAttachTimeout)
	ticker := time.NewTicker(hubAttachPoll)
	defer ticker.Stop()
	for {
		if _, err := pool.Exec(ctx, `SELECT pg_notify('kanban_events', $1)`, string(ref)); err != nil {
			t.Fatalf("hub attach: probe notify: %v", err)
		}
		select {
		case <-probe:
			return
		case <-ticker.C:
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub did not attach to LISTEN kanban_events within %s", hubAttachTimeout)
		}
	}
}

// sseStream reads server-sent events from a live GET /api/events connection.
type sseStream struct {
	events chan map[string]any
}

// openSSE connects, waits for the opening ": stream open" comment (which the
// handler writes only after subscribing), and starts decoding data events.
func openSSE(t *testing.T, url string) *sseStream {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("open SSE %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("SSE %s status = %d, want 200", url, resp.StatusCode)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	s := &sseStream{events: make(chan map[string]any, 128)}
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, ": stream open"):
				close(ready)
			case strings.HasPrefix(line, "data:"):
				var ev map[string]any
				if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev); err == nil {
					s.events <- ev
				}
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("SSE %s did not send the opening comment", url)
	}
	return s
}

// collect drains every event received within d (events are buffered from the
// moment the stream opened, so this also catches fast mutations).
func (s *sseStream) collect(t *testing.T, d time.Duration) []map[string]any {
	t.Helper()
	deadline := time.After(d)
	var out []map[string]any
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
}

func mustEvent(t *testing.T, evs []map[string]any, typ string) map[string]any {
	t.Helper()
	for _, e := range evs {
		if e["type"] == typ {
			return e
		}
	}
	t.Fatalf("no %s event in %v", typ, evTypes(evs))
	return nil
}

func hasEvent(evs []map[string]any, typ string) bool {
	for _, e := range evs {
		if e["type"] == typ {
			return true
		}
	}
	return false
}

func evTypes(evs []map[string]any) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, fmt.Sprint(e["type"]))
	}
	return out
}

func assertNoBoard(t *testing.T, evs []map[string]any, boardID, who string) {
	t.Helper()
	for _, e := range evs {
		if e["board_id"] == boardID {
			t.Errorf("%s leaked an event for board %s: %v", who, boardID, e)
		}
	}
}

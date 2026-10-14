// Package events implements the SSE fan-out hub over the kanban_events
// Postgres LISTEN channel. pg_notify carries only a reference
// ({"event_id":N}); the hub hydrates the full events.payload row by PK.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"api-first-kanban/internal/db"
)

// Message is one fanned-out SSE event; Payload is the SSE `data:` body.
type Message struct {
	Payload []byte
}

// hubEnvelope is a minimal decode used for filtering.
type hubEnvelope struct {
	WorkspaceID *string `json:"workspace_id"`
	BoardID     *string `json:"board_id"`
}

// notifyRef is the lightweight pg_notify reference.
type notifyRef struct {
	EventID int64 `json:"event_id"`
}

// Filter selects which events a subscriber receives; empty = all.
type Filter struct {
	WorkspaceID string
	BoardID     string
}

// matches reports whether a filter selects this event. A board-filtered
// subscriber receives: events for the board AND workspace-scoped events
// (labels, workspace edits) of the board's workspace — ?board_id= means
// "this board + its workspace's shared entities".
func (f Filter) matches(e hubEnvelope) bool {
	if f.BoardID != "" {
		if e.BoardID != nil {
			return *e.BoardID == f.BoardID
		}
		return f.WorkspaceID != "" && e.WorkspaceID != nil && *e.WorkspaceID == f.WorkspaceID
	}
	if f.WorkspaceID != "" {
		return e.WorkspaceID != nil && *e.WorkspaceID == f.WorkspaceID
	}
	return true
}

// Hub fans out LISTEN notifications to per-connection subscribers.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Message]Filter
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan Message]Filter)}
}

// Subscribe registers a subscriber; the returned cancel function unregisters
// and closes the channel.
func (h *Hub) Subscribe(f Filter) (chan Message, func()) {
	ch := make(chan Message, 16)
	h.mu.Lock()
	h.subs[ch] = f
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
	return ch, cancel
}

// publish fans the message out to matching subscribers; slow clients are
// dropped (non-blocking, bounded buffers).
func (h *Hub) publish(e hubEnvelope, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, f := range h.subs {
		if !f.matches(e) {
			continue
		}
		select {
		case ch <- Message{Payload: data}:
		default:
			// full buffer: drop to keep streaming latency low
		}
	}
}

// hydrate resolves the reference into the full event payload + filter keys.
func hydrate(ctx context.Context, q *db.Queries, payload string) (hubEnvelope, []byte, bool) {
	var ref notifyRef
	if err := json.Unmarshal([]byte(payload), &ref); err != nil {
		slog.Warn("events: bad pg_notify payload", "err", err)
		return hubEnvelope{}, nil, false
	}
	row, err := q.GetEventByID(ctx, ref.EventID)
	if err != nil {
		slog.Warn("events: cannot hydrate event", "id", ref.EventID, "err", err)
		return hubEnvelope{}, nil, false
	}
	e := hubEnvelope{}
	if row.WorkspaceID.Valid {
		ws := uuidStr(row.WorkspaceID.Bytes)
		e.WorkspaceID = &ws
	}
	if row.BoardID.Valid {
		bd := uuidStr(row.BoardID.Bytes)
		e.BoardID = &bd
	}
	return e, row.Payload, true
}

// Listen consumes pg_notify until ctx is done, with bounded-backoff reconnect
// so realtime survives connection drops.
func (h *Hub) Listen(ctx context.Context, pool *pgxpool.Pool) error {
	backoff := time.Second
	for {
		err := h.listenOnce(ctx, pool)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			return nil
		}
		slog.Error("events: LISTEN stopped; retrying", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (h *Hub) listenOnce(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN kanban_events"); err != nil {
		return err
	}
	q := db.New(conn)
	slog.Info("events: LISTEN kanban_events")
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		e, data, ok := hydrate(ctx, q, n.Payload)
		if !ok {
			continue
		}
		h.publish(e, data)
	}
}

// uuidStr renders a [16]byte as a canonical UUID string.
func uuidStr(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

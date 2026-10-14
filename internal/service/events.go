package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"api-first-kanban/internal/db"
)

// notify reference envelope sent over pg_notify; the SSE handler hydrates the
// full payload by event id (kills the pg_notify 8000-byte ceiling).
type notifyRef struct {
	EventID int64 `json:"event_id"`
}

// emitEvent writes the event row and sends a lightweight pg_notify reference
// (same transaction), returning the payload for logging/tests.
//
// workspace_id/board_id: pass what you have; when boardID is set and wsID is
// empty, workspace_id is derived from the board — both columns are ALWAYS
// populated (except for workspace-scoped events, which carry board_id = NULL).
func (s *Service) emitEvent(ctx context.Context, q *db.Queries, typ, agent, wsID, boardID, entityKey string, entity any, extras map[string]any) ([]byte, error) {
	if boardID != "" && wsID == "" {
		board, err := q.GetBoard(ctx, boardID)
		if err == nil {
			wsID = board.WorkspaceID
		}
	}
	data := map[string]any{
		"type":         typ,
		"agent":        agent,
		"at":           time.Now().UTC().Format(time.RFC3339Nano),
		"workspace_id": nill(wsID),
		"board_id":     nill(boardID),
		entityKey:      entity,
	}
	for k, v := range extras {
		data[k] = v
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	evtID, err := q.CreateEvent(ctx, db.CreateEventParams{
		WorkspaceID: pgUUIDorNull(wsID),
		BoardID:     pgUUIDorNull(boardID),
		Type:        typ,
		Agent:       agent,
		Payload:     payload,
	})
	if err != nil {
		return nil, err
	}
	ref, _ := json.Marshal(notifyRef{EventID: evtID})
	if err := q.NotifyEvent(ctx, p(string(ref))); err != nil {
		return nil, err
	}
	return payload, nil
}

// eventSpec captures what a mutating fn produced for event emission.
type eventSpec struct {
	typ         string
	agent       string
	workspaceID string
	boardID     string
	entityKey   string
	entity      any
	// extra top-level payload keys (e.g. column.moved carries the reordered
	// sibling list under "columns"). Merged into the stored payload verbatim.
	extra map[string]any
}

// withEventTx runs a mutation + event emission in ONE transaction (INSERT +
// pg_notify reference in the same tx). The mutation fn returns a SLICE of
// eventSpecs (PB-85): a mutation whose parent's child counts changed appends
// one item.updated spec for the parent AFTER its own event spec. Delivery to
// the SSE hub happens via the LISTEN path only — there is no same-process
// re-publish.
func (s *Service) withEventTx(ctx context.Context, fn func(q *db.Queries) ([]eventSpec, error)) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	specs, err := fn(db.New(tx))
	if err != nil {
		return err
	}
	eq := db.New(tx)
	for i := range specs {
		spec := &specs[i]
		if _, err := s.emitEvent(ctx, eq, spec.typ, spec.agent, spec.workspaceID, spec.boardID, spec.entityKey, spec.entity, spec.extra); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

// parentReemitSpecs builds the post-mutation item.updated event specs for a
// mutated item's DIRECT parent (cascade depth exactly 1: a task closing under
// an epic re-emits the epic, never the milestone). Counts and labels are read
// inside the caller's tx AFTER the child mutation, so the event entity carries
// the post-mutation counts (tx visibility — pinned by test).
func parentReemitSpecs(ctx context.Context, q *db.Queries, agent string, parentIDs ...string) ([]eventSpec, error) {
	seen := make(map[string]bool, len(parentIDs))
	var specs []eventSpec
	for _, id := range parentIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		parent, err := q.GetItem(ctx, id)
		if err != nil {
			return nil, err
		}
		labels, err := labelsForItemTx(ctx, q, parent.ID)
		if err != nil {
			return nil, err
		}
		counts, err := childCountsForItemTx(ctx, q, parent.ID)
		if err != nil {
			return nil, err
		}
		specs = append(specs, eventSpec{
			typ:       "item.updated",
			agent:     agent,
			boardID:   parent.BoardID,
			entityKey: "item",
			entity:    itemToView(parent, labels, counts),
		})
	}
	return specs, nil
}

// pgUUIDorNull scans an id string ("" → NULL).
func pgUUIDorNull(id string) pgtype.UUID {
	var u pgtype.UUID
	if id == "" {
		return u
	}
	_ = u.Scan(id)
	return u
}

// nill maps "" to nil for JSON emission.
func nill(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

var _ = strconv.Itoa
var _ = fmt.Sprintf // reserved for future helpers

// uuidStr renders a nullable uuid as a plain string ("" when NULL) for the
// parent-re-emit id collection.
func uuidStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return u.String()
}

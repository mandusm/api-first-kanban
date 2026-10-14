package service

import (
	"context"
	"strings"

	"api-first-kanban/internal/db"
)

// PB-117 auto-transition of milestone/epic parent columns.
//
// Column roles are derived from the column NAME so the rule works on any board
// (the default board uses todo/doing/done; the project board uses
// Backlog/In Progress/In Review/Done). This mapping is the single source of
// truth documented in docs/api.md:
//
//	todo / backlog        -> "todo"
//	doing / in progress   -> "in_progress"
//	done                  -> "done"
//	anything else         -> "" (unclassified; never a target)
//
// "in review" is deliberately NOT done — review is a human gate.
func columnRole(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "doing", "in progress":
		return "in_progress"
	case "done":
		return "done"
	case "todo", "backlog":
		return "todo"
	}
	return ""
}

// boardColumnRoles holds the column ids a board's auto-transition targets,
// resolved by name ("" when the board has no such column).
type boardColumnRoles struct {
	inProgress string
	done       string
}

func boardColumnRolesFor(ctx context.Context, q *db.Queries, boardID string) (boardColumnRoles, error) {
	cols, err := q.ListColumns(ctx, boardID)
	if err != nil {
		return boardColumnRoles{}, err
	}
	var r boardColumnRoles
	for _, c := range cols {
		switch columnRole(c.Name) {
		case "in_progress":
			if r.inProgress == "" {
				r.inProgress = c.ID
			}
		case "done":
			if r.done == "" {
				r.done = c.ID
			}
		}
	}
	return r, nil
}

// autoTransitionSpecs is the PB-117 entry point: after a child mutation it
// examines the child's DIRECT parent and, when the parent is a milestone/epic
// on the SAME board, applies the forward/reverse rule. Cascade depth is exactly
// 1 (mirroring parentReemitSpecs): the parent's own parent is never examined,
// so a milestone's auto-transition never triggers its own parent. Cross-board
// children still count for roll-up but never drive the parent's column.
// Returns the parent's own events (item.moved and/or item.updated), if any.
func autoTransitionSpecs(ctx context.Context, q *db.Queries, agent, childID string) ([]eventSpec, error) {
	child, err := q.GetItem(ctx, childID)
	if err != nil {
		return nil, err
	}
	if !child.ParentItemID.Valid {
		return nil, nil
	}
	parent, err := q.GetItem(ctx, child.ParentItemID.String())
	if err != nil {
		return nil, err
	}
	// Only milestone/epic parents auto-transition; tasks never auto-move.
	if parent.Kind != "milestone" && parent.Kind != "epic" {
		return nil, nil
	}
	if parent.BoardID != child.BoardID {
		return nil, nil
	}
	return autoTransitionParent(ctx, q, agent, parent)
}

// autoTransitionParent applies the forward (start/finish) or reverse (reopen)
// rule for one milestone/epic parent. It is idempotent: when the parent already
// satisfies the target state it returns no specs (no spurious move/event).
func autoTransitionParent(ctx context.Context, q *db.Queries, agent string, parent db.Item) ([]eventSpec, error) {
	counts, err := childCountsForItemTx(ctx, q, parent.ID)
	if err != nil {
		return nil, err
	}
	if counts.Total == 0 {
		return nil, nil // no non-archived children: no signal
	}
	roles, err := boardColumnRolesFor(ctx, q, parent.BoardID)
	if err != nil {
		return nil, err
	}
	parentCol, err := q.GetColumn(ctx, parent.ColumnID)
	if err != nil {
		return nil, err
	}
	parentRole := columnRole(parentCol.Name)
	if parent.ClosedAt.Valid || parentRole == "done" {
		return autoReverseParent(ctx, q, agent, parent, roles, counts)
	}
	// Forward B (finish): every non-archived child closed -> Done + closed.
	if counts.Closed == counts.Total {
		if roles.done == "" || (parentRole != "in_progress" && parentRole != "todo") {
			return nil, nil
		}
		return autoMoveParent(ctx, q, agent, parent, roles.done, true)
	}
	// Forward A (start): a child sits In Progress -> move a Todo/Backlog parent
	// In Progress. A parent already In Progress is left alone (idempotent).
	if parentRole == "todo" && roles.inProgress != "" {
		stat, err := childrenColumnStat(ctx, q, parent.ID, roles.inProgress)
		if err != nil {
			return nil, err
		}
		if stat.In > 0 {
			return autoMoveParent(ctx, q, agent, parent, roles.inProgress, false)
		}
	}
	return nil, nil
}

// autoReverseParent reopens a Done/closed parent when a child is open again or
// has moved out of the Done column. It stays put while every non-archived child
// is closed AND sits in Done (idempotent).
func autoReverseParent(ctx context.Context, q *db.Queries, agent string, parent db.Item, roles boardColumnRoles, counts childCounts) ([]eventSpec, error) {
	if counts.Closed == counts.Total && roles.done != "" {
		stat, err := childrenColumnStat(ctx, q, parent.ID, roles.done)
		if err != nil {
			return nil, err
		}
		if stat.Total > 0 && stat.In == stat.Total {
			return nil, nil // parent stays Done
		}
	}
	closedChanged := false
	if parent.ClosedAt.Valid {
		if _, err := q.ReopenItem(ctx, parent.ID); err != nil {
			return nil, err
		}
		closedChanged = true
	}
	columnChanged := false
	if parent.ColumnID == roles.done && roles.inProgress != "" {
		positions, err := positionsAt(ctx, q, roles.inProgress)
		if err != nil {
			return nil, err
		}
		moved, err := q.MoveItem(ctx, db.MoveItemParams{
			ID:       parent.ID,
			ColumnID: roles.inProgress,
			Position: pgNum(fracTop(positions)),
		})
		if err != nil {
			return nil, err
		}
		if _, err := maybeRenormalize(ctx, q, roles.inProgress, moved.Position); err != nil {
			return nil, err
		}
		columnChanged = true
	}
	if !closedChanged && !columnChanged {
		return nil, nil
	}
	return parentAutoSpecs(ctx, q, agent, parent.ID, columnChanged, closedChanged)
}

// autoMoveParent moves a parent to the top of target (same board) and, when
// closeIt, closes it too. Events: item.moved for the column change and, when
// the closed flag also flipped, item.updated.
func autoMoveParent(ctx context.Context, q *db.Queries, agent string, parent db.Item, target string, closeIt bool) ([]eventSpec, error) {
	positions, err := positionsAt(ctx, q, target)
	if err != nil {
		return nil, err
	}
	moved, err := q.MoveItem(ctx, db.MoveItemParams{
		ID:       parent.ID,
		ColumnID: target,
		Position: pgNum(fracTop(positions)),
	})
	if err != nil {
		return nil, err
	}
	if _, err := maybeRenormalize(ctx, q, target, moved.Position); err != nil {
		return nil, err
	}
	closedChanged := false
	if closeIt && !moved.ClosedAt.Valid {
		if _, err := q.UpdateItemFields(ctx, db.UpdateItemFieldsParams{
			ID:       moved.ID,
			ClosedAt: closeAtOpen(p(true)),
		}); err != nil {
			return nil, err
		}
		closedChanged = true
	}
	return parentAutoSpecs(ctx, q, agent, parent.ID, true, closedChanged)
}

// parentAutoSpecs re-reads the parent (final position after any renumber) and
// builds item.moved (column changed) and/or item.updated (closed flipped) for
// the post-mutation entity — so SSE and the parent-re-emit stay consistent.
// Only the transitions that actually happened are emitted (no spurious event).
func parentAutoSpecs(ctx context.Context, q *db.Queries, agent, parentID string, columnChanged, closedChanged bool) ([]eventSpec, error) {
	fresh, err := q.GetItem(ctx, parentID)
	if err != nil {
		return nil, err
	}
	labels, err := labelsForItemTx(ctx, q, fresh.ID)
	if err != nil {
		return nil, err
	}
	counts, err := childCountsForItemTx(ctx, q, fresh.ID)
	if err != nil {
		return nil, err
	}
	entity := itemToView(fresh, labels, counts)
	// One event per auto mutation: a column change is item.moved (its entity
	// already reflects any closed flip too); a closed-only flip is item.updated.
	if columnChanged {
		return []eventSpec{{typ: "item.moved", agent: agent, boardID: fresh.BoardID, entityKey: "item", entity: entity}}, nil
	}
	if closedChanged {
		return []eventSpec{{typ: "item.updated", agent: agent, boardID: fresh.BoardID, entityKey: "item", entity: entity}}, nil
	}
	return nil, nil
}

// childColumnStat counts a parent's non-archived direct children and how many
// of them sit in the given column (the In-Progress / Done checks).
type childColumnStat struct {
	Total int
	In    int
}

func childrenColumnStat(ctx context.Context, q *db.Queries, parentID, columnID string) (childColumnStat, error) {
	children, err := q.ChildrenOfItem(ctx, pgUUIDorNull(parentID))
	if err != nil {
		return childColumnStat{}, err
	}
	var s childColumnStat
	for _, c := range children {
		if c.ArchivedAt.Valid {
			continue
		}
		s.Total++
		if c.ColumnID == columnID {
			s.In++
		}
	}
	return s, nil
}

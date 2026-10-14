package service

import (
	"context"

	"api-first-kanban/internal/db"
)

// ArchiveItem sets archived_at = now() and emits item.archived. Archive state
// is independent of closed (a closed item archives fine, and an archived item
// keeps its closed badge). Archiving an already-archived item -> 409; an
// unknown item -> 404. Archiving never cascades: the item's children,
// relations, labels and comments are untouched, and it may still be referenced
// as a parent.
func (s *Service) ArchiveItem(ctx context.Context, id, agent string) (ItemView, error) {
	return s.setArchived(ctx, id, true, agent)
}

// UnarchiveItem clears archived_at (NULL = active) and emits item.unarchived.
// Unarchiving an item that is not archived -> 409; an unknown item -> 404.
func (s *Service) UnarchiveItem(ctx context.Context, id, agent string) (ItemView, error) {
	return s.setArchived(ctx, id, false, agent)
}

// setArchived flips the archive state in one tx (guarded UPDATE + event). The
// current state is read first so a double-archive / unarchive-of-active is a
// 409 and a missing item a 404; the guarded UPDATE also turns a concurrent
// state change into a 409 instead of a 500.
func (s *Service) setArchived(ctx context.Context, id string, archive bool, agent string) (ItemView, error) {
	item, err := s.getItemRow(ctx, id)
	if err != nil {
		return ItemView{}, err
	}
	if item.ArchivedAt.Valid == archive {
		if archive {
			return ItemView{}, conflict("item %s is already archived", id)
		}
		return ItemView{}, conflict("item %s is not archived", id)
	}
	typ := "item.unarchived"
	if archive {
		typ = "item.archived"
	}
	var updated db.Item
	err = s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		var row db.Item
		var err error
		if archive {
			row, err = q.ArchiveItem(ctx, id)
		} else {
			row, err = q.UnarchiveItem(ctx, id)
		}
		if err != nil {
			return nil, err
		}
		labels, err := labelsForItemTx(ctx, q, row.ID)
		if err != nil {
			return nil, err
		}
		counts, err := childCountsForItemTx(ctx, q, row.ID)
		if err != nil {
			return nil, err
		}
		updated = row
		specs := []eventSpec{{typ: typ, agent: agent, boardID: row.BoardID, entityKey: "item", entity: itemToView(row, labels, counts)}}
		// PB-85 parent re-emit: archiving/unarchiving a child removes/restores
		// it from the direct parent's counts (archived children are excluded
		// from BOTH numerator and denominator) — one item.updated for the
		// direct parent with post-mutation counts (same tx). Cascade depth 1.
		parentSpecs, err := parentReemitSpecs(ctx, q, agent, uuidStr(row.ParentItemID))
		if err != nil {
			return nil, err
		}
		return append(specs, parentSpecs...), nil
	})
	if err != nil {
		if isNoRows(err) {
			return ItemView{}, conflict("item %s archive state changed concurrently", id)
		}
		return ItemView{}, err
	}
	labels, err := s.labelsForItem(ctx, id)
	if err != nil {
		return ItemView{}, err
	}
	counts, err := s.childCountsForItem(ctx, id)
	if err != nil {
		return ItemView{}, err
	}
	return itemToView(updated, labels, counts), nil
}

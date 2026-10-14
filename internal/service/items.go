package service

import (
	"context"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"api-first-kanban/internal/db"
)

// UpdateItemOpts carries the optional PATCH /api/items/{id} fields; nil = leave
// unchanged. label_ids, when present (non-nil), replaces the full label set.
// Description "" (empty string) clears the description (stored NULL); JSON
// null keeps the current value (v1 semantics).
type UpdateItemOpts struct {
	Title       *string
	Description *string
	OwnerAgent  *string
	Priority    *int16
	Kind        *string
	// ParentSet reports whether the request explicitly carried parent_item_id:
	// absent keeps the current parent, present with a nil ParentItemID clears
	// it, present with a value sets it (PATCH three-state).
	ParentSet    bool
	ParentItemID *string
	LabelIDs     []string
	Closed       *bool
}

// ParseParentItemID decodes the PATCH three-state parent_item_id field shared
// by both transports: an absent raw value keeps the current parent (set=false,
// id=nil), an explicit null clears it (set=true, id=nil), and a JSON string
// sets it (set=true, id=&value). Any other JSON type is a validation error.
func ParseParentItemID(raw json.RawMessage) (set bool, id *string, err error) {
	if raw == nil {
		return false, nil, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return true, nil, nil
	}
	var pid string
	if err := json.Unmarshal(raw, &pid); err != nil {
		return false, nil, invalid("parent_item_id must be a string or null")
	}
	return true, &pid, nil
}

// validateKind rejects a kind outside the milestone/epic/task enum.
func validateKind(kind string) error {
	switch kind {
	case "milestone", "epic", "task":
		return nil
	}
	return invalid("kind must be one of milestone, epic, task")
}

// childParentCompatible reports whether an item of childKind may have a parent
// of parentKind under the milestone -> epic -> task rule.
func childParentCompatible(childKind, parentKind string) bool {
	switch childKind {
	case "epic":
		return parentKind == "milestone"
	case "task":
		return parentKind == "epic" || parentKind == "milestone"
	}
	return false // a milestone never has a parent
}

// validateHierarchy enforces the milestone -> epic -> task rule: a milestone
// has no parent, an epic's parent (when present) must be a milestone, and a
// task's parent may be an epic or a milestone. An epic or task with no parent
// is a valid top-level item. The parent must be an existing item in the same
// workspace. This rule alone caps depth at 3, so no cycle detection is needed
// (self-parenting is impossible: no kind may have a parent of its own kind).
// selfID is the item being patched ("" on create); the parent chain is walked
// defensively and rejected if it ever reaches selfID.
func (s *Service) validateHierarchy(ctx context.Context, selfID, kind string, parentID *string, workspaceID string) error {
	if err := validateKind(kind); err != nil {
		return err
	}
	switch kind {
	case "milestone":
		if parentID != nil {
			return invalid("milestone cannot have a parent")
		}
		return nil
	}
	if parentID == nil {
		return nil // an epic or task without a parent is top-level
	}
	if strings.TrimSpace(*parentID) == "" {
		return invalid("parent_item_id must be an item id")
	}
	parent, err := s.getItemRow(ctx, *parentID)
	if err != nil {
		return err
	}
	parentWS, err := s.BoardWorkspaceID(ctx, parent.BoardID)
	if err != nil {
		return err
	}
	if parentWS != workspaceID {
		return invalid("parent item %s belongs to another workspace", *parentID)
	}
	if !childParentCompatible(kind, parent.Kind) {
		if kind == "epic" {
			return invalid("epic parent must be a milestone")
		}
		return invalid("task parent must be an epic or a milestone")
	}
	// Cycle guard: no ancestor may be the item itself. Valid data is at most 3
	// deep, so the bound only guards against pre-existing corruption.
	ancestor := parent
	for depth := 0; ; depth++ {
		if ancestor.ID == selfID {
			return invalid("parent chain would contain item %s", selfID)
		}
		if !ancestor.ParentItemID.Valid {
			break
		}
		if depth >= 3 {
			return invalid("parent chain is too deep")
		}
		ancestor, err = s.getItemRow(ctx, ancestor.ParentItemID.String())
		if err != nil {
			return err
		}
	}
	return nil
}

// validateChildrenKind rejects a kind change when any existing child would
// become an invalid combination under the new parent kind (e.g. a task child
// under a parent changed to task).
func (s *Service) validateChildrenKind(ctx context.Context, id, newKind string) error {
	children, err := s.q.ChildrenOfItem(ctx, pgUUIDorNull(id))
	if err != nil {
		return err
	}
	for _, c := range children {
		if !childParentCompatible(c.Kind, newKind) {
			return invalid("cannot change kind to %s: child %s of kind %s would be invalid", newKind, c.ID, c.Kind)
		}
	}
	return nil
}

// uuidPtr renders a nullable uuid as an API value (nil when NULL).
func uuidPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := u.String()
	return &s
}

// validatePriority rejects priority values outside the 0..3 enum (P0..P3).
func validatePriority(p *int16) error {
	if p != nil && (*p < 0 || *p > 3) {
		return invalid("priority must be between 0 and 3")
	}
	return nil
}

// itemToView maps a db.Item plus labels to the API shape. cc carries the
// derived child progress counts (PB-84): every construction site must supply
// them (SQL-derived) — a defaulted zero would silently serialize 0/0.
func itemToView(item db.Item, labels []Label, cc childCounts) ItemView {
	return ItemView{
		ID:                item.ID,
		BoardID:           item.BoardID,
		ColumnID:          item.ColumnID,
		Key:               item.Key,
		Title:             item.Title,
		Description:       item.Description,
		OwnerAgent:        item.OwnerAgent,
		Priority:          item.Priority,
		Kind:              item.Kind,
		ParentItemID:      uuidPtr(item.ParentItemID),
		Labels:            labels,
		Position:          item.Position,
		CreatedBy:         item.CreatedBy,
		Closed:            item.ClosedAt.Valid,
		ClosedChildren:    cc.Closed,
		TotalChildren:     cc.Total,
		ClosedDescendants: cc.ClosedDescendants,
		TotalDescendants:  cc.TotalDescendants,
		ArchivedAt:        timePtr(item.ArchivedAt),
		CreatedAt:         item.CreatedAt,
		UpdatedAt:         item.UpdatedAt,
		ActivityAt:        activityAt(item, cc.LatestActivityAt),
	}
}

// activityAt folds the item's updated_at and its latest-comment time (PB-122)
// into the derived activity_at: the later of the two. latest is NULL (comment-
// less item) or not supplied by the SQL path, so updated_at stands. The SQL
// row paths (BoardItems / ColumnItemsPage) already compute this via subselect
// and pass the result through cc, in which case the fold is a no-op.
func activityAt(item db.Item, latest pgtype.Timestamptz) time.Time {
	if latest.Valid && latest.Time.After(item.UpdatedAt) {
		return latest.Time
	}
	return item.UpdatedAt
}

// childCounts carries the derived child progress counts (PB-84) between the
// SQL reads and itemToView.
type childCounts struct {
	Closed int
	Total  int
	// PB-118: recursive subtree counts (all non-archived descendants at any
	// depth); zero for leaves by construction.
	ClosedDescendants int
	TotalDescendants  int
	// PB-122: the item's latest activity timestamp (updated_at ∨ newest
	// comment time). SQL row paths (BoardItems / ColumnItemsPage) supply the
	// SQL-derived activity_at directly; the count-read plumbing supplies the
	// latest-comment time and itemToView folds updated_at in. Invalid when
	// unknown / no comments.
	LatestActivityAt pgtype.Timestamptz
}

// withDescendants merges a subtree-counts row into a direct-counts pair
// (PB-118 review DRY: one merge site instead of three inline copies). The
// subtree row is keyed by parent and absent for parents with no non-archived
// descendants, which maps to the 0/0 "no indicator" default.
func (cc childCounts) withDescendants(d childCounts) childCounts {
	cc.ClosedDescendants = d.ClosedDescendants
	cc.TotalDescendants = d.TotalDescendants
	return cc
}

// childCountsForItemTx is the tx-scoped child progress count read used by the
// mutation/event sites so the event entity and response carry the same values
// (mirrors labelsForItemTx).
func childCountsForItemTx(ctx context.Context, q *db.Queries, itemID string) (childCounts, error) {
	row, err := q.ChildCountsForItem(ctx, pgUUIDorNull(itemID))
	if err != nil {
		return childCounts{}, err
	}
	// PB-118: the subtree counts ride along so every itemToView site (events,
	// relations, auto-transition, responses) carries them in one read.
	desc, err := descendantCountsForItemsTx(ctx, q, []string{itemID})
	if err != nil {
		return childCounts{}, err
	}
	cc := childCounts{Closed: int(row.ClosedChildren), Total: int(row.TotalChildren)}
	if d, ok := desc[itemID]; ok {
		cc = cc.withDescendants(d)
	}
	// PB-122: latest-comment time rides along so every itemToView site carries
	// a correct activity_at in the same read.
	latest, err := q.LatestCommentAtForItem(ctx, itemID)
	if err != nil {
		return childCounts{}, err
	}
	cc.LatestActivityAt = latest
	return cc, nil
}

// childCountsForItem is the pool-scoped variant for post-tx response builds.
func (s *Service) childCountsForItem(ctx context.Context, itemID string) (childCounts, error) {
	return childCountsForItemTx(ctx, s.q, itemID)
}

// childCountsForItems is the batched read for childrenOf rows: one GROUP BY
// over all child ids instead of a count query per row. A child with no
// non-archived children is absent from the result and maps to 0/0.
func (s *Service) childCountsForItems(ctx context.Context, ids []string) (map[string]childCounts, error) {
	out := make(map[string]childCounts, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	pids := make([]string, 0, len(ids))
	for _, id := range ids {
		pids = append(pids, id)
	}
	rows, err := s.q.ChildCountsForItemsBatch(ctx, pids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ParentItemID.String()] = childCounts{Closed: int(r.ClosedChildren), Total: int(r.TotalChildren)}
	}
	// PB-118: merge the recursive subtree counts into the same batch of rows.
	desc, err := descendantCountsForItemsTx(ctx, s.q, ids)
	if err != nil {
		return nil, err
	}
	for id, d := range desc {
		out[id] = out[id].withDescendants(d)
	}
	// PB-122: merge batched latest-comment timestamps into the same rows.
	latest, err := s.q.LatestCommentAtForItemsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range latest {
		cc := out[r.ItemID]
		cc.LatestActivityAt = r.LatestCommentAt
		out[r.ItemID] = cc
	}
	return out, nil
}

// descendantCountsForItemsTx is the tx-scoped recursive subtree progress read
// (PB-118): one batched RECURSIVE CTE over all parent ids instead of a
// traversal per item. Parents with no non-archived descendants are absent
// from the result and map to 0/0 by construction.
func descendantCountsForItemsTx(ctx context.Context, q *db.Queries, ids []string) (map[string]childCounts, error) {
	out := make(map[string]childCounts, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.DescendantCountsForItemsBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.AncestorID] = childCounts{
			ClosedDescendants: int(r.ClosedDescendants),
			TotalDescendants:  int(r.TotalDescendants),
		}
	}
	return out, nil
}

// timePtr renders a nullable timestamptz as an API value (nil when NULL).
func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

// positionsAt reads the ordered positions of a column inside the current tx.
func positionsAt(ctx context.Context, q *db.Queries, columnID string) ([]string, error) {
	pos, err := q.ColumnItemPositions(ctx, columnID)
	if err != nil {
		return nil, err
	}
	return unnull(pos), nil
}

// maybeRenormalize renumbers the column 1..n when the new position collided
// with another row. DuplicatePositionCount counts the just-written row itself,
// so a real collision is count > 1. Returns true when a renumber ran (the
// caller must re-read the row: every position in the column was rewritten).
func maybeRenormalize(ctx context.Context, q *db.Queries, columnID, position string) (bool, error) {
	dupes, err := q.DuplicatePositionCount(ctx, db.DuplicatePositionCountParams{ColumnID: columnID, Position: pgNum(position)})
	if err != nil {
		return false, err
	}
	if dupes > 1 {
		if err := q.RenumberColumnPositions(ctx, pgUUIDorNull(columnID)); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// CreateItem inserts an item at the TOP of its column (fractional midpoint —
// see docs/api.md). column defaults to the first column by position. New
// item + labels + item.created event run in one transaction.
func (s *Service) CreateItem(ctx context.Context, boardID, title string, description, columnID, ownerAgent *string, priority *int16, kind, parentItemID *string, labelIDs []string, agent string) (ItemView, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return ItemView{}, invalid("title is required")
	}
	if err := validatePriority(priority); err != nil {
		return ItemView{}, err
	}
	k := "task"
	if kind != nil {
		k = strings.TrimSpace(*kind)
	}
	board, err := s.getBoard(ctx, boardID)
	if err != nil {
		return ItemView{}, err
	}
	if err := s.validateHierarchy(ctx, "", k, parentItemID, board.WorkspaceID); err != nil {
		return ItemView{}, err
	}
	col := ""
	if columnID != nil {
		coln, err := s.getColumn(ctx, *columnID)
		if err != nil {
			return ItemView{}, err
		}
		if coln.BoardID != boardID {
			return ItemView{}, invalid("column %s does not belong to board %s", *columnID, boardID)
		}
		col = coln.ID
	} else {
		first, err := s.q.ColumnFirstByPosition(ctx, boardID)
		if isNoRows(err) {
			return ItemView{}, conflict("board has no columns")
		}
		if err != nil {
			return ItemView{}, s.normalize(err, "board")
		}
		col = first
	}

	var item db.Item
	err = s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		// Board-row lock first (same anchor as MoveColumn): CreateItem inserts a
		// possibly-colliding fractional position and may trigger the renumber
		// loop; without the lock, concurrent same-board creates/moves take item
		// row locks in different orders → 40P01 deadlock. Serializing on the
		// board row gives every tx the same lock order.
		if _, err := q.LockBoard(ctx, boardID); err != nil {
			return nil, err
		}
		positions, err := positionsAt(ctx, q, col)
		if err != nil {
			return nil, err
		}
		seq, err := q.NextBoardKeySeq(ctx, boardID)
		if err != nil {
			return nil, err
		}
		key := board.KeyPrefix + "-" + strconv.FormatInt(seq, 10)
		created, err := q.CreateItem(ctx, db.CreateItemParams{
			BoardID:      boardID,
			ColumnID:     col,
			Title:        title,
			Description:  description,
			Key:          key,
			OwnerAgent:   ownerAgent,
			Priority:     priority,
			Kind:         p(k),
			ParentItemID: pgUUIDorNull(deref(parentItemID)),
			Position:     pgNum(fracTop(positions)),
			CreatedBy:    p(agent),
		})
		if err != nil {
			return nil, err
		}
		if _, err := maybeRenormalize(ctx, q, col, created.Position); err != nil {
			return nil, err
		}
		if err := q.ClearItemLabels(ctx, created.ID); err != nil {
			return nil, err
		}
		if err := addLabelsChecked(ctx, s, q, board.WorkspaceID, created.ID, labelIDs); err != nil {
			return nil, err
		}
		fresh, err := q.GetItem(ctx, created.ID)
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
		item = fresh
		specs := []eventSpec{{typ: "item.created", agent: agent, boardID: boardID, entityKey: "item", entity: itemToView(fresh, labels, counts)}}
		// PB-85 parent re-emit: creating with a parent changes the parent's
		// child counts — emit one item.updated for the direct parent with
		// post-mutation counts (same tx). Cascade depth 1.
		parentSpecs, err := parentReemitSpecs(ctx, q, agent, deref(parentItemID))
		if err != nil {
			return nil, err
		}
		return append(specs, parentSpecs...), nil
	})
	if err != nil {
		return ItemView{}, s.normalize(err, "item")
	}
	labels, err := s.labelsForItem(ctx, item.ID)
	if err != nil {
		return ItemView{}, err
	}
	counts, err := s.childCountsForItem(ctx, item.ID)
	if err != nil {
		return ItemView{}, err
	}
	return itemToView(item, labels, counts), nil
}

// UpdateItem applies partial updates in ONE tx: field patch, optional reopen
// (closed=false), optional label-set replacement, then a single item.updated
// event carrying the fresh entity.
func (s *Service) UpdateItem(ctx context.Context, id string, opts UpdateItemOpts, agent string) (ItemView, error) {
	item, err := s.getItemRow(ctx, id)
	if err != nil {
		return ItemView{}, err
	}
	if opts.Title != nil && strings.TrimSpace(*opts.Title) == "" {
		return ItemView{}, invalid("title cannot be empty")
	}
	if err := validatePriority(opts.Priority); err != nil {
		return ItemView{}, err
	}
	// Effective hierarchy after applying the patch: kind/parent changes are
	// validated together so e.g. a task->epic change with a milestone parent is
	// validated as one combination.
	hierarchyChanged := opts.Kind != nil || opts.ParentSet
	effectiveKind := item.Kind
	if opts.Kind != nil {
		effectiveKind = strings.TrimSpace(*opts.Kind)
	}
	effectiveParent := uuidPtr(item.ParentItemID)
	if opts.ParentSet {
		effectiveParent = opts.ParentItemID
	}
	if hierarchyChanged {
		ws, err := s.BoardWorkspaceID(ctx, item.BoardID)
		if err != nil {
			return ItemView{}, err
		}
		if err := s.validateHierarchy(ctx, id, effectiveKind, effectiveParent, ws); err != nil {
			return ItemView{}, err
		}
		if opts.Kind != nil && effectiveKind != item.Kind {
			if err := s.validateChildrenKind(ctx, id, effectiveKind); err != nil {
				return ItemView{}, err
			}
		}
	}
	var updated db.Item
	err = s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		patched, err := q.UpdateItemFields(ctx, db.UpdateItemFieldsParams{
			ID:          id,
			Title:       opts.Title,
			Description: opts.Description,
			OwnerAgent:  opts.OwnerAgent,
			Priority:    opts.Priority,
			ClosedAt:    closeAtOpen(opts.Closed),
		})
		if err != nil {
			return nil, err
		}
		updated = patched
		if hierarchyChanged {
			hier, err := q.UpdateItemHierarchy(ctx, db.UpdateItemHierarchyParams{
				ID:           id,
				Kind:         effectiveKind,
				ParentItemID: pgUUIDorNull(deref(effectiveParent)),
			})
			if err != nil {
				return nil, err
			}
			updated = hier
		}
		if opts.Closed != nil && !*opts.Closed {
			reopened, err := q.ReopenItem(ctx, id)
			if err != nil {
				return nil, err
			}
			updated = reopened
		}
		if opts.Description != nil && *opts.Description == "" {
			if err := q.ClearItemDescription(ctx, id); err != nil {
				return nil, err
			}
		}
		if opts.LabelIDs != nil {
			if err := s.replaceLabels(ctx, q, id, opts.LabelIDs); err != nil {
				return nil, err
			}
		}
		fresh, err := q.GetItem(ctx, id)
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
		updated = fresh
		specs := []eventSpec{{typ: "item.updated", agent: agent, boardID: fresh.BoardID, entityKey: "item", entity: itemToView(fresh, labels, counts)}}
		// PB-117: a close/reopen flip may satisfy the milestone/epic parent's
		// start/finish/reopen rule (same tx, cascade depth 1).
		if opts.Closed != nil {
			autoSpecs, err := autoTransitionSpecs(ctx, q, agent, fresh.ID)
			if err != nil {
				return nil, err
			}
			specs = append(specs, autoSpecs...)
		}
		// PB-85 parent re-emit: a closed flip (close/reopen) and a
		// parent_item_id change both move a DIRECT parent's child counts —
		// emit one item.updated per affected parent (old + new on a reparent,
		// deduped when equal) with post-mutation counts (same tx). Cascade
		// depth 1: the parent's own parent is never re-emitted. A title-only
		// patch affects no parent's counts and emits no parent event.
		var reemit []string
		if opts.Closed != nil {
			reemit = append(reemit, uuidStr(item.ParentItemID))
		}
		if opts.ParentSet {
			reemit = append(reemit, uuidStr(item.ParentItemID), deref(effectiveParent))
		}
		parentSpecs, err := parentReemitSpecs(ctx, q, agent, reemit...)
		if err != nil {
			return nil, err
		}
		return append(specs, parentSpecs...), nil
	})
	if err != nil {
		return ItemView{}, s.normalize(err, "item")
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

// closeAtOpen maps the closed flag to a timestamptz param: true→now,
// false/nil→NULL (coalesce keeps nothing? no — false must reopen, handled by
// ReopenItem above; nil here means leave untouched via coalesce(NULL, col)).
func closeAtOpen(closed *bool) pgtype.Timestamptz {
	if closed != nil && *closed {
		return pgtype.Timestamptz{Time: timeNow(), Valid: true}
	}
	return pgtype.Timestamptz{} // NULL → coalesce keeps existing
}

// MoveItem moves an item to a column (same board only) at a fractional
// position between its new neighbors; index 0 = top (default). Neighbor read,
// write and item.moved event run in one tx; collisions renormalize.
func (s *Service) MoveItem(ctx context.Context, id, columnID string, index *int, agent string) (ItemView, error) {
	item, err := s.getItemRow(ctx, id)
	if err != nil {
		return ItemView{}, err
	}
	target, err := s.getColumn(ctx, columnID)
	if err != nil {
		return ItemView{}, err
	}
	if target.BoardID != item.BoardID {
		return ItemView{}, invalid("column %s does not belong to board %s", columnID, item.BoardID)
	}
	var updated db.Item
	err = s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		// Board-row lock first, mirroring MoveColumn: the collision-triggered
		// renumber below UPDATEs every row in the target column, and concurrent
		// same-board moves would otherwise take those row locks in different
		// orders (40P01 deadlock + half-applied renumbers). The target column
		// always belongs to item.BoardID (enforced above), so one lock covers it.
		if _, err := q.LockBoard(ctx, item.BoardID); err != nil {
			return nil, err
		}
		// The outer getItemRow read is pre-lock; a concurrent same-board move
		// may have renumbered this item's column after it. Re-read so the
		// dropFirst below sees the CURRENT position (a stale one could fail to
		// drop the moved item's old slot and corrupt the neighbor math).
		item, err = q.GetItem(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		positions, err := positionsAt(ctx, q, target.ID)
		if err != nil {
			return nil, err
		}
		if item.ColumnID == target.ID {
			positions = dropFirst(positions, item.Position)
		}
		pos := fracAt(positions, derefInt(index))
		moved, err := q.MoveItem(ctx, db.MoveItemParams{ID: item.ID, ColumnID: target.ID, Position: pgNum(pos)})
		if err != nil {
			return nil, err
		}
		renumbered, err := maybeRenormalize(ctx, q, target.ID, moved.Position)
		if err != nil {
			return nil, err
		}
		if renumbered {
			// A renumber rewrote every position in the column; re-read so the
			// response and event carry the final value, not the pre-renumber one.
			moved, err = q.GetItem(ctx, item.ID)
			if err != nil {
				return nil, err
			}
		}
		labels, err := labelsForItemTx(ctx, q, moved.ID)
		if err != nil {
			return nil, err
		}
		counts, err := childCountsForItemTx(ctx, q, moved.ID)
		if err != nil {
			return nil, err
		}
		updated = moved
		specs := []eventSpec{{typ: "item.moved", agent: agent, boardID: item.BoardID, entityKey: "item", entity: itemToView(moved, labels, counts)}}
		// PB-117: a child column move may satisfy the milestone/epic parent's
		// start/finish/reopen rule (same tx, cascade depth 1).
		autoSpecs, err := autoTransitionSpecs(ctx, q, agent, moved.ID)
		if err != nil {
			return nil, err
		}
		return append(specs, autoSpecs...), nil
	})
	if err != nil {
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

// TransferItem sets owner_agent and posts the handoff audit comment in ONE tx
// (plus the item.transferred event), then the local item rows are re-read.
func (s *Service) TransferItem(ctx context.Context, id, toAgent, note, agent string) (ItemView, error) {
	if strings.TrimSpace(toAgent) == "" {
		return ItemView{}, invalid("to_agent is required")
	}
	item, err := s.getItemRow(ctx, id)
	if err != nil {
		return ItemView{}, err
	}
	from := deref(item.OwnerAgent)
	err = s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		moved, err := q.TransferItem(ctx, db.TransferItemParams{ID: item.ID, OwnerAgent: p(toAgent)})
		if err != nil {
			return nil, err
		}
		body := "[handoff] → " + toAgent
		if from != "" {
			body = "[handoff] " + from + " → " + toAgent
		}
		if note != "" {
			body += " — " + note
		}
		if _, err := q.CreateComment(ctx, db.CreateCommentParams{
			ItemID:     item.ID,
			AuthorType: authorType(agent),
			AuthorName: agent,
			Body:       body,
		}); err != nil {
			return nil, err
		}
		labels, err := labelsForItemTx(ctx, q, moved.ID)
		if err != nil {
			return nil, err
		}
		counts, err := childCountsForItemTx(ctx, q, moved.ID)
		if err != nil {
			return nil, err
		}
		return []eventSpec{{typ: "item.transferred", agent: agent, boardID: item.BoardID, entityKey: "item", entity: itemToView(moved, labels, counts)}}, nil
	})
	if err != nil {
		return ItemView{}, err
	}
	view, err := s.GetItem(ctx, id)
	return view.Item, err
}

// --- labels ---

// LabelsList is the result of ListLabels: exactly one of the two shapes is
// populated. Legacy is the pre-PB-73 response (all labels, name-ordered, no
// usage_count); Ranked is the PB-73 usage-ranked shape.
type LabelsList struct {
	Legacy bool
	Labels []db.Label   // set when Legacy
	Ranked []LabelUsage // set when !Legacy
}

// IsLegacyListLabels reports whether a list-labels call is the legacy
// no-param shape: no top, and no (or blank) query. This is the single
// source of truth for both transports (REST and MCP).
func IsLegacyListLabels(q *string, top *int) bool {
	return top == nil && (q == nil || strings.TrimSpace(*q) == "")
}

// ListLabels lists a workspace's labels. With neither a query nor a top
// (or only blank ones) it returns the legacy shape: all labels,
// name-ordered, without usage_count. With a non-blank query and/or top it
// returns the PB-73 ranked shape (usage-ranked, with usage_count).
func (s *Service) ListLabels(ctx context.Context, workspaceID string, q *string, top *int) (LabelsList, error) {
	if IsLegacyListLabels(q, top) {
		labels, err := s.listLabelsLegacy(ctx, workspaceID)
		return LabelsList{Legacy: true, Labels: labels}, err
	}
	ranked, err := s.ListLabelsRanked(ctx, workspaceID, q, top)
	return LabelsList{Ranked: ranked}, err
}

func (s *Service) listLabelsLegacy(ctx context.Context, workspaceID string) ([]db.Label, error) {
	if _, err := s.q.GetWorkspace(ctx, workspaceID); isNoRows(err) {
		return nil, notFound("workspace %s does not exist", workspaceID)
	} else if err != nil {
		return nil, s.normalize(err, "workspace")
	}
	labels, err := s.q.ListLabels(ctx, workspaceID)
	return unnull(labels), err
}

// LabelUsage is a label carrying its usage count (number of item-label links
// across the workspace's items, archived included).
type LabelUsage struct {
	db.Label
	UsageCount int64 `json:"usage_count"`
}

// ListLabelsRanked returns workspace labels ranked by usage (descending),
// ties broken by name ascending. q (when non-empty) filters by
// case-insensitive substring on name BEFORE ranking (filter-then-rank);
// top (when positive) truncates the ranked list to the first N.
func (s *Service) ListLabelsRanked(ctx context.Context, workspaceID string, q *string, top *int) ([]LabelUsage, error) {
	if _, err := s.q.GetWorkspace(ctx, workspaceID); isNoRows(err) {
		return nil, notFound("workspace %s does not exist", workspaceID)
	} else if err != nil {
		return nil, s.normalize(err, "workspace")
	}
	var qp *string
	if q != nil && strings.TrimSpace(*q) != "" {
		// Literal-substring contract: escape LIKE metacharacters so user input
		// is matched verbatim, not as a pattern.
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(*q)
		qp = &esc
	}
	if top != nil && *top < 1 {
		return nil, invalid("top must be a positive integer")
	}
	rows, err := s.q.ListLabelsRanked(ctx, db.ListLabelsRankedParams{WorkspaceID: workspaceID, Q: qp})
	if err != nil {
		return nil, err
	}
	out := make([]LabelUsage, 0, len(rows))
	for _, r := range rows {
		out = append(out, LabelUsage{Label: db.Label{ID: r.ID, WorkspaceID: r.WorkspaceID, Name: r.Name, Color: r.Color}, UsageCount: r.UsageCount})
	}
	if top != nil && *top < len(out) {
		out = out[:*top]
	}
	return out, nil
}

func (s *Service) CreateLabel(ctx context.Context, workspaceID, name string, color *string, agent string) (db.Label, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return db.Label{}, invalid("name is required")
	}
	if _, err := s.q.GetWorkspace(ctx, workspaceID); isNoRows(err) {
		return db.Label{}, notFound("workspace %s does not exist", workspaceID)
	} else if err != nil {
		return db.Label{}, s.normalize(err, "workspace")
	}
	c := "#6366f1"
	if color != nil && strings.TrimSpace(*color) != "" {
		c = strings.TrimSpace(*color)
	}
	cc := c
	var l db.Label
	err := s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		created, err := q.CreateLabel(ctx, db.CreateLabelParams{WorkspaceID: workspaceID, Name: name, Color: &cc})
		if err != nil {
			return nil, err
		}
		l = created
		return []eventSpec{{typ: "label.created", agent: agent, workspaceID: workspaceID, entityKey: "label", entity: l}}, nil
	})
	return l, s.normalize(err, "label name")
}

func (s *Service) UpdateLabel(ctx context.Context, id string, name, color *string, agent string) (db.Label, error) {
	if _, err := s.q.GetLabel(ctx, id); isNoRows(err) {
		return db.Label{}, notFound("label %s does not exist", id)
	} else if err != nil {
		return db.Label{}, s.normalize(err, "label")
	}
	if name != nil && strings.TrimSpace(*name) == "" {
		return db.Label{}, invalid("name cannot be empty")
	}
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		name = &trimmed
	}
	var l db.Label
	err := s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		updated, err := q.UpdateLabel(ctx, db.UpdateLabelParams{ID: id, Name: name, Color: color})
		if err != nil {
			return nil, err
		}
		l = updated
		return []eventSpec{{typ: "label.updated", agent: agent, workspaceID: l.WorkspaceID, entityKey: "label", entity: l}}, nil
	})
	return l, s.normalize(err, "label name")
}

func (s *Service) DeleteLabel(ctx context.Context, id string, agent string) error {
	l, err := s.q.GetLabel(ctx, id)
	if isNoRows(err) {
		return notFound("label %s does not exist", id)
	}
	if err != nil {
		return s.normalize(err, "label")
	}
	return s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		if err := q.DeleteLabel(ctx, id); err != nil {
			return nil, err
		}
		return []eventSpec{{typ: "label.deleted", agent: agent, workspaceID: l.WorkspaceID, entityKey: "label", entity: l}}, nil
	})
}

// --- comments ---

func (s *Service) ListComments(ctx context.Context, itemID string) ([]db.Comment, error) {
	if _, err := s.getItemRow(ctx, itemID); err != nil {
		return nil, err
	}
	comments, err := s.q.ListComments(ctx, itemID)
	return unnull(comments), err
}

// AddComment appends a comment and a comment.added event in one tx;
// author_type is "human" iff agent is empty or "human".
func (s *Service) AddComment(ctx context.Context, itemID, body, agent string) (db.Comment, error) {
	if strings.TrimSpace(body) == "" {
		return db.Comment{}, invalid("body is required")
	}
	item, err := s.getItemRow(ctx, itemID)
	if err != nil {
		return db.Comment{}, err
	}
	var comment db.Comment
	err = s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		created, err := q.CreateComment(ctx, db.CreateCommentParams{
			ItemID:     itemID,
			AuthorType: authorType(agent),
			AuthorName: agent,
			Body:       body,
		})
		if err != nil {
			return nil, err
		}
		comment = created
		return []eventSpec{{typ: "comment.added", agent: agent, boardID: item.BoardID, entityKey: "comment", entity: created}}, nil
	})
	return comment, err
}

func authorType(agent string) string {
	if agent == "" || agent == "human" {
		return "human"
	}
	return "agent"
}

// --- helpers ---

// getItemRow, getColumn, getLabel with 404/400 normalization.
func (s *Service) getItemRow(ctx context.Context, id string) (db.Item, error) {
	item, err := s.q.GetItem(ctx, id)
	if isNoRows(err) {
		return db.Item{}, notFound("item %s does not exist", id)
	}
	if err != nil {
		return db.Item{}, s.normalize(err, "item")
	}
	return item, nil
}

func (s *Service) getBoard(ctx context.Context, id string) (db.Board, error) {
	b, err := s.q.GetBoard(ctx, id)
	if isNoRows(err) {
		return db.Board{}, notFound("board %s does not exist", id)
	}
	if err != nil {
		return db.Board{}, s.normalize(err, "board")
	}
	return b, nil
}

func (s *Service) getColumn(ctx context.Context, id string) (db.Column, error) {
	c, err := s.q.GetColumn(ctx, id)
	if isNoRows(err) {
		return db.Column{}, notFound("column %s does not exist", id)
	}
	if err != nil {
		return db.Column{}, s.normalize(err, "column")
	}
	return c, nil
}

func (s *Service) getLabel(ctx context.Context, id string) (db.Label, error) {
	l, err := s.q.GetLabel(ctx, id)
	if isNoRows(err) {
		return db.Label{}, notFound("label %s does not exist", id)
	}
	if err != nil {
		return db.Label{}, s.normalize(err, "label")
	}
	return l, nil
}

func (s *Service) labelsForItem(ctx context.Context, itemID string) ([]Label, error) {
	return labelsForItemTx(ctx, s.q, itemID)
}

// labelsForItemTx is the tx-scoped label read used by SSE emit sites so event
// payloads carry the same flattened labels as REST responses.
func labelsForItemTx(ctx context.Context, q *db.Queries, itemID string) ([]Label, error) {
	rows, err := q.ItemLabels(ctx, itemID)
	if err != nil {
		return nil, err
	}
	labels := make([]Label, len(rows))
	for i, l := range rows {
		labels[i] = Label{ID: l.ID, WorkspaceID: l.WorkspaceID, Name: l.Name, Color: l.Color}
	}
	return labels, nil
}

// replaceLabels validates workspace scoping then replaces the item's label set
// inside the caller's transaction.
func (s *Service) replaceLabels(ctx context.Context, q *db.Queries, itemID string, ids []string) error {
	item, err := getItemTx(ctx, q, itemID)
	if err != nil {
		return err
	}
	board, err := s.getBoard(ctx, item.BoardID)
	if err != nil {
		return err
	}
	workspaceID := board.WorkspaceID
	if err := q.ClearItemLabels(ctx, itemID); err != nil {
		return err
	}
	return addLabelsChecked(ctx, s, q, workspaceID, itemID, ids)
}

func addLabelsChecked(ctx context.Context, s *Service, q *db.Queries, workspaceID, itemID string, ids []string) error {
	for _, lid := range ids {
		l, err := s.getLabel(ctx, lid)
		if err != nil {
			return err
		}
		if l.WorkspaceID != workspaceID {
			return invalid("label %s belongs to another workspace", lid)
		}
		if err := q.AddItemLabel(ctx, db.AddItemLabelParams{ItemID: itemID, LabelID: lid}); err != nil {
			return err
		}
	}
	return nil
}

func getItemTx(ctx context.Context, q *db.Queries, id string) (db.Item, error) {
	item, err := q.GetItem(ctx, id)
	if isNoRows(err) {
		return db.Item{}, notFound("item %s does not exist", id)
	}
	return item, err
}

// --- fractional position math ---
//
// positions are decimal strings ("1", "1.5", "0.75"); exact math via big.Rat.
// fracAt: slot index i sits between p[i-1] and p[i]; empty column → "1";
// i <= 0 → "0"; append past the end → last + 1.

func fracAt(positions []string, i int) string {
	if len(positions) == 0 {
		return "1"
	}
	if i <= 0 {
		return midpoint("0", positions[0])
	}
	if i >= len(positions) {
		last := ratParse(positions[len(positions)-1])
		return ratString(last.Add(last, big.NewRat(1, 1)))
	}
	return midpoint(positions[i-1], positions[i])
}

func fracTop(positions []string) string { return fracAt(positions, 0) }

func midpoint(a, b string) string {
	ra, rb := ratParse(a), ratParse(b)
	sum := new(big.Rat).Add(ra, rb)
	return ratString(new(big.Rat).Quo(sum, big.NewRat(2, 1)))
}

func ratParse(s string) *big.Rat {
	if r, ok := new(big.Rat).SetString(s); ok {
		return r
	}
	return big.NewRat(0, 1)
}

// ratString formats with 30 fractional digits, then trims zeros ("1.5",
// "0.75", "2").
func ratString(r *big.Rat) string {
	f := r.FloatString(30)
	f = strings.TrimRight(f, "0")
	return strings.TrimSuffix(f, ".")
}

// pgNum wraps a position string in a pgtype.Numeric.
func pgNum(s string) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(s)
	return n
}

func dropFirst(list []string, v string) []string {
	out := make([]string, 0, len(list))
	dropped := false
	for _, p := range list {
		if p == v && !dropped {
			dropped = true
			continue
		}
		out = append(out, p)
	}
	return out
}

func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

// p pointerizes any value.
func p[T any](v T) *T { return &v }

// deref returns "" for nil string pointers.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// unnull normalizes nil slices to [] at the service boundary (JSON [] instead
// of null).
func unnull[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// decodeLabels unmarshals a sqlc row's JSON labels array; an absent/empty
// column yields a nil slice.
func decodeLabels(raw []byte) ([]Label, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var labels []Label
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil, err
	}
	return labels, nil
}

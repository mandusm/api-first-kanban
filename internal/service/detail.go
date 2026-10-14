package service

import (
	"context"

	"api-first-kanban/internal/db"
)

// ItemDetail is the composite GET /api/items/{id} response: the item with its
// labels flattened, its comments (ascending), its outgoing + incoming
// relations, and its child items. The JSON tags are the response keys, so the
// struct serializes as the wire payload directly.
type ItemDetail struct {
	Item      ItemView      `json:"item"`
	Comments  []db.Comment  `json:"comments"`
	Relations ItemRelations `json:"relations"`
	Children  []ItemView    `json:"children"`
}

// GetItem returns the item's full detail read: the item with labels, its
// comments (ascending), its outgoing + incoming relations, and its children
// (oldest first, archived included — see childrenOf). Both transports (REST
// and MCP) serve exactly this payload.
func (s *Service) GetItem(ctx context.Context, id string) (ItemDetail, error) {
	item, err := s.getItemRow(ctx, id)
	if err != nil {
		return ItemDetail{}, err
	}
	labels, err := s.labelsForItem(ctx, id)
	if err != nil {
		return ItemDetail{}, err
	}
	comments, err := s.q.ListComments(ctx, id)
	if err != nil {
		return ItemDetail{}, err
	}
	relations, err := s.Relations(ctx, id)
	if err != nil {
		return ItemDetail{}, err
	}
	children, err := s.childrenOf(ctx, id)
	if err != nil {
		return ItemDetail{}, err
	}
	counts, err := s.childCountsForItem(ctx, id)
	if err != nil {
		return ItemDetail{}, err
	}
	return ItemDetail{
		Item:      itemToView(item, labels, counts),
		Comments:  unnull(comments),
		Relations: relations,
		Children:  children,
	}, nil
}

// childrenOf lists an item's children as full item views (labels flattened).
// Ordering is created_at ascending: positions are per-column and not
// comparable across children, which may span columns and even boards (the
// parent rule is workspace-scoped). Archived children are included — the
// item-detail read is the full-truth read, mirroring GET /api/items/{id} on
// an archived item itself. The caller has already resolved the item (404).
func (s *Service) childrenOf(ctx context.Context, id string) ([]ItemView, error) {
	rows, err := s.q.ChildrenOfItem(ctx, pgUUIDorNull(id))
	if err != nil {
		return nil, err
	}
	children := make([]ItemView, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	counts, err := s.childCountsForItems(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		labels, err := labelsForItemTx(ctx, s.q, row.ID)
		if err != nil {
			return nil, err
		}
		children = append(children, itemToView(row, labels, counts[row.ID]))
	}
	return children, nil
}

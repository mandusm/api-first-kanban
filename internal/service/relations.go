package service

import (
	"context"
	"time"

	"api-first-kanban/internal/db"
)

// Relation values stored in item_relations.relation ("blocked by" is the
// derived incoming VIEW of "blocks", never a stored value).
const (
	RelationBlocks     = "blocks"
	RelationRelatesTo  = "relates_to"
	RelationDuplicates = "duplicates"
)

// relationKinds is the allowed relation enum.
var relationKinds = map[string]bool{
	RelationBlocks:     true,
	RelationRelatesTo:  true,
	RelationDuplicates: true,
}

// RelationEdge is one directed relation seen from a given item: Item is the
// item at the OTHER end of the edge (the target when Direction is "outgoing",
// the source when "incoming").
type RelationEdge struct {
	ID             string    `json:"id"`
	Relation       string    `json:"relation"`
	Direction      string    `json:"direction"`
	Item           ItemView  `json:"item"`
	CreatedByAgent *string   `json:"created_by_agent"`
	CreatedAt      time.Time `json:"created_at"`
}

// ItemRelations bundles an item's outgoing and incoming edges.
type ItemRelations struct {
	Outgoing []RelationEdge `json:"outgoing"`
	Incoming []RelationEdge `json:"incoming"`
}

// relationEdge builds one edge from a relation row, reading the other item's
// ItemView (labels flattened) inside the caller's queries handle.
func relationEdge(ctx context.Context, q *db.Queries, r db.ItemRelation, direction string) (RelationEdge, error) {
	otherID := r.ToItemID
	if direction == "incoming" {
		otherID = r.FromItemID
	}
	item, err := q.GetItem(ctx, otherID)
	if err != nil {
		return RelationEdge{}, err
	}
	labels, err := labelsForItemTx(ctx, q, otherID)
	if err != nil {
		return RelationEdge{}, err
	}
	counts, err := childCountsForItemTx(ctx, q, otherID)
	if err != nil {
		return RelationEdge{}, err
	}
	return RelationEdge{
		ID:             r.ID,
		Relation:       r.Relation,
		Direction:      direction,
		Item:           itemToView(item, labels, counts),
		CreatedByAgent: r.CreatedByAgent,
		CreatedAt:      r.CreatedAt,
	}, nil
}

// sameWorkspace reports whether two items live in the same workspace (items
// join to their workspace through their board).
func (s *Service) sameWorkspace(ctx context.Context, a, b db.Item) (bool, error) {
	ab, err := s.getBoard(ctx, a.BoardID)
	if err != nil {
		return false, err
	}
	bb, err := s.getBoard(ctx, b.BoardID)
	if err != nil {
		return false, err
	}
	return ab.WorkspaceID == bb.WorkspaceID, nil
}

// CreateRelation links fromItemID -> toItemID with the given relation. Both
// items must exist in the same workspace, cannot be the same item, and the
// exact (from,to,relation) triple must be new (409 otherwise). The
// relation.created event carries the outgoing edge.
func (s *Service) CreateRelation(ctx context.Context, fromItemID, toItemID, relation, agent string) (RelationEdge, error) {
	if !relationKinds[relation] {
		return RelationEdge{}, invalid("relation must be one of blocks, relates_to, duplicates")
	}
	if fromItemID == toItemID {
		return RelationEdge{}, invalid("an item cannot relate to itself")
	}
	from, err := s.getItemRow(ctx, fromItemID)
	if err != nil {
		return RelationEdge{}, err
	}
	to, err := s.getItemRow(ctx, toItemID)
	if err != nil {
		return RelationEdge{}, err
	}
	same, err := s.sameWorkspace(ctx, from, to)
	if err != nil {
		return RelationEdge{}, err
	}
	if !same {
		return RelationEdge{}, invalid("related items must belong to the same workspace")
	}

	var edge RelationEdge
	err = s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		created, err := q.CreateRelation(ctx, db.CreateRelationParams{
			FromItemID:     fromItemID,
			ToItemID:       toItemID,
			Relation:       relation,
			CreatedByAgent: p(agent),
		})
		if err != nil {
			return nil, err
		}
		edge, err = relationEdge(ctx, q, created, "outgoing")
		if err != nil {
			return nil, err
		}
		return []eventSpec{{typ: "relation.created", agent: agent, boardID: from.BoardID, entityKey: "relation", entity: edge}}, nil
	})
	if err != nil {
		return RelationEdge{}, s.normalize(err, "relation")
	}
	return edge, nil
}

// DeleteRelation removes a relation by id (404 when missing). The
// relation.deleted event carries the outgoing edge as it was before deletion.
func (s *Service) DeleteRelation(ctx context.Context, relationID, agent string) error {
	rel, err := s.q.GetRelation(ctx, relationID)
	if isNoRows(err) {
		return notFound("relation %s does not exist", relationID)
	}
	if err != nil {
		return s.normalize(err, "relation")
	}
	from, err := s.getItemRow(ctx, rel.FromItemID)
	if err != nil {
		return err
	}
	return s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		edge, err := relationEdge(ctx, q, rel, "outgoing")
		if err != nil {
			return nil, err
		}
		if err := q.DeleteRelation(ctx, relationID); err != nil {
			return nil, err
		}
		return []eventSpec{{typ: "relation.deleted", agent: agent, boardID: from.BoardID, entityKey: "relation", entity: edge}}, nil
	})
}

// Relations lists an item's outgoing and incoming edges (empty slices, never
// null). The item itself must exist (404 otherwise).
func (s *Service) Relations(ctx context.Context, itemID string) (ItemRelations, error) {
	if _, err := s.getItemRow(ctx, itemID); err != nil {
		return ItemRelations{}, err
	}
	out, err := s.q.OutgoingRelations(ctx, itemID)
	if err != nil {
		return ItemRelations{}, err
	}
	in, err := s.q.IncomingRelations(ctx, itemID)
	if err != nil {
		return ItemRelations{}, err
	}
	rels := ItemRelations{Outgoing: make([]RelationEdge, 0, len(out)), Incoming: make([]RelationEdge, 0, len(in))}
	for _, r := range out {
		e, err := relationEdge(ctx, s.q, r, "outgoing")
		if err != nil {
			return ItemRelations{}, err
		}
		rels.Outgoing = append(rels.Outgoing, e)
	}
	for _, r := range in {
		e, err := relationEdge(ctx, s.q, r, "incoming")
		if err != nil {
			return ItemRelations{}, err
		}
		rels.Incoming = append(rels.Incoming, e)
	}
	return rels, nil
}

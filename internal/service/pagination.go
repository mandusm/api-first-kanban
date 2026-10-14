package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"api-first-kanban/internal/db"
)

// Page size bounds for GET /api/boards/{id}/items. An omitted limit defaults to
// DefaultPageLimit; an explicit limit outside (0, MaxPageLimit] is a 400.
const (
	DefaultPageLimit = 100
	MaxPageLimit     = 500
)

// ItemPage is the payload of GET /api/boards/{id}/items. NextCursor is nil when
// the column has no further items.
type ItemPage struct {
	Items      []ItemView `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

// itemCursor is the keyset position of the previous page's last item:
// (column position, item position, id). It is base64(JSON) on the wire and
// opaque to clients.
type itemCursor struct {
	ColumnPosition string `json:"cp"`
	Position       string `json:"p"`
	ID             string `json:"id"`
}

func encodeCursor(columnPosition, position, id string) string {
	b, _ := json.Marshal(itemCursor{ColumnPosition: columnPosition, Position: position, ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses an opaque cursor token, rejecting anything malformed or
// incomplete (-> 400 at the transport).
func decodeCursor(token string) (itemCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return itemCursor{}, invalid("malformed cursor")
	}
	var c itemCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return itemCursor{}, invalid("malformed cursor")
	}
	if c.ColumnPosition == "" || c.Position == "" || c.ID == "" {
		return itemCursor{}, invalid("malformed cursor")
	}
	if _, ok := new(big.Rat).SetString(c.ColumnPosition); !ok {
		return itemCursor{}, invalid("malformed cursor")
	}
	if _, ok := new(big.Rat).SetString(c.Position); !ok {
		return itemCursor{}, invalid("malformed cursor")
	}
	var u pgtype.UUID
	if u.Scan(c.ID) != nil || !u.Valid {
		return itemCursor{}, invalid("malformed cursor")
	}
	return c, nil
}

// ListBoardItems returns one keyset page of a column's items ordered by
// (position, id). column_id is required and must belong to boardID (unknown or
// malformed -> 400). cursor is the opaque token from the previous page ("" for
// the first page). Archived items are excluded unless includeArchived is true.
// The full board snapshot remains the complete read path; pagination never
// truncates it.
func (s *Service) ListBoardItems(ctx context.Context, boardID, columnID string, limit int, cursor string, includeArchived bool) (ItemPage, error) {
	if _, err := s.getBoard(ctx, boardID); err != nil {
		return ItemPage{}, err
	}
	if limit <= 0 || limit > MaxPageLimit {
		return ItemPage{}, invalid("limit must be between 1 and %d", MaxPageLimit)
	}
	if strings.TrimSpace(columnID) == "" {
		return ItemPage{}, invalid("column_id is required")
	}
	var cu pgtype.UUID
	if cu.Scan(columnID) != nil || !cu.Valid {
		return ItemPage{}, invalid("malformed column_id")
	}
	col, err := s.q.GetColumn(ctx, columnID)
	if isNoRows(err) {
		return ItemPage{}, invalid("unknown column_id %s", columnID)
	}
	if err != nil {
		return ItemPage{}, s.normalize(err, "column")
	}
	if col.BoardID != boardID {
		return ItemPage{}, invalid("column_id %s does not belong to board %s", columnID, boardID)
	}

	// Read one extra row to detect whether a further page exists.
	params := db.ColumnItemsPageParams{ColumnID: col.ID, Limit: int64(limit) + 1, IncludeArchived: &includeArchived}
	if cursor != "" {
		c, err := decodeCursor(cursor)
		if err != nil {
			return ItemPage{}, err
		}
		params.AfterPosition = pgNum(c.Position)
		params.AfterID = pgUUIDorNull(c.ID)
	}
	rows, err := s.q.ColumnItemsPage(ctx, params)
	if err != nil {
		return ItemPage{}, err
	}

	page := ItemPage{Items: []ItemView{}}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		nc := encodeCursor(strconv.Itoa(int(col.Position)), last.Position, last.ID)
		page.NextCursor = &nc
	}
	// PB-118: batch the recursive subtree counts for the page rows.
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	desc, err := descendantCountsForItemsTx(ctx, s.q, ids)
	if err != nil {
		return ItemPage{}, err
	}
	for _, row := range rows {
		labels, err := decodeLabels(row.Labels)
		if err != nil {
			return ItemPage{}, err
		}
		page.Items = append(page.Items, itemToView(columnPageRow(row), labels, childCounts{
			Closed: int(row.ClosedChildren),
			Total:  int(row.TotalChildren),
			// PB-122: activity_at was computed in the row query itself.
			LatestActivityAt: row.ActivityAt,
		}.withDescendants(desc[row.ID])))
	}
	return page, nil
}

// columnPageRow adapts a ColumnItemsPageRow into a db.Item for itemToView.
func columnPageRow(r db.ColumnItemsPageRow) db.Item {
	return db.Item{ID: r.ID, BoardID: r.BoardID, ColumnID: r.ColumnID, Key: r.Key, Title: r.Title,
		Description: r.Description, OwnerAgent: r.OwnerAgent, Priority: r.Priority, Kind: r.Kind,
		ParentItemID: r.ParentItemID, Position: r.Position,
		CreatedBy: r.CreatedBy, ClosedAt: r.ClosedAt, ArchivedAt: r.ArchivedAt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// Package service holds the domain service layer: transport-agnostic
// interfaces (consumed by internal/httpapi) implemented over sqlc queries.
package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"api-first-kanban/internal/db"
	"api-first-kanban/internal/search"
)

// Sentinel errors mapped to HTTP statuses by the transport layer.
var (
	ErrInvalid   = errors.New("bad request")
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("conflict")
	ErrForbidden = errors.New("forbidden")
)

// ServiceError carries a sentinel plus a human-readable message.
type ServiceError struct {
	sentinel error
	msg      string
}

func (e *ServiceError) Error() string { return e.msg }
func (e *ServiceError) Unwrap() error { return e.sentinel }

// Message returns the human-readable error text.
func (e *ServiceError) Message() string { return e.msg }

// Sentinel returns the categorized sentinel error.
func (e *ServiceError) Sentinel() error {
	return e.sentinel
}

func invalid(f string, a ...any) error {
	return &ServiceError{sentinel: ErrInvalid, msg: fmt.Sprintf(f, a...)}
}

func notFound(f string, a ...any) error {
	return &ServiceError{sentinel: ErrNotFound, msg: fmt.Sprintf(f, a...)}
}

func conflict(f string, a ...any) error {
	return &ServiceError{sentinel: ErrConflict, msg: fmt.Sprintf(f, a...)}
}

// Service implements the domain services over a pgx pool.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

// New creates a Service over the given pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, q: db.New(pool)}
}

// --- workspaces ---

func (s *Service) ListWorkspaces(ctx context.Context) ([]db.Workspace, error) {
	ws, err := s.q.ListWorkspaces(ctx)
	return unnull(ws), err
}

func (s *Service) CreateWorkspace(ctx context.Context, name string, description *string, agent string) (db.Workspace, error) {
	if name == "" || strings.TrimSpace(name) == "" {
		return db.Workspace{}, invalid("name is required")
	}
	name = strings.TrimSpace(name)
	if description != nil && strings.TrimSpace(*description) == "" {
		description = nil
	}
	var ws db.Workspace
	err := s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		created, err := q.CreateWorkspace(ctx, db.CreateWorkspaceParams{Name: name, Description: description})
		if err != nil {
			return nil, err
		}
		ws = created
		return []eventSpec{{typ: "workspace.created", agent: agent, workspaceID: ws.ID, entityKey: "workspace", entity: ws}}, nil
	})
	return ws, s.normalize(err, "workspace name")
}

// --- boards ---

// BoardSnapshot is the full board payload (columns with item_count + items)
// returned by GET /api/boards/{id}. Items is empty (never nil) for a
// lightweight snapshot requested with include_items=false.
type BoardSnapshot struct {
	Board   db.Board     `json:"board"`
	Columns []ColumnView `json:"columns"`
	Items   []ItemView   `json:"items"`
}

// ColumnView is the API representation of a column with the count of live items
// it holds. ItemCount respects the snapshot's archive filter exactly like the
// column's full paginated walk.
type ColumnView struct {
	ID        string `json:"id"`
	BoardID   string `json:"board_id"`
	Name      string `json:"name"`
	Position  int32  `json:"position"`
	ItemCount int64  `json:"item_count"`
}

// ItemView is the API representation of an item with its labels flattened.
// Position is the fractional-ordering value as a decimal string.
type ItemView struct {
	ID           string  `json:"id"`
	BoardID      string  `json:"board_id"`
	ColumnID     string  `json:"column_id"`
	Key          string  `json:"key"`
	Title        string  `json:"title"`
	Description  *string `json:"description"`
	OwnerAgent   *string `json:"owner_agent"`
	Priority     int16   `json:"priority"`
	Kind         string  `json:"kind"`
	ParentItemID *string `json:"parent_item_id"`
	Labels       []Label `json:"labels"`
	Position     string  `json:"position"`
	CreatedBy    *string `json:"created_by"`
	Closed       bool    `json:"closed"`
	// Derived child progress counts (PB-84): direct, non-archived children;
	// closed = closed_at IS NOT NULL. Archived children are excluded from both
	// numerator and denominator; a parent with no live children reads 0/0 (no
	// indicator). Children are workspace-scoped and may sit on another board.
	ClosedChildren int `json:"closed_children"`
	TotalChildren  int `json:"total_children"`
	// Recursive subtree progress counts (PB-118): ALL non-archived
	// descendants at any depth (milestone -> epic -> task); closed = closed_at
	// IS NOT NULL. Archived descendants are excluded from both numerator and
	// denominator; a leaf (no descendants) reads 0/0 — no indicator. With only
	// direct children these equal closed_children/total_children. Children are
	// workspace-scoped and may sit on another board.
	ClosedDescendants int        `json:"closed_descendants"`
	TotalDescendants  int        `json:"total_descendants"`
	ArchivedAt        *time.Time `json:"archived_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	// Derived activity timestamp (PB-122): the item's updated_at or the
	// newest comment on it, whichever is later. AddComment deliberately does
	// not bump updated_at, so comment recency rides here instead. Never null
	// (updated_at is NOT NULL).
	ActivityAt time.Time `json:"activity_at"`
}

// Label is the API representation of a label.
type Label struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
}

// CreateBoard inserts the board and its initial columns in one transaction.
func (s *Service) CreateBoard(ctx context.Context, workspaceID, name string, columns []string, agent string) (BoardSnapshot, error) {
	if strings.TrimSpace(name) == "" {
		return BoardSnapshot{}, invalid("name is required")
	}
	if len(columns) == 0 {
		columns = []string{"todo", "doing", "done"}
	}
	if _, err := s.q.GetWorkspace(ctx, workspaceID); isNoRows(err) {
		return BoardSnapshot{}, notFound("workspace %s does not exist", workspaceID)
	} else if err != nil {
		return BoardSnapshot{}, s.normalize(err, "workspace")
	}

	var board db.Board
	err := s.withTx(ctx, func(q *db.Queries) error {
		prefix, err := allocBoardPrefix(ctx, q, name)
		if err != nil {
			return err
		}
		created, err := q.CreateBoard(ctx, db.CreateBoardParams{WorkspaceID: workspaceID, Name: name, KeyPrefix: prefix})
		if err != nil {
			return err
		}
		board = created
		for i, cn := range columns {
			if _, err := q.CreateColumn(ctx, db.CreateColumnParams{
				BoardID:  board.ID,
				Name:     cn,
				Position: int32(i),
			}); err != nil {
				return err
			}
		}
		if _, err := s.emitEvent(ctx, q, "board.created", agent, workspaceID, board.ID, "board", board, nil); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return BoardSnapshot{}, s.normalize(err, "board")
	}
	return s.GetBoardSnapshot(ctx, board.ID, false)
}

func (s *Service) ListBoards(ctx context.Context, workspaceID string) ([]db.Board, error) {
	if _, err := s.q.GetWorkspace(ctx, workspaceID); isNoRows(err) {
		return nil, notFound("workspace %s does not exist", workspaceID)
	} else if err != nil {
		return nil, s.normalize(err, "workspace")
	}
	boards, err := s.q.ListBoards(ctx, workspaceID)
	return unnull(boards), err
}

// GetBoardSnapshot returns the full board payload (columns with item_count +
// items). Archived items are excluded unless includeArchived is true (archiving
// hides an item from default listings without cascading to its children or
// relations).
func (s *Service) GetBoardSnapshot(ctx context.Context, boardID string, includeArchived bool) (BoardSnapshot, error) {
	return s.boardSnapshot(ctx, boardID, includeArchived, true, nil)
}

// GetBoardSnapshotWithoutItems returns the lightweight board payload used by
// GET /api/boards/{id}?include_items=false: columns still carry item_count but
// items is empty and the items table is never walked.
func (s *Service) GetBoardSnapshotWithoutItems(ctx context.Context, boardID string, includeArchived bool) (BoardSnapshot, error) {
	return s.boardSnapshot(ctx, boardID, includeArchived, false, nil)
}

// GetBoardSnapshotWithQuery returns the board payload filtered by a parsed
// search query (PB-57 searched-view seam). The transport hands over the
// expression from search.Parse (parsed ONCE per request upstream of the
// service; a parse failure never reaches here), the service filters the full
// in-memory snapshot. Behavior (spec §7.1 + the orchestrator's searched-view
// contract):
//   - items are the snapshot's items (order preserved) filtered in memory;
//   - per-column item_count is derived from the FILTERED set;
//   - archived handling is unchanged (excluded unless includeArchived);
//   - column keyset pagination is not involved: transports only add q to the
//     board-snapshot path, and a queried view always includes the items array.
//
// A nil expr (or a MatchAll expression from an empty query) yields the same
// result as GetBoardSnapshot.
func (s *Service) GetBoardSnapshotWithQuery(ctx context.Context, boardID string, includeArchived bool, expr search.Expr) (BoardSnapshot, error) {
	return s.boardSnapshot(ctx, boardID, includeArchived, true, expr)
}

func (s *Service) boardSnapshot(ctx context.Context, boardID string, includeArchived, includeItems bool, expr search.Expr) (BoardSnapshot, error) {
	board, err := s.q.GetBoard(ctx, boardID)
	if isNoRows(err) {
		return BoardSnapshot{}, notFound("board %s does not exist", boardID)
	}
	if err != nil {
		return BoardSnapshot{}, s.normalize(err, "board")
	}
	cols, err := s.q.ListColumns(ctx, boardID)
	if err != nil {
		return BoardSnapshot{}, err
	}
	// With an active query the counts derive from the filtered set, so the
	// SQL counts are not needed (and would be misleading); nil expr keeps the
	// byte-for-byte snapshot path.
	var counts map[string]int64
	if expr == nil {
		countRows, err := s.q.BoardColumnItemCounts(ctx, db.BoardColumnItemCountsParams{BoardID: boardID, IncludeArchived: &includeArchived})
		if err != nil {
			return BoardSnapshot{}, err
		}
		counts = make(map[string]int64, len(countRows))
		for _, row := range countRows {
			counts[row.ColumnID] = row.ItemCount
		}
	}
	items := []ItemView{}
	if includeItems {
		rows, err := s.q.BoardItems(ctx, db.BoardItemsParams{BoardID: boardID, IncludeArchived: &includeArchived})
		if err != nil {
			return BoardSnapshot{}, err
		}
		items = make([]ItemView, 0, len(rows))
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		// PB-118: batch the recursive subtree counts for the whole snapshot.
		desc, err := descendantCountsForItemsTx(ctx, s.q, ids)
		if err != nil {
			return BoardSnapshot{}, err
		}
		for _, row := range rows {
			labels, err := decodeLabels(row.Labels)
			if err != nil {
				return BoardSnapshot{}, err
			}
			items = append(items, itemToView(itemRow(row), labels, childCounts{
				Closed: int(row.ClosedChildren),
				Total:  int(row.TotalChildren),
				// PB-122: activity_at was computed in the row query itself.
				LatestActivityAt: row.ActivityAt,
			}.withDescendants(desc[row.ID])))
		}
	}
	if expr != nil {
		items = filterItems(items, expr)
		counts = make(map[string]int64, len(cols))
		for _, it := range items {
			counts[it.ColumnID]++
		}
	}
	columns := make([]ColumnView, 0, len(cols))
	for _, c := range cols {
		columns = append(columns, ColumnView{ID: c.ID, BoardID: c.BoardID, Name: c.Name, Position: c.Position, ItemCount: counts[c.ID]})
	}
	return BoardSnapshot{Board: board, Columns: columns, Items: items}, nil
}

// filterItems evaluates the parsed query against every snapshot item in
// memory, preserving board order (spec §7.1: search never reorders). now is
// fixed once per request (§4.1) — one clock read for the whole evaluation.
// The evaluator panics only on a malformed AST (a parser bug), never on user
// input; parse errors are rejected in search.Parse before the service sees an
// expression.
func filterItems(items []ItemView, expr search.Expr) []ItemView {
	now := timeNow().UTC()
	filtered := make([]ItemView, 0, len(items))
	for _, it := range items {
		if search.Evaluate(expr, searchItemOf(it), now) {
			filtered = append(filtered, it)
		}
	}
	return filtered
}

// searchItemOf adapts an ItemView to the transport-agnostic search.Item
// (eng-eval's evaluator struct). Nullability mapping per spec §5.6: a missing
// owner is "", labels are name-only (nil and empty are equivalent for the
// evaluator), and priority is stored NOT NULL DEFAULT 2, so the store value is
// passed through unchanged — the default lives in the schema, not here.
func searchItemOf(v ItemView) search.Item {
	owner := ""
	if v.OwnerAgent != nil {
		owner = *v.OwnerAgent
	}
	labels := make([]string, 0, len(v.Labels))
	for _, l := range v.Labels {
		labels = append(labels, l.Name)
	}
	return search.Item{
		Key:        v.Key,
		Title:      v.Title,
		Kind:       v.Kind,
		Priority:   float64(v.Priority),
		OwnerAgent: owner,
		Closed:     v.Closed,
		CreatedAt:  v.CreatedAt,
		UpdatedAt:  v.UpdatedAt,
		Labels:     labels,
	}
}

// itemRow adapts a BoardItemsRow into a db.Item for itemToView.
func itemRow(r db.BoardItemsRow) db.Item {
	return db.Item{ID: r.ID, BoardID: r.BoardID, ColumnID: r.ColumnID, Key: r.Key, Title: r.Title,
		Description: r.Description, OwnerAgent: r.OwnerAgent, Priority: r.Priority, Kind: r.Kind,
		ParentItemID: r.ParentItemID, Position: r.Position,
		CreatedBy: r.CreatedBy, ClosedAt: r.ClosedAt, ArchivedAt: r.ArchivedAt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// --- columns ---

func (s *Service) CreateColumn(ctx context.Context, boardID, name string, position *int, agent string) (db.Column, error) {
	if strings.TrimSpace(name) == "" {
		return db.Column{}, invalid("name is required")
	}
	if _, err := s.q.GetBoard(ctx, boardID); isNoRows(err) {
		return db.Column{}, notFound("board %s does not exist", boardID)
	} else if err != nil {
		return db.Column{}, s.normalize(err, "board")
	}
	var pos int
	if position != nil {
		if *position < 0 {
			return db.Column{}, invalid("position must be >= 0")
		}
		pos = *position
	} else {
		maxPos, err := s.q.MaxColumnPosition(ctx, boardID)
		if err != nil {
			return db.Column{}, err
		}
		if maxPos != nil {
			pos = int(*maxPos) + 1
		}
	}
	var col db.Column
	err := s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		created, err := q.CreateColumn(ctx, db.CreateColumnParams{BoardID: boardID, Name: name, Position: int32(pos)})
		if err != nil {
			return nil, err
		}
		col = created
		return []eventSpec{{typ: "column.created", agent: agent, workspaceID: "", boardID: boardID, entityKey: "column", entity: col}}, nil
	})
	return col, s.normalize(err, "column name")
}

func (s *Service) UpdateColumn(ctx context.Context, id string, name *string, position *int, agent string) (db.Column, error) {
	col, err := s.q.GetColumn(ctx, id)
	if isNoRows(err) {
		return db.Column{}, notFound("column %s does not exist", id)
	}
	if err != nil {
		return db.Column{}, err
	}
	if name != nil && strings.TrimSpace(*name) == "" {
		return db.Column{}, invalid("name cannot be empty")
	}
	err = s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		updated, err := q.UpdateColumn(ctx, db.UpdateColumnParams{
			ID:       col.ID,
			Name:     name,
			Position: posParam(position),
		})
		if err != nil {
			return nil, err
		}
		col = updated
		return []eventSpec{{typ: "column.updated", agent: agent, boardID: col.BoardID, entityKey: "column", entity: col}}, nil
	})
	return col, s.normalize(err, "column name")
}

// MoveColumn reorders a column within its board: index 0 = first (mirrors
// move_item semantics); an index past the end clamps to the end. The whole
// reorder runs in one tx: positions are renumbered densely 0..n-1, so the
// event carries the full sibling list (every column's position may change).
func (s *Service) MoveColumn(ctx context.Context, id string, index *int, agent string) (db.Column, error) {
	col, err := s.q.GetColumn(ctx, id)
	if isNoRows(err) {
		return db.Column{}, notFound("column %s does not exist", id)
	}
	if err != nil {
		return db.Column{}, err
	}
	if index != nil && *index < 0 {
		return db.Column{}, invalid("index must be >= 0")
	}
	err = s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		// Serialize same-board renumbers: concurrent MoveColumn txs would
		// UPDATE the board's column rows in different orders (new-position
		// order differs per move) → Postgres 40P01 deadlock and half-applied
		// renumbers. Taking the board row lock FIRST puts every tx in the
		// same lock order; the renumber loop then never races.
		if _, err := q.LockBoard(ctx, col.BoardID); err != nil {
			return nil, err
		}
		cols, err := q.ListColumns(ctx, col.BoardID)
		if err != nil {
			return nil, err
		}
		// Use the FRESH inside-lock row for the moved column, not the pre-lock
		// GetColumn read: a concurrent same-board move may have renumbered it
		// after our outer read, and reinserting the stale struct would carry an
		// outdated Position — the renumber loop's `Position == i → skip` check
		// then skips a needed write (duplicate + gap in final positions).
		fresh := col
		for _, c := range cols {
			if c.ID == col.ID {
				fresh = c
				break
			}
		}
		col = fresh
		// Build the new order: drop the moved column, reinsert at the clamped
		// index, then renumber 0..n-1.
		order := make([]db.Column, 0, len(cols))
		for _, c := range cols {
			if c.ID != col.ID {
				order = append(order, c)
			}
		}
		at := derefInt(index)
		if at < 0 {
			at = 0
		}
		if at > len(order) {
			at = len(order)
		}
		rest := append([]db.Column(nil), order[at:]...)
		order = append(order[:at:at], col)
		order = append(order, rest...)
		for i := range order {
			if order[i].Position == int32(i) {
				continue
			}
			at2 := i
			updated, err := q.UpdateColumn(ctx, db.UpdateColumnParams{ID: order[i].ID, Position: posParam(&at2)})
			if err != nil {
				return nil, err
			}
			order[i] = updated
			if order[i].ID == col.ID {
				col = updated
			}
		}
		return []eventSpec{{typ: "column.moved", agent: agent, boardID: col.BoardID, entityKey: "column", entity: col, extra: map[string]any{"columns": order}}}, nil
	})
	return col, s.normalize(err, "column")
}

func (s *Service) DeleteColumn(ctx context.Context, id string, agent string) error {
	col, err := s.q.GetColumn(ctx, id)
	if isNoRows(err) {
		return notFound("column %s does not exist", id)
	}
	if err != nil {
		return err
	}
	// Guarded delete closes the holds-items TOCTOU: 409 when items exist.
	return s.withEventTx(ctx, func(q *db.Queries) ([]eventSpec, error) {
		n, err := q.DeleteColumnGuard(ctx, pgUUIDorNull(id))
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, conflict("column holds items")
		}
		return []eventSpec{{typ: "column.deleted", agent: agent, boardID: col.BoardID, entityKey: "column", entity: col}}, nil
	})
}

// --- helpers ---

// normalize maps Postgres unique-violation errors to ErrConflict, tagging the
// message with what collided.
func (s *Service) normalize(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return conflict("duplicate %s", what)
		case "22P02": // invalid_text_representation (e.g. malformed uuid)
			return invalid("malformed id for %s", what)
		case "40P01": // deadlock_detected — retryable contention, not a 500
			return conflict("%s contention, retry", what)
		}
	}
	return err
}

// BoardWorkspaceID returns the workspace of a board (404 when missing).
func (s *Service) BoardWorkspaceID(ctx context.Context, boardID string) (string, error) {
	board, err := s.q.GetBoard(ctx, boardID)
	if isNoRows(err) {
		return "", notFound("board %s does not exist", boardID)
	}
	if err != nil {
		return "", s.normalize(err, "board")
	}
	return board.WorkspaceID, nil
}

// Ping verifies database connectivity (used by the healthz endpoint).
func (s *Service) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// withTx runs fn in a single transaction.
func (s *Service) withTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(db.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// boardNameInitialism derives a board's key namespace from its name: the
// uppercase first letter of each alphanumeric word, capped at 5 chars (e.g.
// "Project Board" -> "PB"). Names with no usable letters fall back to "B".
func boardNameInitialism(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var b strings.Builder
	for _, w := range words {
		r := unicode.ToUpper(rune(w[0]))
		if r < 'A' || r > 'Z' {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 5 {
			break
		}
	}
	if b.Len() == 0 {
		return "B"
	}
	return b.String()
}

// allocBoardPrefix picks a globally unique key prefix for a new board: the
// name initialism, with a numeric suffix on collision ("PB", "PB2", ...).
// The unique index on boards.key_prefix is the final guard against a race; the
// check here keeps the common case a single deterministic allocation.
func allocBoardPrefix(ctx context.Context, q *db.Queries, name string) (string, error) {
	base := boardNameInitialism(name)
	for i := 0; ; i++ {
		candidate := base
		if i > 0 {
			suffix := strconv.Itoa(i + 1)
			keep := 5 - len(suffix)
			if keep < 1 {
				keep = 1
			}
			if len(base) > keep {
				base = base[:keep]
			}
			candidate = base + suffix
		}
		exists, err := q.BoardKeyPrefixExists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		if i >= 1000 {
			return "", errBoardPrefixExhausted(base)
		}
	}
}

// errBoardPrefixExhausted is returned when allocBoardPrefix exhausts its
// candidate loop. The message names the real cause (a saturated prefix space,
// usually stale test boards) and the remedy, instead of a bare 409.
func errBoardPrefixExhausted(base string) error {
	return conflict("could not allocate a unique board key prefix: no free candidate in the %q-prefixed space after 1000 tries (base, base2, …, base1000) — the boards table likely holds many stale boards (accumulated test runs). Remedy: in a test environment run `make reset-test-db`; otherwise clean up unused boards", base)
}

// NormalizeAgent maps an absent or blank agent to "human", the default actor
// recorded on mutations by both the REST and MCP transports.
func NormalizeAgent(a string) string {
	if strings.TrimSpace(a) == "" {
		return "human"
	}
	return a
}

// NormalizeAgentPtr is NormalizeAgent for the REST transport's optional
// "agent" field, where absence and blank are equivalent.
func NormalizeAgentPtr(a *string) string {
	if a == nil {
		return NormalizeAgent("")
	}
	return NormalizeAgent(*a)
}

// posParam maps *int to a nullable int32 param.
func posParam(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

// timeNow is a var for testability.
var timeNow = time.Now

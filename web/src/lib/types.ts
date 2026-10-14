// Types for the api-first-kanban API v1 contract.
//
// These mirror the agreed contract exactly. If the backend ever disagrees with
// this file, the contract is the source of truth — raise the drift, do not
// silently accommodate it here.
//
// Responses may carry additional fields beyond those declared (the server is
// additive, e.g. `board_id` on items, `board_id` on snapshot columns).

export interface Workspace {
  id: string
  name: string
  description: string | null
  created_at: string
}

export interface Board {
  id: string
  workspace_id: string
  name: string
  /**
   * Immutable item-key namespace chosen when the board is created (e.g. "PB");
   * item keys are `<key_prefix>-<sequence>`. Additive to the contract.
   */
  key_prefix: string
  created_at: string
}

export interface Column {
  id: string
  name: string
  position: number
  /**
   * Live (non-archived) item count for the column. Present on the board
   * snapshot (both with and without items); absent on column.created/updated
   * SSE events, which is why it is optional.
   */
  item_count?: number
}

export interface Label {
  id: string
  workspace_id: string
  name: string
  color: string
  /** Link count; present only on ?top=/?q= list responses (PB-73 contract). */
  usage_count?: number
}

/**
 * Hierarchy node type. Milestone > Epic > Task; the kind/parent-kind rule
 * enforces the three-level cap, so cycles are impossible by construction.
 */
export type ItemKind = 'milestone' | 'epic' | 'task'

export interface Item {
  id: string
  /** Additive beyond the contract: the server also sends the owning board. */
  board_id: string
  column_id: string
  /**
   * Stable, globally unique human key `<board key_prefix>-<sequence>` (e.g.
   * "PB-123"). Composed at create; immutable across move/patch/archive/transfer.
   */
  key: string
  title: string
  description: string | null
  owner_agent: string | null
  /** Integer enum 0..3: 0=P0 blocker, 1=P1 high, 2=P2 normal (default), 3=P3 low. */
  priority: number
  /** Hierarchy node type (default `task`). */
  kind: ItemKind
  /** Owning parent item id, or null for a top-level item. */
  parent_item_id: string | null
  /**
   * Fractional ordering value as an exact decimal STRING (e.g. "1", "0.5").
   * Never sort lexically ("10" < "2"); parse with Number() for ordering at
   * our magnitudes and keep the string for display.
   */
  position: string
  labels: Label[]
  closed: boolean
  /**
   * Derived child progress counts (PB-84): direct, non-archived children;
   * closed = closed_at IS NOT NULL. Archived children are excluded from both
   * numerator and denominator; a parent with no live children reads 0/0 (no
   * indicator). SERVER SINGLE SOURCE (PB-88): never recompute these from a
   * children list client-side — display the server's values as-is.
   */
  closed_children: number
  total_children: number
  /** RFC3339 timestamp while the item is archived; null while it is active. */
  archived_at: string | null
  created_by: string | null
  created_at: string
  updated_at: string
  /** Derived activity timestamp (PB-122): updated_at ∨ newest comment time. */
  activity_at: string
}

export type RelationKind = 'blocks' | 'relates_to' | 'duplicates'

/** Direction is relative to the item the edge was read from. */
export type RelationDirection = 'outgoing' | 'incoming'

/**
 * One directed relation edge. `item` is the item at the OTHER end of the edge:
 * the target when `direction` is `"outgoing"`, the source when `"incoming"`.
 * "blocked by" is the derived incoming view of a `blocks` edge.
 */
export interface Relation {
  id: string
  relation: RelationKind
  direction: RelationDirection
  item: Item
  created_by_agent: string | null
  created_at: string
}

/** An item's edges, split by direction (empty arrays, never null). */
export interface ItemRelations {
  outgoing: Relation[]
  incoming: Relation[]
}

export type AuthorType = 'agent' | 'human'

export interface Comment {
  id: string
  item_id: string
  author_type: AuthorType
  author_name: string
  body: string
  created_at: string
}

/** GET /api/boards/{id} — items is empty when requested with include_items=false. */
export interface BoardSnapshot {
  board: Board
  columns: Column[]
  items: Item[]
}

/**
 * GET /api/boards/{id}/items?column_id=&limit=&cursor= — one keyset page of a
 * single column. `next_cursor` is null on the last page (also for an empty
 * column, which returns an empty `items` list).
 */
export interface BoardItemsPage {
  items: Item[]
  next_cursor: string | null
}

/**
 * POST /api/workspaces/{id}/boards returns the created board with its initial
 * columns nested inside it (docs/api.md), not the GET snapshot shape.
 */
export interface CreatedBoard extends Board {
  columns: Column[]
}

/** GET /api/items/{id} */
export interface ItemDetail {
  item: Item
  comments: Comment[]
  relations: ItemRelations
  /**
   * The item's child items (parent_item_id = item.id), oldest first,
   * archived included. Children may sit on other boards of the workspace —
   * their column is then not resolvable from the active board.
   */
  children: Item[]
}

// ---------------------------------------------------------------------------
// Request payloads
// ---------------------------------------------------------------------------

export interface WorkspaceInput {
  name: string
  description?: string
}

export interface BoardInput {
  name: string
  columns?: string[]
}

export interface ColumnCreateInput {
  name: string
  position?: number
}

export interface ColumnPatchInput {
  name?: string
  position?: number
}

export interface ColumnMoveInput {
  /** 0 = first column (server default); past-the-end clamps. */
  index?: number
  agent?: string
}

export interface ItemCreateInput {
  title: string
  description?: string
  column_id?: string
  owner_agent?: string
  priority?: number
  kind?: ItemKind
  parent_item_id?: string | null
  label_ids?: string[]
  agent?: string
}

export interface ItemPatchInput {
  title?: string
  description?: string
  owner_agent?: string
  priority?: number
  kind?: ItemKind
  /**
   * Three-state, matching the API: absent keeps the current parent, a string
   * sets it, an explicit null clears it. Serialize carefully — only send the
   * key when the intent is to change the parent.
   */
  parent_item_id?: string | null
  label_ids?: string[]
  closed?: boolean
  agent?: string
}

export interface MoveInput {
  column_id: string
  /** 0 = top of column (server default). */
  index?: number
  agent?: string
}

export interface TransferInput {
  to_agent: string
  note?: string
  agent?: string
}

export interface LabelInput {
  name: string
  color?: string
}

export interface LabelPatchInput {
  name?: string
  color?: string
}

export interface CommentInput {
  body: string
  agent?: string
}

export interface RelationInput {
  to_item_id: string
  relation: RelationKind
  agent?: string
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

/** Body shape for 400/404/409 responses. */
export interface ApiErrorBody {
  error: {
    code: string
    message: string
  }
}

// ---------------------------------------------------------------------------
// SSE
// ---------------------------------------------------------------------------

export type KanbanEventType =
  | 'workspace.created'
  | 'board.created'
  | 'column.created'
  | 'column.updated'
  | 'column.moved'
  | 'column.deleted'
  | 'item.created'
  | 'item.updated'
  | 'item.moved'
  | 'item.transferred'
  | 'item.archived'
  | 'item.unarchived'
  | 'comment.added'
  | 'label.created'
  | 'label.updated'
  | 'label.deleted'
  | 'relation.created'
  | 'relation.deleted'

/**
 * SSE event envelope from GET /api/events.
 *
 * `type`, `board_id`, `agent` and `at` are contractual. The contract describes
 * the remainder as "+ affected entity" without pinning the key per event type,
 * so the known entities below are optional and every consumer must tolerate
 * their absence (see KanbanStore.applyEvent for the fallback strategy).
 */
export interface KanbanEvent {
  type: KanbanEventType | (string & {})
  board_id: string | null
  agent: string
  at: string
  /** Entity id, when the event is about a deletion or carries no full entity. */
  id?: string
  workspace?: Workspace
  board?: Board
  column?: Column
  /** column.moved carries the full reordered sibling list. */
  columns?: Column[]
  item?: Item
  label?: Label
  comment?: Comment
  relation?: Relation
  [key: string]: unknown
}

export interface Health {
  status: string
  db: boolean
  /** Build identity: the VCS revision (optionally suffixed "-dirty"), or "unknown". */
  commit: string
}

-- name: GetItem :one
SELECT * FROM items WHERE id = $1;

-- name: ItemLabels :many
SELECT l.id, l.workspace_id, l.name, l.color
FROM item_labels il
JOIN labels l ON l.id = il.label_id
WHERE il.item_id = $1
ORDER BY l.name;

-- name: BoardItems :many
SELECT i.*,
       -- Child progress counts (PB-84): direct, non-archived children. The
       -- subselect deliberately does NOT filter children by board — the parent
       -- rule is workspace-scoped, so cross-board children count.
       (SELECT count(*) FROM items c
        WHERE c.parent_item_id = i.id AND c.archived_at IS NULL
          AND c.closed_at IS NOT NULL) AS closed_children,
       (SELECT count(*) FROM items c
        WHERE c.parent_item_id = i.id AND c.archived_at IS NULL) AS total_children,
       -- Derived activity timestamp (PB-122): item.updated_at vs the newest
       -- comment on the item. AddComment deliberately does not touch
       -- updated_at, so the recency lives here. greatest() ignores NULLs, so
       -- a comment-less item yields i.updated_at.
       greatest(i.updated_at,
                (SELECT max(c.created_at) FROM comments c WHERE c.item_id = i.id)
       ) AS activity_at,
       coalesce(
           json_agg(json_build_object('id', l.id, 'workspace_id', l.workspace_id,
                                      'name', l.name, 'color', l.color)
                    ORDER BY l.name) FILTER (WHERE l.id IS NOT NULL),
           '[]'::json) AS labels
FROM items i
LEFT JOIN item_labels il ON il.item_id = i.id
LEFT JOIN labels l ON l.id = il.label_id
WHERE i.board_id = $1
  AND (sqlc.narg('include_archived')::boolean OR i.archived_at IS NULL)
GROUP BY i.id
ORDER BY (SELECT c.position FROM columns c WHERE c.id = i.column_id), i.position, i.created_at;

-- name: ColumnItemsPage :many
-- Keyset page of one column, ordered by (position, id). after_position and
-- after_id carry the previous page's last (position, id); NULL means "first
-- page". Labels are grouped exactly like BoardItems so the page rows map onto
-- the same ItemView shape.
SELECT i.id, i.board_id, i.column_id, i.key, i.title, i.description, i.owner_agent,
       i.priority, i.kind, i.parent_item_id, i.position, i.created_by,
       i.closed_at, i.archived_at, i.created_at, i.updated_at,
       -- Derived activity timestamp (PB-122), identical shape to BoardItems.
       greatest(i.updated_at,
                (SELECT max(c.created_at) FROM comments c WHERE c.item_id = i.id)
       ) AS activity_at,
       -- Child progress counts (PB-84), identical shape to BoardItems.
       (SELECT count(*) FROM items c
        WHERE c.parent_item_id = i.id AND c.archived_at IS NULL
          AND c.closed_at IS NOT NULL) AS closed_children,
       (SELECT count(*) FROM items c
        WHERE c.parent_item_id = i.id AND c.archived_at IS NULL) AS total_children,
       coalesce(
           json_agg(json_build_object('id', l.id, 'workspace_id', l.workspace_id,
                                      'name', l.name, 'color', l.color)
                    ORDER BY l.name) FILTER (WHERE l.id IS NOT NULL),
           '[]'::json) AS labels
FROM items i
LEFT JOIN item_labels il ON il.item_id = i.id
LEFT JOIN labels l ON l.id = il.label_id
WHERE i.column_id = $1
  AND (sqlc.narg('include_archived')::boolean OR i.archived_at IS NULL)
  AND (
    sqlc.narg('after_position')::numeric IS NULL
    OR i.position > sqlc.narg('after_position')::numeric
    OR (i.position = sqlc.narg('after_position')::numeric AND i.id > sqlc.narg('after_id')::uuid)
  )
GROUP BY i.id
ORDER BY i.position, i.id
LIMIT $2;

-- name: ColumnItemPositions :many
SELECT position FROM items WHERE column_id = $1 ORDER BY position;

-- name: ChildrenOfItem :many
SELECT * FROM items WHERE parent_item_id = $1 ORDER BY created_at;

-- name: ChildCountsForItem :one
-- Child progress counts (PB-84) for one parent: direct children only,
-- archived children excluded from BOTH numerator and denominator (an empty
-- parent reads 0/0 — no children, no indicator). Cross-board children count
-- (the parent rule is workspace-scoped).
SELECT count(*) FILTER (WHERE closed_at IS NOT NULL) AS closed_children,
       count(*) AS total_children
FROM items
WHERE parent_item_id = $1 AND archived_at IS NULL;

-- name: ChildCountsForItemsBatch :many
-- Batched child progress counts (PB-84) for the item-detail children read:
-- one GROUP BY instead of a count query per child row. Parents with no
-- non-archived children are absent from the result (0/0 by construction).
SELECT parent_item_id,
       count(*) FILTER (WHERE closed_at IS NOT NULL) AS closed_children,
       count(*) AS total_children
FROM items
WHERE parent_item_id = ANY(sqlc.arg('parent_ids')::uuid[]) AND archived_at IS NULL
GROUP BY parent_item_id;

-- name: DescendantCountsForItemsBatch :many
-- Recursive subtree progress counts (PB-118) for one or more parents: ALL
-- non-archived descendants at any depth (milestone -> epic -> task), closed =
-- closed_at IS NOT NULL. Mirrors ChildCountsForItemsBatch: archived
-- descendants are excluded from BOTH numerator and denominator (consistent
-- with the direct-count rule); the parent itself is never counted; parents
-- with no non-archived descendants are absent from the result (0/0 by
-- construction). Descendants may sit on another board — the parent rule is
-- workspace-scoped, so no board filter applies.
WITH RECURSIVE subtree AS (
  SELECT i.id AS ancestor_id, d.id, d.closed_at
  FROM items i
  JOIN items d ON d.parent_item_id = i.id AND d.archived_at IS NULL
  WHERE i.id = ANY(sqlc.arg('parent_ids')::uuid[])
  UNION ALL
  SELECT s.ancestor_id, g.id, g.closed_at
  FROM subtree s
  JOIN items g ON g.parent_item_id = s.id AND g.archived_at IS NULL
)
SELECT ancestor_id,
       count(*) FILTER (WHERE closed_at IS NOT NULL) AS closed_descendants,
       count(*) AS total_descendants
FROM subtree
GROUP BY ancestor_id;

-- name: LatestCommentAtForItem :one
-- Latest comment timestamp (PB-122) for a single item, NULL when the item has
-- no comments. Used by the single-item response/event plumbing so activity_at
-- (greatest with item.updated_at) is correct without a comments roundtrip per
-- call site. Mirrors the ChildCountsForItem batching pattern.
SELECT max(created_at) AS latest_comment_at
FROM comments
WHERE item_id = $1;

-- name: LatestCommentAtForItemsBatch :many
-- Batched latest comment timestamps (PB-122) for the item-detail children
-- read: one GROUP BY instead of a query per child row. Items with no
-- comments are absent from the result (NULL latest_comment_at by
-- construction).
SELECT item_id, max(created_at) AS latest_comment_at
FROM comments
WHERE item_id = ANY(sqlc.arg('item_ids')::uuid[])
GROUP BY item_id;

-- name: ColumnFirstByPosition :one
SELECT id FROM columns WHERE board_id = $1 ORDER BY position LIMIT 1;

-- name: CreateItem :one
INSERT INTO items (board_id, column_id, title, description, key, owner_agent, priority, kind, parent_item_id, position, created_by)
VALUES ($1, $2, $3, $4, $5, sqlc.narg('owner_agent'), coalesce(sqlc.narg('priority'), 2::smallint), coalesce(sqlc.narg('kind'), 'task'::text), sqlc.narg('parent_item_id'), sqlc.narg('position'), $11)
RETURNING *;

-- name: UpdateItemHierarchy :one
UPDATE items
SET kind = $2, parent_item_id = $3
WHERE id = $1
RETURNING *;

-- name: UpdateItemFields :one
UPDATE items
SET
  title = coalesce(sqlc.narg('title'), title),
  description = coalesce(sqlc.narg('description'), description),
  owner_agent = coalesce(sqlc.narg('owner_agent'), owner_agent),
  priority = coalesce(sqlc.narg('priority'), priority),
  closed_at = coalesce(sqlc.narg('closed_at'), closed_at)
WHERE id = $1
RETURNING *;

-- name: ReopenItem :one
UPDATE items SET closed_at = NULL WHERE id = $1 AND closed_at IS NOT NULL RETURNING *;

-- name: ArchiveItem :one
-- Guarded so a concurrent double-archive returns no row; the service maps
-- that to 409 (it reads the current state first for the 404 vs 409 decision).
UPDATE items SET archived_at = now() WHERE id = $1 AND archived_at IS NULL RETURNING *;

-- name: UnarchiveItem :one
UPDATE items SET archived_at = NULL WHERE id = $1 AND archived_at IS NOT NULL RETURNING *;

-- name: TransferItem :one
UPDATE items
SET owner_agent = $2
WHERE id = $1
RETURNING *;

-- name: MoveItem :one
UPDATE items
SET column_id = $2, position = $3
WHERE id = $1
RETURNING *;

-- name: AddItemLabel :exec
INSERT INTO item_labels (item_id, label_id) VALUES ($1, $2);

-- name: ClearItemLabels :exec
DELETE FROM item_labels WHERE item_id = $1;
-- name: ClearItemDescription :exec
UPDATE items SET description = NULL WHERE id = $1;

-- name: RenumberColumnPositions :exec
WITH ranked AS (
  SELECT id, (row_number() OVER (ORDER BY position, id))::numeric AS rn
  FROM items
  WHERE column_id = $1
)
UPDATE items i SET position = ranked.rn FROM ranked WHERE i.id = ranked.id;

-- name: DuplicatePositionCount :one
SELECT count(*) FROM items WHERE column_id = $1 AND position = sqlc.arg('position');

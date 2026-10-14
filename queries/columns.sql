-- name: ListColumns :many
SELECT id, board_id, name, position
FROM columns
WHERE board_id = $1
ORDER BY position;

-- name: GetColumn :one
SELECT id, board_id, name, position
FROM columns
WHERE id = $1;

-- name: CreateColumn :one
INSERT INTO columns (board_id, name, position)
VALUES ($1, $2, $3)
RETURNING id, board_id, name, position;

-- name: ColumnNameExists :one
SELECT EXISTS(SELECT 1 FROM columns WHERE board_id = $1 AND name = $2) AS name_exists;

-- name: MaxColumnPosition :one
SELECT coalesce(max(position), -1) FROM columns WHERE board_id = $1;

-- name: UpdateColumn :one
UPDATE columns
SET
  name = coalesce(sqlc.narg('name'), name),
  position = coalesce(sqlc.narg('position'), position)
WHERE id = $1
RETURNING id, board_id, name, position;

-- name: DeleteColumnGuard :execrows
DELETE FROM columns
WHERE id = $1
  AND NOT EXISTS (SELECT 1 FROM items WHERE column_id = $1);

-- name: ColumnItemCount :one
SELECT count(*) FROM items WHERE column_id = $1;

-- name: BoardColumnItemCounts :many
-- Live item count per column for a board. Mirrors the archive filter of
-- BoardItems/ColumnItemsPage so a column's count equals the full paginated walk.
SELECT c.id AS column_id, count(i.id) AS item_count
FROM columns c
LEFT JOIN items i
  ON i.column_id = c.id
  AND (sqlc.narg('include_archived')::boolean OR i.archived_at IS NULL)
WHERE c.board_id = $1
GROUP BY c.id
ORDER BY c.position;
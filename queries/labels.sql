-- name: ListLabels :many
SELECT *
FROM labels
WHERE workspace_id = $1
ORDER BY name;

-- name: ListLabelsRanked :many
-- Usage-ranked labels for a workspace. usage_count = number of item-label
-- links (all items, archived included). Optional q filters by
-- case-insensitive substring on name BEFORE ranking; ties break by name ASC.
SELECT l.*, COUNT(il.item_id)::bigint AS usage_count
FROM labels l
LEFT JOIN item_labels il ON il.label_id = l.id
WHERE l.workspace_id = $1
  AND (sqlc.narg('q')::text IS NULL OR l.name ILIKE '%' || sqlc.narg('q')::text || '%')
GROUP BY l.id
ORDER BY usage_count DESC, l.name ASC;

-- name: GetLabel :one
SELECT * FROM labels WHERE id = $1;

-- name: CreateLabel :one
INSERT INTO labels (workspace_id, name, color)
VALUES ($1, $2, sqlc.narg('color'))
RETURNING *;

-- name: UpdateLabel :one
UPDATE labels
SET
  name = coalesce(sqlc.narg('name'), name),
  color = coalesce(sqlc.narg('color'), color)
WHERE id = $1
RETURNING *;

-- name: DeleteLabel :exec
DELETE FROM labels WHERE id = $1;
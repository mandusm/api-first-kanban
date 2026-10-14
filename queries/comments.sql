-- name: ListComments :many
SELECT *
FROM comments
WHERE item_id = $1
ORDER BY created_at;

-- name: CreateComment :one
INSERT INTO comments (item_id, author_type, author_name, body)
VALUES ($1, $2, $3, $4)
RETURNING *;
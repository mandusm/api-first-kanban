-- name: CreateRelation :one
INSERT INTO item_relations (from_item_id, to_item_id, relation, created_by_agent)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetRelation :one
SELECT * FROM item_relations WHERE id = $1;

-- name: DeleteRelation :exec
DELETE FROM item_relations WHERE id = $1;

-- name: OutgoingRelations :many
SELECT * FROM item_relations WHERE from_item_id = $1 ORDER BY created_at, id;

-- name: IncomingRelations :many
SELECT * FROM item_relations WHERE to_item_id = $1 ORDER BY created_at, id;

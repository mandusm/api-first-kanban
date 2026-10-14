-- name: ListBoards :many
SELECT b.id, b.workspace_id, b.name, b.key_prefix, b.created_at
FROM boards b
WHERE b.workspace_id = $1
ORDER BY b.created_at, b.name;

-- name: GetBoard :one
SELECT id, workspace_id, name, key_prefix, created_at
FROM boards
WHERE id = $1;

-- name: CreateBoard :one
INSERT INTO boards (workspace_id, name, key_prefix)
VALUES ($1, $2, $3)
RETURNING id, workspace_id, name, key_prefix, created_at;

-- name: BoardWorkspace :one
SELECT workspace_id FROM boards WHERE id = $1;

-- name: BoardNameExists :one
SELECT EXISTS(SELECT 1 FROM boards WHERE workspace_id = $1 AND name = $2) AS name_exists;

-- name: BoardKeyPrefixExists :one
SELECT EXISTS(SELECT 1 FROM boards WHERE key_prefix = $1) AS prefix_exists;
-- name: LockBoard :one
-- FOR UPDATE serialization anchor: concurrent multi-row renumbers (e.g.
-- column move) must take the board row lock FIRST so every tx touches the
-- board's rows in the same lock order (else 40P01 deadlock).
SELECT id FROM boards WHERE id = $1 FOR UPDATE;

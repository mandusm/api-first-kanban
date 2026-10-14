-- name: ListWorkspaces :many
SELECT id, name, description, created_at
FROM workspaces
ORDER BY created_at, name;

-- name: GetWorkspace :one
SELECT id, name, description, created_at
FROM workspaces
WHERE id = $1;

-- name: CreateWorkspace :one
INSERT INTO workspaces (name, description)
VALUES ($1, $2)
RETURNING id, name, description, created_at;

-- name: WorkspaceNameExists :one
SELECT EXISTS(SELECT 1 FROM workspaces WHERE name = $1) AS name_exists;
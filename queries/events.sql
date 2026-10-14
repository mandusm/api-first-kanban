-- name: CreateEvent :one
INSERT INTO events (workspace_id, board_id, type, agent, payload)
VALUES (sqlc.narg('workspace_id'), sqlc.narg('board_id'), sqlc.arg('type'), sqlc.arg('agent'), sqlc.arg('payload'))
RETURNING id;
-- name: NotifyEvent :exec
SELECT pg_notify('kanban_events', sqlc.narg('payload'));

-- name: GetEventByID :one
SELECT workspace_id, board_id, payload
FROM events
WHERE id = $1;

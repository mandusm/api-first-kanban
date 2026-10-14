-- goose Embed Source: migrations/00001_init.sql
-- +goose Up
--
-- Schema design notes:
--   items.position uses fractional midpoint ordering: when inserting between
--   two items, position = midpoint of the neighbours' positions. Duplicate
--   position values are allowed BY DESIGN (uniqueness would break midpoints).
--   Strict, non-duplicate column ordering (e.g. DEFERRABLE UNIQUE) is deferred
--   to M2 and arrives together with the move endpoint.
--   events is append-only by design (audit log of agent/human interactions):
--   a trigger rejects UPDATE and DELETE, and workspace_id/board_id are
--   nullable WITHOUT foreign keys so event history survives entity deletion.
--   gen_random_uuid() is core Postgres 18 (no pgcrypto needed).

CREATE TABLE workspaces (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text UNIQUE NOT NULL,
    description text,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- key_prefix is the immutable human key namespace of a board (e.g. "PB" for
-- "Project Board"); items are keyed <key_prefix>-<per-board sequence> (PB-1).
-- Globally unique so keys never collide across boards/workspaces.
CREATE TABLE boards (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name text NOT NULL,
    key_prefix text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name),
    UNIQUE (key_prefix)
);

-- board_sequences allocates item keys per board. The counter lives in its own
-- table so it never leaks into board REST/SSE payloads; an atomic upsert on the
-- row lock (see NextBoardKeySeq) serializes concurrent creates per board and
-- rolls back with the create tx, so keys are gap-free and race-safe.
CREATE TABLE board_sequences (
    board_id uuid PRIMARY KEY REFERENCES boards(id) ON DELETE CASCADE,
    last_key bigint NOT NULL
);

CREATE TABLE columns (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    name text NOT NULL,
    position int NOT NULL,
    UNIQUE (board_id, name)
);

-- Composite FK: an item column_id must live on the item's own board.
ALTER TABLE columns ADD CONSTRAINT columns_id_board_key UNIQUE (id, board_id);

CREATE TABLE items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id uuid NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    column_id uuid NOT NULL,
    -- key is composed once at create (<board key_prefix>-<seq>) and stored, so
    -- it is a stable reference that survives moves/archive/transfer. Globally
    -- UNIQUE (matches ^[A-Z][A-Z0-9]{0,4}-[1-9][0-9]*$).
    key text NOT NULL,
    title text NOT NULL,
    description text,
    owner_agent text,
    priority smallint NOT NULL DEFAULT 2,
    -- kind + parent_item_id carry the milestone -> epic -> task hierarchy.
    -- Max depth 3 is enforced by the kind/parent-kind rule alone
    -- (milestone has no parent; epic's parent must be a milestone; task's parent
    -- may be an epic or a milestone), so a cycle is impossible by construction.
    kind text NOT NULL DEFAULT 'task' CHECK (kind IN ('milestone', 'epic', 'task')),
    parent_item_id uuid REFERENCES items(id),
    position numeric NOT NULL,
    created_by text,
    closed_at timestamptz,
    -- archived_at is archive state, independent of closed: NULL = active,
    -- non-NULL = archived. Archived items are hidden from default listings
    -- (board snapshot + paginated items) and returned only with
    -- include_archived=true. Archiving never cascades to children/relations.
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (key)
);

ALTER TABLE items ADD CONSTRAINT items_column_board_fkey
    FOREIGN KEY (column_id, board_id) REFERENCES columns(id, board_id)
    ON DELETE CASCADE;

CREATE TABLE labels (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name text NOT NULL,
    color text NOT NULL DEFAULT '#6366f1',
    UNIQUE (workspace_id, name)
);

CREATE TABLE item_labels (
    item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    label_id uuid NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, label_id)
);

CREATE TABLE comments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    author_type text NOT NULL CHECK (author_type IN ('agent', 'human')),
    author_name text NOT NULL,
    body text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- item_relations links two items (relations are directed: A blocks B means B
-- is blocked by A; "blocked by" is the derived INCOMING view, never a stored
-- relation value). Rows must be within one workspace — enforced in the service
-- layer via the board join. The UNIQUE key makes an exact duplicate a 409.
CREATE TABLE item_relations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    from_item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    to_item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    relation text NOT NULL CHECK (relation IN ('blocks', 'relates_to', 'duplicates')),
    created_by_agent text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (from_item_id, to_item_id, relation),
    CHECK (from_item_id <> to_item_id)
);

CREATE TABLE events (
    id bigserial PRIMARY KEY,
    workspace_id uuid,
    board_id uuid,
    type text NOT NULL,
    agent text NOT NULL DEFAULT 'human',
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Append-only audit log: reject mutations; rows survive entity deletion
-- (workspace_id/board_id intentionally carry no foreign keys).
CREATE FUNCTION events_no_mutation()
RETURNS trigger AS 'BEGIN RAISE EXCEPTION ''events is append-only''; END'
LANGUAGE plpgsql;

CREATE TRIGGER events_no_update
BEFORE UPDATE ON events
FOR EACH ROW EXECUTE FUNCTION events_no_mutation();

CREATE TRIGGER events_no_delete
BEFORE DELETE ON events
FOR EACH ROW EXECUTE FUNCTION events_no_mutation();

CREATE INDEX idx_items_board ON items(board_id);
CREATE INDEX idx_items_column ON items(column_id);
CREATE INDEX idx_events_workspace_created ON events(workspace_id, created_at);

CREATE FUNCTION touch_updated_at()
RETURNS trigger AS 'BEGIN NEW.updated_at = now(); RETURN NEW; END'
LANGUAGE plpgsql;

CREATE TRIGGER items_touch_updated_at
BEFORE UPDATE ON items
FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- +goose Down
DROP TRIGGER IF EXISTS items_touch_updated_at ON items;
DROP FUNCTION IF EXISTS touch_updated_at();
DROP TRIGGER IF EXISTS events_no_update ON events;
DROP TRIGGER IF EXISTS events_no_delete ON events;
DROP FUNCTION IF EXISTS events_no_mutation();
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS item_relations;
DROP TABLE IF EXISTS comments;
DROP TABLE IF EXISTS item_labels;
DROP TABLE IF EXISTS labels;
DROP TABLE IF EXISTS items;
DROP TABLE IF EXISTS board_sequences;
DROP TABLE IF EXISTS columns;
DROP TABLE IF EXISTS boards;
DROP TABLE IF EXISTS workspaces;
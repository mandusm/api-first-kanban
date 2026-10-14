-- Live delta for migration 00001 (unique item keys). Idempotent: safe to re-run.
-- The dev DB backs the live coordination board and is never reset, so schema
-- changes ship as an in-place edit to migrations/00001_init.sql PLUS this
-- byte-stable delta applied to the live database (goose version stays frozen).
--
-- Apply with:
--   podman exec -i api-first-kanban-db psql -U kanban -d kanban \
--     -v ON_ERROR_STOP=1 < scripts/live-delta-00001-unique-item-keys.sql

-- 1. New columns, added nullable first so existing rows survive the backfill.
ALTER TABLE boards ADD COLUMN IF NOT EXISTS key_prefix text;
ALTER TABLE items ADD COLUMN IF NOT EXISTS key text;

-- 2. Per-board key sequence table.
CREATE TABLE IF NOT EXISTS board_sequences (
    board_id uuid PRIMARY KEY REFERENCES boards(id) ON DELETE CASCADE,
    last_key bigint NOT NULL
);

-- 3. Backfill boards.key_prefix from the board name: uppercase initialism of the
--    name's alphanumeric words, capped at 5 chars, numeric suffix on collision.
--    Only NULL rows are touched, so a re-run is a no-op.
WITH derived AS (
    SELECT b.id,
           left(
               nullif(
                   regexp_replace(
                       upper(coalesce(string_agg(left(t.w, 1), '' ORDER BY t.ord), '')),
                       '[^A-Z0-9]', '', 'g'),
                   ''),
               5) AS base
    FROM boards b
    LEFT JOIN LATERAL regexp_split_to_table(b.name, '[^A-Za-z0-9]+')
        WITH ORDINALITY AS t(w, ord) ON true
    GROUP BY b.id
),
deduped AS (
    SELECT id,
           coalesce(base, 'B') AS base,
           row_number() OVER (PARTITION BY coalesce(base, 'B') ORDER BY id) AS rn
    FROM derived
)
UPDATE boards b
SET key_prefix = CASE
        WHEN d.rn = 1 THEN d.base
        ELSE left(d.base, greatest(1, 5 - length(d.rn::text))) || d.rn::text
    END
FROM deduped d
WHERE b.id = d.id AND b.key_prefix IS NULL;

-- 4. Enforce NOT NULL + uniqueness (re-running SET NOT NULL / CREATE INDEX
--    IF NOT EXISTS is a no-op).
ALTER TABLE boards ALTER COLUMN key_prefix SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS boards_key_prefix_key ON boards (key_prefix);

-- 5. Backfill items.key = <prefix>-<row_number within board>. Ordered by
--    created_at, id so the numbering is deterministic; only NULL rows are set.
WITH numbered AS (
    SELECT i.id,
           i.board_id,
           row_number() OVER (PARTITION BY i.board_id ORDER BY i.created_at, i.id) AS rn
    FROM items i
    WHERE i.key IS NULL
)
UPDATE items i
SET key = b.key_prefix || '-' || n.rn::text
FROM numbered n
JOIN boards b ON b.id = n.board_id
WHERE i.id = n.id AND i.key IS NULL;

ALTER TABLE items ALTER COLUMN key SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS items_key_key ON items (key);

-- 6. Seed each board's sequence with its current item count so the next created
--    item continues after the backfilled range. DO NOTHING keeps re-runs a no-op.
INSERT INTO board_sequences (board_id, last_key)
SELECT b.id, count(i.id)
FROM boards b
LEFT JOIN items i ON i.board_id = b.id
GROUP BY b.id
ON CONFLICT (board_id) DO NOTHING;

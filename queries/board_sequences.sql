-- name: NextBoardKeySeq :one
-- Allocate the next per-board item key sequence. The INSERT ... ON CONFLICT
-- upsert takes a row lock on the board's counter, so concurrent creates on the
-- same board serialize here (one atomic statement, no read-modify-write race);
-- the increment rolls back with the caller's create tx, so keys are gap-free.
INSERT INTO board_sequences (board_id, last_key)
VALUES ($1, 1)
ON CONFLICT (board_id) DO UPDATE SET last_key = board_sequences.last_key + 1
RETURNING last_key;

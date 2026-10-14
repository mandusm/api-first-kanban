-- +goose Up
-- idx_items_parent backs the child progress counts (PB-84): every ItemView
-- read path counts direct non-archived children via a correlated subselect on
-- items(parent_item_id), and the parent/child hierarchy reads walk the same
-- column. 00001 only indexes board/column.
CREATE INDEX idx_items_parent ON items(parent_item_id);

-- +goose Down
DROP INDEX idx_items_parent;

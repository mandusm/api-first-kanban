package main

import (
	"context"
	"testing"

	"api-first-kanban/internal/testutil"
)

// TestSchemaComplete asserts the expected v1 schema exists after migrations:
// 8 tables, the items updated_at trigger, and the composite column FK.
func TestSchemaComplete(t *testing.T) {
	ctx := context.Background()
	db := testutil.OpenPool(t)

	want := map[string]bool{
		"workspaces": false, "boards": false, "columns": false,
		"items": false, "labels": false, "item_labels": false,
		"comments": false, "item_relations": false, "events": false,
	}
	rows, err := db.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	rows.Close()
	for name, found := range want {
		if !found {
			t.Errorf("table %q missing", name)
		}
	}

	// items updated_at trigger must exist.
	var triggerCount int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM pg_trigger WHERE tgrelid = 'items'::regclass AND NOT tgisinternal`,
	).Scan(&triggerCount); err != nil {
		t.Fatalf("query triggers: %v", err)
	}
	if triggerCount == 0 {
		t.Error("trigger on items table missing")
	}

	// Composite FK: items(column_id, board_id) must reference columns(id, board_id).
	var fkCount int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM pg_constraint
		 WHERE conname = 'items_column_board_fkey'
		   AND contype = 'f'
		   AND conrelid = 'items'::regclass
		   AND confrelid = 'columns'::regclass`,
	).Scan(&fkCount); err != nil {
		t.Fatalf("query constraint: %v", err)
	}
	if fkCount != 1 {
		t.Errorf("composite FK items_column_board_fkey missing (count=%d)", fkCount)
	}

	// Hierarchy: items.kind (with enum check) and the self-referencing parent FK.
	var kindOK bool
	if err := db.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		 WHERE table_name = 'items' AND column_name = 'kind' AND is_nullable = 'NO'
		   AND column_default = '''task''::text'`,
	).Scan(&kindOK); err != nil {
		t.Fatalf("query items.kind: %v", err)
	}
	if !kindOK {
		t.Error("items.kind missing, nullable, or wrong default")
	}
	var parentFK int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM pg_constraint
		 WHERE conname = 'items_parent_item_id_fkey' AND contype = 'f'
		   AND conrelid = 'items'::regclass AND confrelid = 'items'::regclass`,
	).Scan(&parentFK); err != nil {
		t.Fatalf("query parent FK: %v", err)
	}
	if parentFK != 1 {
		t.Errorf("items_parent_item_id_fkey missing (count=%d)", parentFK)
	}

	// Archive state: items.archived_at timestamptz NULL (NULL = active).
	var archivedOK bool
	if err := db.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		 WHERE table_name = 'items' AND column_name = 'archived_at'
		   AND data_type = 'timestamp with time zone' AND is_nullable = 'YES'`,
	).Scan(&archivedOK); err != nil {
		t.Fatalf("query items.archived_at: %v", err)
	}
	if !archivedOK {
		t.Error("items.archived_at missing, not nullable, or wrong type")
	}

	// items_kind_check pins the kind enum at the DB level.
	var kindCheck int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM pg_constraint
		 WHERE conname = 'items_kind_check' AND contype = 'c'
		   AND conrelid = 'items'::regclass`,
	).Scan(&kindCheck); err != nil {
		t.Fatalf("query kind check: %v", err)
	}
	if kindCheck != 1 {
		t.Errorf("items_kind_check missing (count=%d)", kindCheck)
	}

	// Unique item keys: boards.key_prefix and items.key are NOT NULL, and
	// board_sequences exists (the per-board key counter).
	var keyPrefixOK bool
	if err := db.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		 WHERE table_name = 'boards' AND column_name = 'key_prefix' AND is_nullable = 'NO'`,
	).Scan(&keyPrefixOK); err != nil {
		t.Fatalf("query boards.key_prefix: %v", err)
	}
	if !keyPrefixOK {
		t.Error("boards.key_prefix missing or nullable")
	}
	var itemKeyOK bool
	if err := db.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		 WHERE table_name = 'items' AND column_name = 'key' AND is_nullable = 'NO'`,
	).Scan(&itemKeyOK); err != nil {
		t.Fatalf("query items.key: %v", err)
	}
	if !itemKeyOK {
		t.Error("items.key missing or nullable")
	}
	var seqTable bool
	if err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'public' AND tablename = 'board_sequences')`,
	).Scan(&seqTable); err != nil {
		t.Fatalf("query board_sequences: %v", err)
	}
	if !seqTable {
		t.Error("board_sequences table missing")
	}
	// The uniqueness that makes keys/prefixes stable references: a unique index
	// on items(key) and one on boards(key_prefix).
	for _, idx := range []struct{ table, column string }{{"items", "key"}, {"boards", "key_prefix"}} {
		var unique bool
		if err := db.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM pg_index i
			   JOIN pg_class t ON t.oid = i.indrelid
			   JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
			   WHERE t.relname = $1 AND a.attname = $2 AND i.indisunique AND i.indisvalid
			 )`, idx.table, idx.column).Scan(&unique); err != nil {
			t.Fatalf("query unique index %s(%s): %v", idx.table, idx.column, err)
		}
		if !unique {
			t.Errorf("unique index on %s(%s) missing", idx.table, idx.column)
		}
	}
}

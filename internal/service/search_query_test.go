package service_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"api-first-kanban/internal/search"
	"api-first-kanban/internal/service"
	"api-first-kanban/internal/testutil"
)

// searchFixture seeds an isolated workspace/board (unique per test — the test
// database is shared across concurrent lanes) with:
//
//	t-bug    P1, owner "eng-svc", label "bug"
//	t-plain  priority defaulted to 2 by the store, no labels, no owner
//	t-epic   epic, closed, priority 0
//	t-arch   label "bug", archived
func searchFixture(t *testing.T) (*service.Service, service.BoardSnapshot, map[string]service.ItemView) {
	t.Helper()
	pool := testutil.OpenPool(t)
	svc := service.New(pool)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d-%s", time.Now().UnixNano(), strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	ws, err := svc.CreateWorkspace(ctx, "search-svc-"+suffix, nil, "eng-svc")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	snap, err := svc.CreateBoard(ctx, ws.ID, "Search Board", []string{"todo", "doing"}, "eng-svc")
	if err != nil {
		t.Fatalf("create board: %v", err)
	}
	bug, err := svc.CreateLabel(ctx, ws.ID, "bug", nil, "eng-svc")
	if err != nil {
		t.Fatalf("create label: %v", err)
	}
	wontfix, err := svc.CreateLabel(ctx, ws.ID, "wontfix", nil, "eng-svc")
	if err != nil {
		t.Fatalf("create label: %v", err)
	}

	str := func(s string) *string { return &s }
	i16 := func(v int16) *int16 { return &v }

	items := map[string]service.ItemView{}
	mk := func(name, title, kind string, prio *int16, owner *string, labelIDs []string) service.ItemView {
		it, err := svc.CreateItem(ctx, snap.Board.ID, name+" "+title, nil, nil, owner, prio, str(kind), nil, labelIDs, "eng-svc")
		if err != nil {
			t.Fatalf("create item %s: %v", name, err)
		}
		items[name] = it
		return it
	}
	_ = mk("t-bug", "Bug report", "task", i16(1), str("eng-svc"), []string{bug.ID})
	mk("t-plain", "Plain task", "task", nil, nil, nil)
	tEpic := mk("t-epic", "Big epic", "epic", i16(0), nil, []string{wontfix.ID})
	if _, err := svc.UpdateItem(ctx, tEpic.ID, service.UpdateItemOpts{Closed: boolPtr(true)}, "eng-svc"); err != nil {
		t.Fatalf("close epic: %v", err)
	}
	tArch := mk("t-arch", "Archived bug", "task", nil, nil, []string{bug.ID})
	if _, err := svc.ArchiveItem(ctx, tArch.ID, "eng-svc"); err != nil {
		t.Fatalf("archive item: %v", err)
	}

	fresh, err := svc.GetBoardSnapshot(ctx, snap.Board.ID, false)
	if err != nil {
		t.Fatalf("board snapshot: %v", err)
	}
	return svc, fresh, items
}

func boolPtr(b bool) *bool { return &b }

// keysOf extracts item keys in snapshot order.
func keysOf(snap service.BoardSnapshot) []string {
	keys := make([]string, 0, len(snap.Items))
	for _, it := range snap.Items {
		keys = append(keys, it.Key)
	}
	return keys
}

func containsKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// TestSearchBoardSnapshotFiltered is the main table: seeded items + query →
// expected subset, including filtered counts, archived exclusion under q,
// default priority = 2 through the real store, and label filtering.
func TestSearchBoardSnapshotFiltered(t *testing.T) {
	svc, snap, items := searchFixture(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		q      string
		want   []string // expected item names (fixture keys), any order
		wantB1 int64    // expected item_count of column "todo"
		wantB2 int64    // expected item_count of column "doing"
	}{
		{name: "match all non-archived", q: "", want: []string{"t-bug", "t-plain", "t-epic"}},
		{name: "whitespace query matches all", q: "   ", want: []string{"t-bug", "t-plain", "t-epic"}},
		{name: "label equality excludes archived", q: "LABEL = 'bug'", want: []string{"t-bug"}},
		{name: "label inequality vacuously true for unlabeled", q: "LABEL != 'bug'", want: []string{"t-plain", "t-epic"}},
		{name: "label IN list", q: "LABEL IN ('BUG', 'wontfix')", want: []string{"t-bug", "t-epic"}},
		{name: "store-default priority evaluates as 2", q: "PRIORITY = 2", want: []string{"t-plain"}},
		{name: "priority ordering", q: "PRIORITY <= 1", want: []string{"t-bug", "t-epic"}},
		{name: "kind enum", q: "KIND = 'epic'", want: []string{"t-epic"}},
		{name: "kind not in domain matches nothing", q: "KIND = 'story'", want: nil},
		{name: "closed boolean", q: "CLOSED = true", want: []string{"t-epic"}},
		{name: "owner absent is empty string", q: "OWNER = ''", want: []string{"t-plain", "t-epic"}},
		{name: "owner set", q: "OWNER = 'ENG-SVC'", want: []string{"t-bug"}},
		{name: "not label", q: "NOT LABEL = 'wontfix'", want: []string{"t-bug", "t-plain"}},
		{name: "recently created matches CREATED >= iso", q: "CREATED >= 2020-01-01", want: []string{"t-bug", "t-plain", "t-epic"}},
		{name: "created before 24h ago matches nothing", q: "CREATED < -1d", want: nil},
		{name: "updated_at equals created_at", q: "UPDATED >= -1h AND CREATED >= -1h", want: []string{"t-bug", "t-plain", "t-epic"}},
		{name: "boolean conjunction", q: "CLOSED = false AND PRIORITY <= 1", want: []string{"t-bug"}},
		{name: "disjunction with grouping", q: "(KIND = 'epic' OR KIND = 'milestone') AND CLOSED = true", want: []string{"t-epic"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := search.Parse(tc.q)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.q, err)
			}
			got, err := svc.GetBoardSnapshotWithQuery(ctx, snap.Board.ID, false, expr)
			if err != nil {
				t.Fatalf("service.GetBoardSnapshotWithQuery(%q): %v", tc.q, err)
			}
			keys := keysOf(got)
			if len(keys) != len(tc.want) {
				t.Fatalf("got keys %v, want %v", keys, tc.want)
			}
			for _, w := range tc.want {
				if !containsKey(keys, items[w].Key) {
					t.Errorf("expected item %s missing from %v", w, keys)
				}
			}
			for _, col := range got.Columns {
				wantCount := int64(0)
				if col.Name == "todo" {
					wantCount = int64(len(tc.want))
				}
				if col.ItemCount != wantCount {
					t.Errorf("column %s item_count = %d, want %d", col.Name, col.ItemCount, wantCount)
				}
			}
			// board/unchanged fields still present
			if got.Board.ID != snap.Board.ID {
				t.Errorf("board id changed: %s != %s", got.Board.ID, snap.Board.ID)
			}
			if got.Items == nil {
				t.Error("Items must never be nil")
			}
		})
	}
	// t-arch is archived and must never appear under any query (default exclusion).
	expr, _ := search.Parse("LABEL = 'bug' OR LABEL != 'bug'")
	got, err := svc.GetBoardSnapshotWithQuery(ctx, snap.Board.ID, false, expr)
	if err != nil {
		t.Fatalf("t-arch check: %v", err)
	}
	if containsKey(keysOf(got), items["t-arch"].Key) {
		t.Error("archived item leaked into queried snapshot")
	}
}

// TestSearchBoardSnapshotEmptyResult pins a valid query matching nothing.
func TestSearchBoardSnapshotEmptyResult(t *testing.T) {
	svc, snap, _ := searchFixture(t)
	expr, err := search.Parse("KIND = 'story'")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := svc.GetBoardSnapshotWithQuery(context.Background(), snap.Board.ID, false, expr)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got.Items) != 0 {
		t.Errorf("expected empty items, got %v", keysOf(got))
	}
	for _, col := range got.Columns {
		if col.ItemCount != 0 {
			t.Errorf("column %s item_count = %d, want 0", col.Name, col.ItemCount)
		}
	}
}

// TestSearchBoardSnapshotNoQueryRegression pins that a nil expr is
// bit-identical to today's service.GetBoardSnapshot path.
func TestSearchBoardSnapshotNoQueryRegression(t *testing.T) {
	svc, snap, _ := searchFixture(t)
	got, err := svc.GetBoardSnapshotWithQuery(context.Background(), snap.Board.ID, false, nil)
	if err != nil {
		t.Fatalf("nil expr: %v", err)
	}
	base, err := svc.GetBoardSnapshot(context.Background(), snap.Board.ID, false)
	if err != nil {
		t.Fatalf("plain snapshot: %v", err)
	}
	if len(got.Items) != len(base.Items) || len(got.Columns) != len(base.Columns) {
		t.Fatalf("shape mismatch: %d items/%d cols vs %d items/%d cols",
			len(got.Items), len(got.Columns), len(base.Items), len(base.Columns))
	}
	for i := range got.Items {
		if !reflect.DeepEqual(got.Items[i], base.Items[i]) {
			t.Errorf("item[%d] differs: %+v vs %+v", i, got.Items[i], base.Items[i])
		}
	}
	for i := range got.Columns {
		if got.Columns[i] != base.Columns[i] {
			t.Errorf("column[%d] differs: %+v vs %+v", i, got.Columns[i], base.Columns[i])
		}
	}
	// IncludeArchived opt-in still works alongside a query.
	all, err := svc.GetBoardSnapshotWithQuery(context.Background(), snap.Board.ID, true, search.MatchAll{})
	if err != nil {
		t.Fatalf("includeArchived query: %v", err)
	}
	if len(all.Items) != len(base.Items)+1 {
		t.Errorf("includeArchived items = %d, want %d", len(all.Items), len(base.Items)+1)
	}
}

// TestSearchBoardSnapshotOrderPreserved pins spec §7.1: search never reorders.
func TestSearchBoardSnapshotOrderPreserved(t *testing.T) {
	svc, snap, _ := searchFixture(t)
	base, err := svc.GetBoardSnapshot(context.Background(), snap.Board.ID, false)
	if err != nil {
		t.Fatalf("plain snapshot: %v", err)
	}
	expr, err := search.Parse("CREATED >= 2000-01-01")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := svc.GetBoardSnapshotWithQuery(context.Background(), snap.Board.ID, false, expr)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	a, b := keysOf(base), keysOf(got)
	if len(a) != len(b) {
		t.Fatalf("length mismatch: %v vs %v", a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("order changed at %d: %v vs %v", i, a, b)
		}
	}
}

// TestSearchParseErrorSeam pins the transport seam: the raw query is parsed
// ONCE (transports call search.Parse, map a *search.ParseError to HTTP 400 per
// spec §6, and only hand the service a valid expression). The position fields
// carry the spec §6 offset/line/col contract.
func TestSearchParseErrorSeam(t *testing.T) {
	_, err := search.Parse("created < -1d")
	var perr *search.ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("want *search.ParseError, got %v", err)
	}
	if perr.Line != 1 || perr.Col != 1 || perr.Offset != 0 {
		t.Errorf("parse error position = %d:%d (offset %d), want 1:1 (offset 0)", perr.Line, perr.Col, perr.Offset)
	}
	// A valid expr of an empty query is service.MatchAll and never reaches the error path.
	expr, err := search.Parse("  ")
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if _, ok := expr.(search.MatchAll); !ok {
		t.Errorf("empty query expr = %T, want search.MatchAll", expr)
	}
}

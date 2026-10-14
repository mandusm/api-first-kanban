package search

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// now is the fixed reference instant used throughout the evaluation tests
// (spec §7.1: now is injected, never read from the clock).
var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// baseItem is a typical open item; individual tests override fields.
func baseItem() Item {
	return Item{
		Key:        "PB-54",
		Title:      "Wire Request Auth",
		Kind:       "task",
		Priority:   2,
		OwnerAgent: "worker-spec",
		Closed:     false,
		CreatedAt:  now.Add(-2 * time.Hour),
		UpdatedAt:  now.Add(-2 * time.Hour),
		Labels:     []string{"bug", "api"},
	}
}

// mustParse parses q or fails the test.
func mustParse(t *testing.T, q string) Expr {
	t.Helper()
	expr, err := Parse(q)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", q, err)
	}
	return expr
}

func TestEvaluate(t *testing.T) {
	dayOld := now.Add(-24 * time.Hour) // exactly the -1d boundary
	weekOld := now.Add(-14 * 24 * time.Hour)
	isoMidnight := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		query string
		item  Item
		want  bool
	}{
		// --- MatchAll / empty query ------------------------------------
		{"empty query matches all", "", baseItem(), true},
		{"whitespace query matches all", "   \n\t ", baseItem(), true},

		// --- TITLE -----------------------------------------------------
		{"TITLE = exact", "TITLE = 'Wire Request Auth'", baseItem(), true},
		{"TITLE = case-insensitive", "TITLE = 'wire request auth'", baseItem(), true},
		{"TITLE = no substring", "TITLE = 'Wire'", baseItem(), false},
		{"TITLE != mismatch", "TITLE != 'other'", baseItem(), true},
		{"TITLE != match negated", "TITLE != 'wire request auth'", baseItem(), false},
		{"TITLE IN any-of", "TITLE IN ('nope', 'WIRE REQUEST AUTH')", baseItem(), true},
		{"TITLE IN none", "TITLE IN ('nope', 'other')", baseItem(), false},

		// --- KEY -------------------------------------------------------
		{"KEY = lowercase matches uppercase key", "KEY = 'pb-54'", baseItem(), true},
		{"KEY = exact", "KEY = 'PB-54'", baseItem(), true},
		{"KEY = other", "KEY = 'pb-99'", baseItem(), false},
		{"KEY IN", "KEY IN ('pb-99', 'PB-54')", baseItem(), true},

		// --- KIND ------------------------------------------------------
		{"KIND = match", "KIND = 'task'", baseItem(), true},
		{"KIND = case-insensitive", "KIND = 'TASK'", baseItem(), true},
		{"KIND = valid-grammar no match", "KIND = 'story'", baseItem(), false},
		{"KIND IN", "KIND IN ('epic', 'milestone')", baseItem(), false},

		// --- OWNER -----------------------------------------------------
		{"OWNER = match", "OWNER = 'worker-spec'", baseItem(), true},
		{"OWNER = case-insensitive", "OWNER = 'Worker-Spec'", baseItem(), true},
		{"ownerless item", "OWNER = ''", Item{Title: "t", Kind: "task", Priority: 2, Labels: nil, CreatedAt: now, UpdatedAt: now}, true},
		{"ownerless item != ''", "OWNER != ''", Item{Title: "t", Kind: "task", Priority: 2, CreatedAt: now, UpdatedAt: now}, false},
		{"owned item != ''", "OWNER != ''", baseItem(), true},
		{"OWNER IN", "OWNER IN ('other', 'worker-spec')", baseItem(), true},

		// --- PRIORITY --------------------------------------------------
		{"PRIORITY = integer", "PRIORITY = 2", baseItem(), true},
		{"PRIORITY = decimal no match", "PRIORITY = 1.5", baseItem(), false},
		{"PRIORITY < decimal literal", "PRIORITY < 2.5", baseItem(), true},
		{"PRIORITY <= 1", "PRIORITY <= 1", baseItem(), false},
		{"PRIORITY > 1", "PRIORITY > 1", baseItem(), true},
		{"PRIORITY != ", "PRIORITY != 2", baseItem(), false},
		{"PRIORITY = 7 out of range matches nothing", "PRIORITY = 7", baseItem(), false},

		// --- CLOSED ----------------------------------------------------
		{"CLOSED = true open item", "CLOSED = true", baseItem(), false},
		{"CLOSED = false open item", "CLOSED = false", baseItem(), true},
		{"CLOSED != true open item", "CLOSED != true", baseItem(), true},
		{"CLOSED != false open item", "CLOSED != false", baseItem(), false},
		{"CLOSED = true closed item", "CLOSED = true", func() Item { it := baseItem(); it.Closed = true; return it }(), true},

		// --- LABEL (set-valued, vacuous truth) -------------------------
		{"LABEL = any label", "LABEL = 'bug'", baseItem(), true},
		{"LABEL = case-insensitive", "LABEL = 'BUG'", baseItem(), true},
		{"LABEL = no match", "LABEL = 'feat'", baseItem(), false},
		{"LABEL != no match (labeled item)", "LABEL != 'feat'", baseItem(), true},
		{"LABEL != match negated", "LABEL != 'bug'", baseItem(), false},
		{"LABEL != vacuous truth on unlabeled item", "LABEL != 'bug'", Item{Title: "t", Kind: "task", Priority: 2, Labels: nil, CreatedAt: now, UpdatedAt: now}, true},
		{"LABEL = vacuous false on unlabeled item", "LABEL = 'bug'", Item{Title: "t", Kind: "task", Priority: 2, Labels: nil, CreatedAt: now, UpdatedAt: now}, false},
		{"LABEL IN any label", "LABEL IN ('feat', 'API')", baseItem(), true},
		{"LABEL IN no label", "LABEL IN ('feat', 'chore')", baseItem(), false},
		{"LABEL IN single ≡ =", "LABEL IN ('bug')", baseItem(), true},

		// --- Relative time (§4.1) ---------------------------------------
		{"CREATED < -1d inside window", "CREATED < -1d", baseItem(), false}, // created 2h ago, after boundary
		{"CREATED < -1d old item", "CREATED < -1d", func() Item { it := baseItem(); it.CreatedAt = dayOld.Add(-time.Minute); return it }(), true},
		{"CREATED < -1d exactly at boundary is NOT <", "CREATED < -1d", func() Item { it := baseItem(); it.CreatedAt = dayOld; return it }(), false},
		{"CREATED <= -1d exactly at boundary IS <=", "CREATED <= -1d", func() Item { it := baseItem(); it.CreatedAt = dayOld; return it }(), true},
		{"-0d equals now", "CREATED < -0d", func() Item { it := baseItem(); it.CreatedAt = now.Add(-time.Second); return it }(), true},
		{"-0d boundary excluded", "CREATED < -0d", func() Item { it := baseItem(); it.CreatedAt = now; return it }(), false},
		{"+1d future offset", "CREATED < 1d", baseItem(), true}, // created 2h ago < now+24h
		{"UPDATED >= -2w", "UPDATED >= -2w", func() Item { it := baseItem(); it.UpdatedAt = weekOld; return it }(), true},
		{"UPDATED > -2w at boundary excluded", "UPDATED > -2w", func() Item { it := baseItem(); it.UpdatedAt = weekOld; return it }(), false},
		{"-30m minutes", "CREATED < -30m", baseItem(), true},
		{"-2h hours", "CREATED < -2h", baseItem(), false},

		// --- ISO time (§4.2) -------------------------------------------
		{"date-only = UTC midnight", "CREATED >= 2026-10-01", func() Item { it := baseItem(); it.CreatedAt = isoMidnight; return it }(), true},
		{"date-only midnight boundary", "CREATED > 2026-10-01", func() Item { it := baseItem(); it.CreatedAt = isoMidnight; return it }(), false},
		{"no-zone timestamp = UTC", "UPDATED = 2026-10-05T10:00:00", func() Item { it := baseItem(); it.UpdatedAt = now.Add(-2 * time.Hour); return it }(), true},
		{"with zone offset", "UPDATED = 2026-10-05T12:00:00+02:00", func() Item { it := baseItem(); it.UpdatedAt = now.Add(-2 * time.Hour); return it }(), true},
		{"with negative zone offset", "UPDATED = 2026-10-05T06:00:00-04:00", func() Item { it := baseItem(); it.UpdatedAt = now.Add(-2 * time.Hour); return it }(), true},
		{"Z suffix", "UPDATED = 2026-10-05T10:00:00Z", func() Item { it := baseItem(); it.UpdatedAt = now.Add(-2 * time.Hour); return it }(), true},
		{"fractional seconds", "UPDATED = 2026-10-05T10:00:00.123456", func() Item {
			it := baseItem()
			it.UpdatedAt = now.Add(-2*time.Hour + 123456*time.Microsecond)
			return it
		}(), true},
		{"CREATED = exact instant", "CREATED = 2026-10-05T10:00:00Z", baseItem(), true},

		// --- Boolean logic (§7.2) --------------------------------------
		{"AND both true", "KIND = 'task' AND CLOSED = false", baseItem(), true},
		{"AND short-circuit left false", "KIND = 'epic' AND CLOSED = false", baseItem(), false},
		{"OR right true", "KIND = 'epic' OR CLOSED = false", baseItem(), true},
		{"OR both false", "KIND = 'epic' OR CLOSED = true", baseItem(), false},
		{"NOT negation", "NOT CLOSED = true", baseItem(), true},
		{"NOT stacking (double negation)", "NOT NOT CLOSED = false", baseItem(), true},
		{"NOT applies to primary not operator", "NOT LABEL = 'bug'", baseItem(), false},
		{"precedence NOT>AND>OR", "NOT KIND = 'task' AND CLOSED = false OR CLOSED = true", baseItem(), false},
		{"parentheses override precedence", "(KIND = 'task' AND CLOSED = false) OR CLOSED = true", baseItem(), true},

		// --- Spec §7.3 worked examples (now = 2026-10-05T12:00:00Z) ----
		{
			"§7.3 worked example 1: match",
			"CREATED < -1d AND LABEL IN ('bug')",
			func() Item {
				it := baseItem()
				it.CreatedAt = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
				it.Labels = []string{"Bug"}
				return it
			}(), true,
		},
		{"§7.3 worked example 1: no match (recent)", "CREATED < -1d AND LABEL IN ('bug')", baseItem(), false},
		{"§7.3 worked example 2: open P0/P1", "PRIORITY <= 1 AND NOT CLOSED = true", func() Item { it := baseItem(); it.Priority = 1; return it }(), true},
		{"§7.3 worked example 2: P2 excluded", "PRIORITY <= 1 AND NOT CLOSED = true", baseItem(), false},
		{
			"§7.3 worked example 3: epic/milestone within 2w",
			"(KIND = 'epic' OR KIND = 'milestone') AND UPDATED >= -2w",
			func() Item {
				it := baseItem()
				it.Kind = "epic"
				it.UpdatedAt = time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
				return it
			}(), true,
		},
		{
			"§7.3 worked example 3: task excluded",
			"(KIND = 'epic' OR KIND = 'milestone') AND UPDATED >= -2w",
			func() Item { it := baseItem(); it.UpdatedAt = time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC); return it }(), false,
		},
		{
			"§7.3 worked example 4: owner since Oct 1 UTC midnight",
			"OWNER = 'worker-spec' AND CREATED >= 2026-10-01",
			func() Item { it := baseItem(); it.CreatedAt = time.Date(2026, 10, 2, 8, 30, 0, 0, time.UTC); return it }(), true,
		},
		{
			"§7.3 worked example 4: before Oct 1 excluded",
			"OWNER = 'worker-spec' AND CREATED >= 2026-10-01",
			func() Item {
				it := baseItem()
				it.CreatedAt = time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC)
				return it
			}(), false,
		},
		{
			"§7.3 worked example 5: open, no wontfix (incl. unlabeled)",
			"CLOSED = false AND LABEL != 'wontfix'",
			Item{Title: "t", Kind: "task", Priority: 2, Labels: nil, CreatedAt: now, UpdatedAt: now}, true,
		},
		{"§7.3 worked example 5: wontfix excluded", "CLOSED = false AND LABEL != 'wontfix'", func() Item { it := baseItem(); it.Labels = []string{"WontFix"}; return it }(), false},
		{"§7.3 worked example 6: case-insensitive key", "KEY = 'pb-54'", baseItem(), true},
		{"§7.3 worked example 7: owned items", "NOT (TITLE = '' OR OWNER = '')", baseItem(), true},
		{"§7.3 worked example 7: ownerless excluded", "NOT (TITLE = '' OR OWNER = '')", Item{Title: "t", Kind: "task", Priority: 2, CreatedAt: now, UpdatedAt: now}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr := mustParse(t, tt.query)
			if got := Evaluate(expr, tt.item, now); got != tt.want {
				t.Errorf("Evaluate(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

// TestEvaluateDeterminism guards the spec requirement that RELTIME resolution
// never reads the clock: Evaluate must be a pure function of (expr, item, now).
func TestEvaluateDeterminism(t *testing.T) {
	expr := mustParse(t, "CREATED < -1d")
	item := func() Item { it := baseItem(); it.CreatedAt = now.Add(-25 * time.Hour); return it }()

	first := Evaluate(expr, item, now)
	for i := 0; i < 100; i++ {
		if got := Evaluate(expr, item, now); got != first {
			t.Fatalf("Evaluate is nondeterministic: got %v on iteration %d, first call %v", got, i, first)
		}
	}

	// Different `now` values produce correspondingly different results.
	dayOld := now.Add(-24 * time.Hour)
	if Evaluate(expr, func() Item { it := item; it.CreatedAt = dayOld.Add(-time.Minute); return it }(), now) != true {
		t.Error("expected true for created 25h ago with now=t0")
	}
	if Evaluate(expr, item, now.Add(-2*time.Hour)) != false {
		t.Error("expected false when now is moved earlier so the item is inside the window")
	}
}

// TestEvaluateShortCircuit verifies AND/OR short-circuiting does not evaluate
// the right side once the outcome is decided (observable only via panics from
// impossible states, which we induce with a malformed manual AST).
func TestEvaluateShortCircuit(t *testing.T) {
	broken := Cmp{Field: "BOGUS", Op: "=", Value: Value{Kind: KindString, Str: "x"}}
	item := baseItem()

	mustPanic := func(name string, expr Expr) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: expected panic from evaluating broken node", name)
			}
		}()
		Evaluate(expr, item, now)
	}

	// AND with false left must not evaluate the (panicking) right side.
	if Evaluate(And{Left: Cmp{Field: "KEY", Op: "=", Value: Value{Kind: KindString, Str: "other"}}, Right: broken}, item, now) {
		t.Error("expected false")
	}
	// OR with true left must not evaluate the right side.
	if !Evaluate(Or{Left: Cmp{Field: "KEY", Op: "=", Value: Value{Kind: KindString, Str: "PB-54"}}, Right: broken}, item, now) {
		t.Error("expected true")
	}
	// NOT must not protect against the panic (still evaluates its child).
	mustPanic("NOT", Not{Expr: broken})
}

// TestEvaluateUnknownNode pins the defensive panic on impossible AST states.
func TestEvaluateUnknownNode(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on unknown node")
		} else if !strings.Contains(fmt.Sprint(r), "unknown expression node") {
			t.Errorf("unexpected panic message: %v", r)
		}
	}()
	Evaluate(unknownExpr{}, baseItem(), now)
}

type unknownExpr struct{}

func (unknownExpr) isExpr() {}

// TestEvaluateTimeInstantComparison verifies comparisons are instant-based in
// UTC regardless of the location carried by the item's timestamps.
func TestEvaluateTimeInstantComparison(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable:", err)
	}
	it := baseItem()
	it.UpdatedAt = time.Date(2026, 10, 5, 6, 0, 0, 0, ny) // = 10:00:00Z

	expr := mustParse(t, "UPDATED = 2026-10-05T10:00:00Z")
	if !Evaluate(expr, it, now) {
		t.Error("instant comparison should ignore zone offset representation")
	}
}

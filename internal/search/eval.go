package search

import (
	"fmt"
	"strings"
	"time"
)

// Item is the minimal, transport-agnostic view of a kanban item that the
// evaluator operates on (spec §7.1: the fields of the board snapshot item
// view). The service layer (PB-57) adapts storage rows to this struct; the
// search package must not import service types.
//
// Field semantics (spec §5.6 — no NULL three-valued logic):
//   - OwnerAgent is "" when the item has no owner (§5.2).
//   - Priority defaults to 2 when unset (§5.3).
//   - Labels holds label names (comparison uses names only, §5.4).
//   - CreatedAt/UpdatedAt are instants; UpdatedAt equals CreatedAt until the
//     first update.
type Item struct {
	Key        string
	Title      string
	Kind       string
	Priority   float64
	OwnerAgent string
	Closed     bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Labels     []string
}

// Evaluate reports whether the parsed expression expr matches item, using
// now as the single reference instant for all RELTIME values (spec §4.1/§7.1:
// `now` is fixed once per evaluation; never re-read the clock).
//
// The evaluator assumes expr comes from Parse and is therefore well-typed
// (§5 static rules). Impossible states (unknown field/operator) panic as
// internal errors — they indicate a bug in the parser, not a user error.
func Evaluate(expr Expr, item Item, now time.Time) bool {
	switch e := expr.(type) {
	case MatchAll:
		return true
	case Or:
		return Evaluate(e.Left, item, now) || Evaluate(e.Right, item, now) // short-circuit
	case And:
		return Evaluate(e.Left, item, now) && Evaluate(e.Right, item, now) // short-circuit
	case Not:
		return !Evaluate(e.Expr, item, now)
	case Cmp:
		return evalCmp(e, item, now)
	case In:
		return evalIn(e, item, now)
	default:
		panic(fmt.Sprintf("search: unknown expression node %T", expr))
	}
}

// timeValue resolves a time-typed literal to an instant: RELTIME becomes
// now + offset; ISOTIME is already absolute (UTC, resolved at parse time).
func timeValue(v Value, now time.Time) time.Time {
	switch v.Kind {
	case KindRelTime:
		return now.Add(v.Rel)
	case KindISOTime:
		return v.Time
	default:
		panic(fmt.Sprintf("search: field requires a time value, got kind %v", v.Kind))
	}
}

// textField returns the item's text value for a text field (via the
// shared fieldSpecs table in search.go).
func textField(field string, item Item) string {
	spec, ok := fieldSpecs[field]
	if !ok || spec.category != catText {
		panic(fmt.Sprintf("search: unknown text field %q", field))
	}
	return spec.text(item)
}

// evalCmp evaluates a single field/operator/value comparison (§5),
// dispatching on the field's category from the fieldSpecs table.
func evalCmp(c Cmp, item Item, now time.Time) bool {
	spec, ok := fieldSpecs[c.Field]
	if !ok {
		panic(fmt.Sprintf("search: unknown field %q", c.Field))
	}
	switch spec.category {
	case catTime:
		return compareTime(spec.time(item), c.Op, timeValue(c.Value, now))

	case catNumber:
		return compareNumber(spec.number(item), c.Op, c.Value.Num)

	case catBool:
		eq := spec.boolean(item) == c.Value.Bool
		if c.Op == "=" {
			return eq
		}
		return !eq

	case catText:
		// Text: exact, case-insensitive (strings.EqualFold, §5.2).
		eq := strings.EqualFold(spec.text(item), c.Value.Str)
		if c.Op == "=" {
			return eq
		}
		return !eq

	case catSet:
		// Set-valued: `=` true iff ANY label equals the literal;
		// `!=` is its negation — vacuously true when no label matches,
		// including items with no labels at all (§5.4).
		anyEq := false
		for _, l := range item.Labels {
			if strings.EqualFold(l, c.Value.Str) {
				anyEq = true
				break
			}
		}
		if c.Op == "=" {
			return anyEq
		}
		return !anyEq

	default:
		panic(fmt.Sprintf("search: unknown field %q", c.Field))
	}
}

// evalIn evaluates field IN (v1, v2, ...): true iff any listed value matches.
// Dispatch reads the shared fieldSpecs table; IN is only permitted for
// text and set fields (see textOps).
func evalIn(in In, item Item, now time.Time) bool {
	spec, ok := fieldSpecs[in.Field]
	if !ok || (spec.category != catText && spec.category != catSet) {
		panic(fmt.Sprintf("search: IN not allowed for field %q", in.Field))
	}
	if spec.category == catSet {
		for _, l := range item.Labels {
			for _, val := range in.Values {
				if strings.EqualFold(l, val.Str) {
					return true
				}
			}
		}
		return false
	}
	v := spec.text(item)
	for _, val := range in.Values {
		if strings.EqualFold(v, val.Str) {
			return true
		}
	}
	return false
}

// compareTime applies an ordering/equality operator to instants (both in UTC
// semantics; time.Time comparison is instant-based regardless of location).
func compareTime(ts time.Time, op string, target time.Time) bool {
	switch op {
	case "=":
		return ts.Equal(target)
	case "!=":
		return !ts.Equal(target)
	case "<":
		return ts.Before(target)
	case "<=":
		return ts.Before(target) || ts.Equal(target)
	case ">":
		return ts.After(target)
	case ">=":
		return ts.After(target) || ts.Equal(target)
	default:
		panic(fmt.Sprintf("search: unknown time operator %q", op))
	}
}

// compareNumber applies an ordering/equality operator numerically.
func compareNumber(priority float64, op string, target float64) bool {
	switch op {
	case "=":
		return priority == target
	case "!=":
		return priority != target
	case "<":
		return priority < target
	case "<=":
		return priority <= target
	case ">":
		return priority > target
	case ">=":
		return priority >= target
	default:
		panic(fmt.Sprintf("search: unknown number operator %q", op))
	}
}

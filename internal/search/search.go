// Package search implements the kanban search query language (v1) as
// specified in docs/search-query.md: a hand-rolled lexer and
// recursive-descent parser producing a typed AST, with static type
// checking performed during parsing (§5) and the §6 error model.
package search

import (
	"fmt"
	"strings"
	"time"
)

// Expr is a node of the parsed query AST.
type Expr interface{ isExpr() }

// MatchAll is returned by Parse for an empty (or whitespace-only) query;
// it matches every item. Value type, like the other AST nodes.
type MatchAll struct{}

func (MatchAll) isExpr() {}

// Or is a disjunction: Left OR Right.
type Or struct{ Left, Right Expr }

func (Or) isExpr() {}

// And is a conjunction: Left AND Right.
type And struct{ Left, Right Expr }

func (And) isExpr() {}

// Not is a logical negation of a primary expression.
type Not struct{ Expr Expr }

func (Not) isExpr() {}

// Cmp is a single field/operator/value comparison.
type Cmp struct {
	Field string
	Op    string // "=", "!=", "<", "<=", ">", ">="
	Value Value
}

func (Cmp) isExpr() {}

// In is a field IN (v1, v2, ...) membership test.
type In struct {
	Field  string
	Values []Value
}

func (In) isExpr() {}

// ValueKind classifies a literal value.
type ValueKind int

const (
	KindString ValueKind = iota
	KindNumber
	KindRelTime
	KindISOTime
	KindBool
)

// Value is a parsed literal. Kind selects which payload fields are set.
// Raw is the original lexeme (including quotes for strings).
// ISOTime values are already resolved to time.Time (UTC); RelTime keeps
// a duration offset — the evaluator resolves `now` (PB-56).
type Value struct {
	Kind ValueKind
	Raw  string
	Str  string        // KindString: unescaped string value
	Num  float64       // KindNumber
	Rel  time.Duration // KindRelTime: offset (may be negative)
	Time time.Time     // KindISOTime: absolute instant, UTC
	Bool bool          // KindBool
}

// ParseError is a search query parse error carrying position information
// (byte offset, 1-based line/column) per spec §6.
type ParseError struct {
	Offset int
	Line   int
	Col    int
	Desc   string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error at %d:%d (offset %d): %s", e.Line, e.Col, e.Offset, e.Desc)
}

// IsBlank reports whether q is empty after trimming the spec §1.1 ASCII
// whitespace set (space/tab/newline/CR). Blank queries are valid and match
// all items; note that a NBSP-only query is NOT blank per spec — it lexes
// as unexpected input. Transports use this so "blank" means the same thing
// on every transport.
func IsBlank(q string) bool {
	return strings.Trim(q, " \t\n\r") == ""
}

// Parse parses a search query into an Expr. An empty (after trimming)
// query is valid and yields MatchAll. All lexical, syntactic and static
// type errors are returned as *ParseError; only the first error is
// reported.
func Parse(query string) (Expr, error) {
	// §1.1: whitespace is space/tab/newline (ASCII) — trim only those, so a
	// NBSP-only query is NOT treated as empty and lexes as unexpected input.
	if IsBlank(query) {
		return MatchAll{}, nil
	}
	return newParser(query).parse()
}

// fieldCategory classifies a field for evaluator dispatch. Category, not
// field name, drives the evaluator's comparison semantics.
type fieldCategory int

const (
	catText   fieldCategory = iota // single string value, case-insensitive equality
	catSet                         // set of strings; '=' matches iff ANY member equals
	catNumber                      // numeric ordering/equality
	catBool                        // boolean equality
	catTime                        // instant ordering/equality (RELTIME resolved against now)
)

// fieldSpec is the single source of truth for a query field (PB-63 P1-1):
// the lexer's field recognition, the parser's allowed-operator and required
// value-kind checks, and the evaluator's dispatch all read this table.
// Adding a field = adding one entry here (plus docs).
type fieldSpec struct {
	kind     ValueKind // required literal kind; KindRelTime accepts RELTIME or ISOTIME
	ops      map[string]bool
	category fieldCategory
	text     func(Item) string    // catText/catSet: value source
	time     func(Item) time.Time // catTime
	number   func(Item) float64   // catNumber
	boolean  func(Item) bool      // catBool
}

var cmpOps = map[string]bool{"=": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true}
var textOps = map[string]bool{"=": true, "!=": true, "IN": true}
var eqOps = map[string]bool{"=": true, "!=": true}

var fieldSpecs = map[string]fieldSpec{
	"CREATED":  {kind: KindRelTime, ops: cmpOps, category: catTime, time: func(i Item) time.Time { return i.CreatedAt }},
	"UPDATED":  {kind: KindRelTime, ops: cmpOps, category: catTime, time: func(i Item) time.Time { return i.UpdatedAt }},
	"PRIORITY": {kind: KindNumber, ops: cmpOps, category: catNumber, number: func(i Item) float64 { return i.Priority }},
	"CLOSED":   {kind: KindBool, ops: eqOps, category: catBool, boolean: func(i Item) bool { return i.Closed }},
	"LABEL":    {kind: KindString, ops: textOps, category: catSet}, // set: iterates item.Labels
	"KIND":     {kind: KindString, ops: textOps, category: catText, text: func(i Item) string { return i.Kind }},
	"OWNER":    {kind: KindString, ops: textOps, category: catText, text: func(i Item) string { return i.OwnerAgent }},
	"TITLE":    {kind: KindString, ops: textOps, category: catText, text: func(i Item) string { return i.Title }},
	"KEY":      {kind: KindString, ops: textOps, category: catText, text: func(i Item) string { return i.Key }},
}

// lineColAt computes the 1-based line and column (counting bytes) of a
// 0-based byte offset in q.
func lineColAt(q string, offset int) (line, col int) {
	line = 1
	for i := 0; i < offset && i < len(q); i++ {
		if q[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return line, col + 1
}

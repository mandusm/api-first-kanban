package search

import (
	"testing"
	"time"
)

func TestWorkedErrorTable(t *testing.T) {
	// Pinned verbatim from docs/search-query.md §6 worked-error table
	// (including the two corrected positions).
	tests := []struct {
		query string
		want  string // exact Error() string, "" = valid
	}{
		{"CREATED < -1d AND LABEL IN ('bug')", ""},
		{"created < -1d", "parse error at 1:1 (offset 0): unexpected input 'created'"},
		{"CREATED <", "parse error at 1:10 (offset 9): unexpected end of query"},
		{"PRIORITY = 'high'", "parse error at 1:12 (offset 11): field 'PRIORITY' requires a number value"},
		{"TITLE = 'wire", "parse error at 1:9 (offset 8): unterminated string literal"},
		{"CLOSED < true", "parse error at 1:8 (offset 7): operator '<' not allowed for field 'CLOSED'"},
		{"LABEL IN ()", "parse error at 1:10 (offset 9): IN requires at least one value"},
		{"(CREATED < -1d", "parse error at 1:15 (offset 14): expected ')' to match '(' at 1:1"},
	}
	for _, tt := range tests {
		expr, err := Parse(tt.query)
		if tt.want == "" {
			if err != nil {
				t.Errorf("Parse(%q) unexpected error: %v", tt.query, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("Parse(%q) = %v, want error %q", tt.query, expr, tt.want)
			continue
		}
		if err.Error() != tt.want {
			t.Errorf("Parse(%q) error = %q, want %q", tt.query, err.Error(), tt.want)
		}
		if _, ok := err.(*ParseError); !ok {
			t.Errorf("Parse(%q) error is %T, want *ParseError", tt.query, err)
		}
	}
}

func TestEmptyQuery(t *testing.T) {
	for _, q := range []string{"", "   ", "\n\t "} {
		expr, err := Parse(q)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", q, err)
		}
		if _, ok := expr.(MatchAll); !ok {
			t.Errorf("Parse(%q) = %T, want MatchAll value", q, expr)
		}
	}
}

func cmp(t *testing.T, e Expr, field, op, raw string) {
	t.Helper()
	c, ok := e.(Cmp)
	if !ok {
		t.Fatalf("want Cmp, got %T", e)
	}
	if c.Field != field || c.Op != op || c.Value.Raw != raw {
		t.Fatalf("got Cmp{%s %s %#v}, want %s %s %s", c.Field, c.Op, c.Value.Raw, field, op, raw)
	}
}

func TestPrecedence(t *testing.T) {
	// NOT a AND b OR c == ((NOT a) AND b) OR c
	expr, err := Parse("NOT LABEL = 'a' AND TITLE = 'b' OR KEY = 'c'")
	if err != nil {
		t.Fatal(err)
	}
	or, ok := expr.(Or)
	if !ok {
		t.Fatalf("top node = %T, want Or", expr)
	}
	and, ok := or.Left.(And)
	if !ok {
		t.Fatalf("or.Left = %T, want And", or.Left)
	}
	not, ok := and.Left.(Not)
	if !ok {
		t.Fatalf("and.Left = %T, want Not", and.Left)
	}
	if _, ok := not.Expr.(Cmp); !ok {
		t.Fatalf("not.Expr = %T, want Cmp", not.Expr)
	}
	cmp(t, and.Right, "TITLE", "=", "'b'")
	cmp(t, or.Right, "KEY", "=", "'c'")
}

func TestNotStacking(t *testing.T) {
	expr, err := Parse("NOT NOT CLOSED = true")
	if err != nil {
		t.Fatal(err)
	}
	n1, ok := expr.(Not)
	if !ok {
		t.Fatalf("top = %T", expr)
	}
	n2, ok := n1.Expr.(Not)
	if !ok {
		t.Fatalf("inner = %T", n1.Expr)
	}
	cmp(t, n2.Expr, "CLOSED", "=", "true")
}

func TestTokenEdgeCases(t *testing.T) {
	valid := []string{
		"NOT(CLOSED = true)", // no space after NOT
		"PRIORITY<=2",        // self-delimiting operator
		"PRIORITY <= 2",      // spaced
		"CREATED< -1d",       // spec: fine
		"NOT CLOSED=true AND PRIORITY!=3",
		"UPDATED >= 2026-10-04",
		"UPDATED >= 2026-10-04T12:30:00Z",
		"UPDATED >= 2026-10-04T14:30:00+02:00",
		"UPDATED >= 2026-10-04T12:30:00.123456789",
		"TITLE = 'it''s'",          // escaped quote
		"TITLE = 'multi\nline'",    // multi-line string
		"TITLE = ''",               // empty string
		"LABEL IN ('bug', 'feat')", // IN list
		"LABEL IN ('bug')",         // single-element IN == =
		"CREATED = -0d",            // -0d == now
		"CREATED < +1h",            // positive offset
		"PRIORITY < 1.5",           // decimal
		"PRIORITY = -1",            // negative number
		"KIND = 'story'",           // valid query, matches nothing
		"OWNER = ''",               // ownerless
	}
	for _, q := range valid {
		if _, err := Parse(q); err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", q, err)
		}
	}
	invalid := []struct {
		q    string
		desc string
	}{
		{"NOTx CLOSED = true", "unexpected input 'NOTx'"},    // single unknown token
		{"and CLOSED = true", "unexpected input 'and'"},      // lowercase keyword
		{"CREATED < -1d30m", "unexpected input"},             // units do not compound
		{"LABEL IN ('a',)", "expected value after operator"}, // trailing comma
		{"KIND IN ('task', 3)", "IN list values must have the same type"},
		{"KIND IN ('task', true)", "IN list values must have the same type"},
		{"PRIORITY IN (1)", "operator 'IN' not allowed for field 'PRIORITY'"},
		{"CREATED IN (-1d)", "operator 'IN' not allowed for field 'CREATED'"},
		{"CREATED = '2026-10-01'", "field 'CREATED' requires a time value"},
		{"CREATED < 5", "field 'CREATED' requires a time value"},
		{"UPDATED < 2026-13-01", "invalid ISO time"},
		{"UPDATED < 2026-10-01T24:00:00", "invalid ISO time"},
		{"UPDATED < 2026-10-01T12:00:60", "invalid ISO time"},
		{"UPDATED < 2026-10-01t12:00:00", "invalid ISO time '2026-10-01t12:00:00'"}, // lowercase t
		{"TITLE = 5", "field 'TITLE' requires a string value"},
		{"CLOSED = 'true'", "field 'CLOSED' requires a boolean value"},
		{"CLOSED = True", "unexpected input 'True'"},
		{"PRIORITY = 'high' OR", "field 'PRIORITY' requires a number value"},
		{"CREATED < !", "unexpected input '!'"},       // lone bang names the text
		{"LABEL IN ('a', !)", "unexpected input '!'"}, // lex error after IN comma surfaces
		{"LABEL IN ('a', 3.14)", "IN list values must have the same type"},
		{"CREATED", "expected operator after CREATED"}, // missing operator at EOF
		{"CREATED LABEL = 'x'", "expected operator after CREATED"},
		{"AND LABEL = 'x'", "unexpected input 'AND'"},
		{"LABEL IN", "unexpected end of query"},
		{"CREATED < -1d)", "unexpected input ')'"},
		{"LABEL = 'a' LABEL = 'b'", "unexpected input 'LABEL'"},
		{"CREATED < -1d AND", "unexpected end of query"},
		{"TITLE = 'wire request auth' (", "unexpected input '('"},
		{"CREATED < --1d", "unexpected input"},
		{"PRIORITY = 5x", "unexpected input '5x'"},
		{"UPDATED >= 2026-10-01Z", "invalid ISO time '2026-10-01Z'"},
		{"(LABEL = 'a' AND TITLE = 'b'", "expected ')' to match '(' at 1:1"},
		{"(CREATED < -1d) AND (LABEL = 'a'", "expected ')' to match '(' at 1:21"},
	}
	for _, tt := range invalid {
		_, err := Parse(tt.q)
		if err == nil {
			t.Errorf("Parse(%q): want error containing %q, got nil", tt.q, tt.desc)
			continue
		}
		pe, ok := err.(*ParseError)
		if !ok {
			t.Errorf("Parse(%q): error %T, want *ParseError", tt.q, err)
			continue
		}
		if !contains(pe.Desc, tt.desc) {
			t.Errorf("Parse(%q): desc %q, want containing %q", tt.q, pe.Desc, tt.desc)
		}
		if pe.Line < 1 || pe.Col < 1 || pe.Offset < 0 || pe.Offset > len(tt.q) {
			t.Errorf("Parse(%q): bad position %d:%d offset %d", tt.q, pe.Line, pe.Col, pe.Offset)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestLineColumn(t *testing.T) {
	_, err := Parse("LABEL = 'a'\n  FOO")
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("want *ParseError, got %v", err)
	}
	if pe.Line != 2 || pe.Col != 3 || pe.Offset != 14 {
		t.Errorf("got %d:%d offset %d, want 2:3 offset 14", pe.Line, pe.Col, pe.Offset)
	}
}

func TestValueSemantics(t *testing.T) {
	expr, err := Parse("TITLE = 'it''s'")
	if err != nil {
		t.Fatal(err)
	}
	c := expr.(Cmp)
	if c.Value.Str != "it's" {
		t.Errorf("escaped quote: %q", c.Value.Str)
	}
	if c.Value.Kind != KindString {
		t.Errorf("kind = %v", c.Value.Kind)
	}

	expr, _ = Parse("CREATED < -1d")
	c = expr.(Cmp)
	if c.Value.Kind != KindRelTime || c.Value.Rel != -24*time.Hour {
		t.Errorf("-1d = %v %v", c.Value.Kind, c.Value.Rel)
	}

	expr, _ = Parse("CREATED < -2w")
	if got := expr.(Cmp).Value.Rel; got != -14*24*time.Hour {
		t.Errorf("-2w = %v", got)
	}

	expr, _ = Parse("CREATED < -30m")
	if got := expr.(Cmp).Value.Rel; got != -30*time.Minute {
		t.Errorf("-30m = %v", got)
	}

	expr, _ = Parse("CREATED = -0d")
	if got := expr.(Cmp).Value.Rel; got != 0 {
		t.Errorf("-0d = %v", got)
	}

	// ISO date-only → midnight UTC
	expr, _ = Parse("UPDATED >= 2026-10-04")
	c = expr.(Cmp)
	if c.Value.Kind != KindISOTime {
		t.Fatalf("kind = %v", c.Value.Kind)
	}
	want := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	if !c.Value.Time.Equal(want) {
		t.Errorf("date = %v, want %v", c.Value.Time, want)
	}

	// RFC3339 with offset → UTC
	expr, _ = Parse("UPDATED >= 2026-10-04T14:30:00+02:00")
	c = expr.(Cmp)
	if !c.Value.Time.Equal(time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)) {
		t.Errorf("zoned = %v", c.Value.Time)
	}

	// Fractional seconds
	expr, _ = Parse("UPDATED >= 2026-10-04T12:00:00.5")
	if got := expr.(Cmp).Value.Time; got.Nanosecond() != 500000000 {
		t.Errorf("fraction = %v", got)
	}

	// IN list values
	expr, _ = Parse("LABEL IN ('bug', 'feat')")
	in := expr.(In)
	if in.Field != "LABEL" || len(in.Values) != 2 || in.Values[0].Str != "bug" || in.Values[1].Str != "feat" {
		t.Errorf("IN = %+v", in)
	}

	// NUMBER value
	expr, _ = Parse("PRIORITY <= 1.5")
	if got := expr.(Cmp).Value.Num; got != 1.5 {
		t.Errorf("1.5 = %v", got)
	}
}

func TestFirstErrorWins(t *testing.T) {
	// Lexical error earlier in the input beats a later type error; and a
	// parse error at an earlier token beats a later lex error.
	_, err := Parse("TITLE = 5 UPDATED < 2026-13-99")
	if got := err.(*ParseError).Desc; got != "field 'TITLE' requires a string value" {
		t.Errorf("first error = %q", got)
	}
	_, err = Parse("(PRIORITY = 'x'")
	if got := err.(*ParseError).Desc; got != "field 'PRIORITY' requires a number value" {
		t.Errorf("parse-before-lex error = %q", got)
	}
}

func TestASCIIWhitespaceOnlyEmpty(t *testing.T) {
	// §1.1: whitespace is space/tab/newline only. An NBSP-only query is not
	// "empty" — it lexes as unexpected input (spec-strict; deviates from
	// Unicode-friendly trimming on purpose).
	for _, q := range []string{"", " ", "\t\n\r"} {
		if _, err := Parse(q); err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", q, err)
		}
	}
	if _, err := Parse("\u00a0"); err == nil {
		t.Error("Parse(NBSP): want unexpected-input error, got nil")
	} else if pe := err.(*ParseError); pe.Desc != "unexpected input '\u00a0'" {
		t.Errorf("NBSP desc = %q", pe.Desc)
	}
}

func TestMatchAllIsValueType(t *testing.T) {
	expr, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	// Type-switch over the AST must see a value node, not a pointer.
	switch expr.(type) {
	case MatchAll:
	default:
		t.Errorf("got %T, want MatchAll value", expr)
	}
}

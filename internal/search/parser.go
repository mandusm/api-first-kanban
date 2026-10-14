package search

import "fmt"

type parser struct {
	q      string
	toks   []token
	lexErr *ParseError
	i      int
}

func newParser(q string) *parser {
	toks, lexErr := lexAll(q)
	return &parser{q: q, toks: toks, lexErr: lexErr}
}

func (p *parser) errAt(offset int, format string, args ...any) error {
	l, c := lineColAt(p.q, offset)
	return &ParseError{Offset: offset, Line: l, Col: c, Desc: fmt.Sprintf(format, args...)}
}

// peek returns the current token; tokErr signals a pending lex error.
func (p *parser) peek() token {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	if p.lexErr != nil {
		return token{kind: tokErr, start: p.lexErr.Offset, err: p.lexErr}
	}
	return token{kind: tokEOF, start: len(p.q)}
}

func (p *parser) advance() { p.i++ }

func (p *parser) parse() (Expr, error) {
	expr, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.lexErr != nil {
		return nil, p.lexErr
	}
	if t := p.peek(); t.kind != tokEOF {
		return nil, p.errAt(t.start, "unexpected input '%s'", t.text)
	}
	return expr, nil
}

func (p *parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOr {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = Or{Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (Expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokAnd {
		p.advance()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = And{Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseNot() (Expr, error) {
	n := 0
	for p.peek().kind == tokNot {
		p.advance()
		n++
	}
	expr, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for ; n > 0; n-- {
		expr = Not{Expr: expr}
	}
	return expr, nil
}

func (p *parser) parsePrimary() (Expr, error) {
	t := p.peek()
	switch t.kind {
	case tokLParen:
		openOffset := t.start
		p.advance()
		expr, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		closer := p.peek()
		if closer.kind != tokRParen {
			ol, oc := lineColAt(p.q, openOffset)
			return nil, p.errAt(closer.start, "expected ')' to match '(' at %d:%d", ol, oc)
		}
		p.advance()
		return expr, nil
	case tokField:
		return p.parseComparison()
	case tokErr:
		return nil, t.err
	case tokEOF:
		return nil, p.errAt(t.start, "unexpected end of query")
	default:
		return nil, p.errAt(t.start, "unexpected input '%s'", t.text)
	}
}

// Operator and required-value-kind checks read the shared fieldSpecs table
// (search.go) — no separate allowedOps/requiredKind maps here.
var kindName = map[ValueKind]string{
	KindString: "string", KindNumber: "number", KindRelTime: "time",
	KindISOTime: "time", KindBool: "boolean",
}

func kindMatches(spec fieldSpec, k ValueKind) bool {
	if spec.kind == KindRelTime {
		return k == KindRelTime || k == KindISOTime
	}
	return k == spec.kind
}

func (p *parser) parseComparison() (Expr, error) {
	fieldTok := p.peek()
	field := fieldTok.text
	p.advance()

	opTok := p.peek()
	spec, ok := fieldSpecs[field]
	if !ok {
		panic(fmt.Sprintf("search: unknown field %q", field))
	}
	switch opTok.kind {
	case tokOp:
		if !spec.ops[opTok.text] {
			return nil, p.errAt(opTok.start, "operator '%s' not allowed for field '%s'", opTok.text, field)
		}
		p.advance()
		v, err := p.parseValue(field)
		if err != nil {
			return nil, err
		}
		return Cmp{Field: field, Op: opTok.text, Value: v}, nil
	case tokIn:
		if !spec.ops["IN"] {
			return nil, p.errAt(opTok.start, "operator 'IN' not allowed for field '%s'", field)
		}
		p.advance()
		return p.parseInList(field)
	case tokErr:
		return nil, opTok.err
	case tokEOF:
		return nil, p.errAt(opTok.start, "expected operator after %s", field)
	default:
		return nil, p.errAt(opTok.start, "expected operator after %s", field)
	}
}

func (p *parser) parseInList(field string) (Expr, error) {
	open := p.peek()
	if open.kind != tokLParen {
		if open.kind == tokEOF {
			return nil, p.errAt(open.start, "unexpected end of query")
		}
		return nil, p.errAt(open.start, "expected '(' after IN")
	}
	openOffset := open.start
	p.advance()

	if p.peek().kind == tokRParen {
		// Position points at the '(' of the empty list (spec §6 worked example).
		return nil, p.errAt(openOffset, "IN requires at least one value")
	}

	var values []Value
	for {
		t := p.peek()
		if t.kind == tokErr {
			return nil, t.err
		}
		// Mixed-type check takes precedence over the per-field type check
		// once a first value is established (spec §6).
		if len(values) > 0 && valueKindOf(t.kind) != values[0].Kind {
			return nil, p.errAt(t.start, "IN list values must have the same type")
		}
		v, err := p.valueFromToken(t, field)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
		p.advance()
		next := p.peek()
		switch next.kind {
		case tokComma:
			p.advance()
			// A pending lex error right after the comma must surface itself,
			// not be masked by the generic missing-value message.
			if p.peek().kind == tokErr {
				return nil, p.peek().err
			}
			if pk := p.peek().kind; pk == tokRParen || pk == tokEOF {
				return nil, p.errAt(p.peek().start, "expected value after operator")
			}
		case tokRParen:
			p.advance()
			return In{Field: field, Values: values}, nil
		case tokEOF:
			return nil, p.errAt(next.start, "unexpected end of query")
		default:
			return nil, p.errAt(next.start, "expected ',' or ')' in IN list")
		}
	}
}

// valueKindOf maps a token kind to the ValueKind it would produce.
func valueKindOf(k tokKind) ValueKind {
	switch k {
	case tokString:
		return KindString
	case tokNumber:
		return KindNumber
	case tokRelTime:
		return KindRelTime
	case tokISOTime:
		return KindISOTime
	case tokBool:
		return KindBool
	default:
		return -1
	}
}

// parseValue reads a single literal token and statically type-checks it
// against the field (spec §5).
func (p *parser) parseValue(field string) (Value, error) {
	t := p.peek()
	v, err := p.valueFromToken(t, field)
	if err != nil {
		return Value{}, err
	}
	p.advance()
	return v, nil
}

func (p *parser) valueFromToken(t token, field string) (Value, error) {
	switch t.kind {
	case tokErr:
		return Value{}, t.err
	case tokString, tokNumber, tokRelTime, tokISOTime, tokBool:
		var k ValueKind
		switch t.kind {
		case tokString:
			k = KindString
		case tokNumber:
			k = KindNumber
		case tokRelTime:
			k = KindRelTime
		case tokISOTime:
			k = KindISOTime
		default:
			k = KindBool
		}
		spec, ok := fieldSpecs[field]
		if !ok {
			panic(fmt.Sprintf("search: unknown field %q", field))
		}
		if !kindMatches(spec, k) {
			return Value{}, p.errAt(t.start, "field '%s' requires a %s value", field, kindName[spec.kind])
		}
		return Value{Kind: k, Raw: t.text, Str: t.str, Num: t.num, Rel: t.rel, Time: t.tm, Bool: t.str == "true"}, nil
	case tokEOF:
		return Value{}, p.errAt(t.start, "unexpected end of query")
	default:
		return Value{}, p.errAt(t.start, "expected value after operator")
	}
}

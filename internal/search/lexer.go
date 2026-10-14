package search

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

type tokKind int

const (
	tokEOF tokKind = iota
	tokField
	tokOp // = != < <= > >=
	tokAnd
	tokOr
	tokNot
	tokIn
	tokLParen
	tokRParen
	tokComma
	tokString
	tokNumber
	tokRelTime
	tokISOTime
	tokBool
	tokErr // lexing failed; err holds the *ParseError
)

type token struct {
	kind  tokKind
	text  string // raw lexeme ("" for EOF)
	str   string // tokString: unescaped value
	num   float64
	rel   time.Duration
	tm    time.Time
	start int // byte offset of first character
	err   *ParseError
}

var (
	isoRe     = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(T(\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-]\d{2}:\d{2})?)?`)
	relRe     = regexp.MustCompile(`^[+-]?[0-9]+[dhmw]`)
	numRe     = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?`)
	alnumNext = regexp.MustCompile(`^[0-9A-Za-z]`)
)

// lexWord's field recognition reads the shared fieldSpecs table
// (search.go) — no separate field set here.

// lexAll tokenizes the whole query. On a lexical error, tokens end right
// before the offending position and the error is returned alongside; the
// parser surfaces it only after consuming the successfully lexed prefix
// (first error wins, in input order).
func lexAll(q string) ([]token, *ParseError) {
	var toks []token
	pos := 0
	for {
		for pos < len(q) && (q[pos] == ' ' || q[pos] == '\t' || q[pos] == '\n' || q[pos] == '\r') {
			pos++
		}
		if pos >= len(q) {
			toks = append(toks, token{kind: tokEOF, start: len(q)})
			return toks, nil
		}
		tok, next, perr := lexOne(q, pos)
		if perr != nil {
			return toks, perr
		}
		toks = append(toks, tok)
		pos = next
	}
}

func lexOne(q string, pos int) (token, int, *ParseError) {
	c := q[pos]
	fail := func(desc string) (token, int, *ParseError) {
		l, cl := lineColAt(q, pos)
		return token{}, pos, &ParseError{Offset: pos, Line: l, Col: cl, Desc: desc}
	}

	switch {
	case c == '\'':
		return lexString(q, pos)
	case c == '(':
		return token{kind: tokLParen, text: "(", start: pos}, pos + 1, nil
	case c == ')':
		return token{kind: tokRParen, text: ")", start: pos}, pos + 1, nil
	case c == ',':
		return token{kind: tokComma, text: ",", start: pos}, pos + 1, nil
	case c == '=':
		return token{kind: tokOp, text: "=", start: pos}, pos + 1, nil
	case c == '!' && pos+1 < len(q) && q[pos+1] == '=':
		return token{kind: tokOp, text: "!=", start: pos}, pos + 2, nil
	case c == '!':
		return fail("unexpected input '!'")
	case c == '<' && pos+1 < len(q) && q[pos+1] == '=':
		return token{kind: tokOp, text: "<=", start: pos}, pos + 2, nil
	case c == '<':
		return token{kind: tokOp, text: "<", start: pos}, pos + 1, nil
	case c == '>' && pos+1 < len(q) && q[pos+1] == '=':
		return token{kind: tokOp, text: ">=", start: pos}, pos + 2, nil
	case c == '>':
		return token{kind: tokOp, text: ">", start: pos}, pos + 1, nil
	case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
		return lexWord(q, pos, fail)
	default:
		return lexNumeric(q, pos, fail)
	}
}

func lexString(q string, pos int) (token, int, *ParseError) {
	var sb strings.Builder
	j := pos + 1
	for {
		if j >= len(q) {
			l, cl := lineColAt(q, pos)
			return token{}, pos, &ParseError{Offset: pos, Line: l, Col: cl, Desc: "unterminated string literal"}
		}
		if q[j] == '\'' {
			if j+1 < len(q) && q[j+1] == '\'' {
				sb.WriteByte('\'')
				j += 2
				continue
			}
			j++
			break
		}
		sb.WriteByte(q[j])
		j++
	}
	return token{kind: tokString, text: q[pos:j], str: sb.String(), start: pos}, j, nil
}

func lexWord(q string, pos int, fail func(string) (token, int, *ParseError)) (token, int, *ParseError) {
	j := pos
	for j < len(q) && (q[j] >= 'A' && q[j] <= 'Z' || q[j] >= 'a' && q[j] <= 'z' || q[j] >= '0' && q[j] <= '9') {
		j++
	}
	word := q[pos:j]
	k, ok := map[string]tokKind{
		"AND": tokAnd, "OR": tokOr, "NOT": tokNot, "IN": tokIn,
		"true": tokBool, "false": tokBool,
	}[word]
	if !ok {
		if _, ok := fieldSpecs[word]; ok {
			k = tokField
		} else {
			return fail("unexpected input '" + word + "'")
		}
	}
	return token{kind: k, text: word, str: word, start: pos}, j, nil
}

// lexNumeric handles NUMBER, RELTIME and ISOTIME tokens (and unknown
// tokens starting with digits or a sign).
func lexNumeric(q string, pos int, fail func(string) (token, int, *ParseError)) (token, int, *ParseError) {
	// ISOTIME first (only starts with a digit).
	if q[pos] >= '0' && q[pos] <= '9' {
		if m := isoRe.FindStringSubmatch(q[pos:]); m != nil {
			if !alnumNext.MatchString(q[pos+len(m[0]):]) {
				t, ok := parseISOTime(m)
				if !ok {
					return fail("invalid ISO time '" + m[0] + "'")
				}
				return token{kind: tokISOTime, text: m[0], tm: t, start: pos}, pos + len(m[0]), nil
			}
			// ISO-shaped prefix followed by more alphanumeric characters
			// (lowercase 't', a trailing 'Z' on a date-only form, extra digits):
			// name the whole run as an invalid ISO time instead of masking it
			// as an unrelated number.
			return fail("invalid ISO time '" + unknownRun(q, pos) + "'")
		}
	}
	if m := relRe.FindString(q[pos:]); m != "" && !alnumNext.MatchString(q[pos+len(m):]) {
		d, ok := parseRelTime(m)
		if !ok {
			return fail("invalid relative time '" + m + "'")
		}
		return token{kind: tokRelTime, text: m, rel: d, start: pos}, pos + len(m), nil
	}
	if q[pos] != '+' {
		if m := numRe.FindString(q[pos:]); m != "" && !alnumNext.MatchString(q[pos+len(m):]) && !strings.HasSuffix(m, ".") {
			n, _ := strconv.ParseFloat(m, 64)
			return token{kind: tokNumber, text: m, num: n, start: pos}, pos + len(m), nil
		}
	}
	// Unknown token: maximal run of non-space, non-punctuation characters.
	return fail("unexpected input '" + unknownRun(q, pos) + "'")
}

// unknownRun scans the maximal run of non-whitespace, non-punctuation
// characters starting at pos (the offending token text for unknown input).
func unknownRun(q string, pos int) string {
	j := pos
	for j < len(q) && !strings.ContainsRune(" \t\r\n()<>=!,", rune(q[j])) {
		j++
	}
	return q[pos:j]
}

func parseRelTime(s string) (time.Duration, bool) {
	sign := int64(1)
	body := s
	if s[0] == '-' {
		sign = -1
		body = s[1:]
	} else if s[0] == '+' {
		body = s[1:]
	}
	unit := body[len(body)-1]
	digits := body[:len(body)-1]
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	var mult int64
	switch unit {
	case 'd':
		mult = 24 * int64(time.Hour)
	case 'h':
		mult = int64(time.Hour)
	case 'm':
		mult = int64(time.Minute)
	case 'w':
		mult = 7 * 24 * int64(time.Hour)
	default:
		return 0, false
	}
	return time.Duration(sign * n * mult), true
}

// parseISOTime validates an ISOTIME lexeme against the shape matched by
// isoRe and returns the absolute instant (UTC).
func parseISOTime(m []string) (time.Time, bool) {
	date := m[1] + "-" + m[2] + "-" + m[3]
	if m[4] == "" { // date only: midnight UTC
		t, err := time.Parse("2006-01-02", date)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	}
	frac := m[8]
	if len(frac) > 10 { // keep at most nanosecond precision
		frac = frac[:10]
	}
	stamp := date + "T" + m[5] + ":" + m[6] + ":" + m[7] + frac
	var t time.Time
	var err error
	if m[9] != "" { // has zone suffix
		t, err = time.Parse(time.RFC3339, stamp+m[9])
	} else { // no zone: interpreted as UTC
		t, err = time.Parse("2006-01-02T15:04:05.999999999", stamp)
	}
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

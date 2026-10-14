# Search Query Language — v1 Grammar & Semantics

Authoritative specification for the `api-first-kanban` search query language.
This document gates PB-55 (lexer/parser/AST), PB-56 (evaluator + relative
time), PB-57 (service integration), PB-58 (REST `q` parameter + error
mapping), PB-59 (MCP), PB-60 (frontend search box) and PB-61 (e2e/docs).
Implementations must not invent behavior that is not defined here; if
something is genuinely unspecified, raise it on this ticket before coding.

A search query is a boolean expression over item fields. Example:

```
CREATED < -1d AND LABEL IN ('bug')
```

meaning: *items created more than 24 hours ago that carry the label `bug`*.

---

## 1. Lexical structure

### 1.1 Character set & encoding

Queries are UTF-8 strings. Whitespace is spaces, tabs and newlines; it is
insignificant between tokens (but not inside quoted string literals).

### 1.2 Tokens

| Token      | Lexical form                                                                                                        |
|------------|---------------------------------------------------------------------------------------------------------------------|
| `FIELD`    | One of the field names in §2, uppercase only: `CREATED UPDATED LABEL KIND PRIORITY OWNER TITLE KEY CLOSED`          |
| `OP`       | `=` `!=` `<` `<=` `>` `>=` `IN`                                                                                     |
| `AND`      | literal `AND` (uppercase only)                                                                                       |
| `OR`       | literal `OR` (uppercase only)                                                                                        |
| `NOT`      | literal `NOT` (uppercase only)                                                                                       |
| `(` `)`    | grouping parentheses                                                                                                 |
| `STRING`   | single-quoted string literal, `'…'` — see §1.3                                                                       |
| `NUMBER`   | optional `-`, digits, optional fractional part: `-?[0-9]+(\.[0-9]+)?` (no exponent, no leading `+`)                |
| `RELTIME`  | relative time offset: `[-+]?[0-9]+[dhmw]` — see §4.1                                                                |
| `ISOTIME`  | ISO-8601 date or RFC3339 timestamp — see §4.2                                                                        |
| `BOOL`     | `true` or `false`, lowercase only                                                                                    |

Rules:

- Keywords (`AND`, `OR`, `NOT`, `IN`) and field names are **case-sensitive
  and uppercase-only**. `and`, `And`, `created` are not recognized; `created`
  as a bare word is a parse error ("unknown token"), not a field.
- `IN` and `BOOL` are disambiguated by context: `IN` only follows a field
  where an operator is expected; `true`/`false` only appear where a value is
  expected.
- Comments: none in v1.
- Empty query string (after trimming) means "match all items" — accepted, not
  an error (transport layers decide whether an absent vs empty `q` differ;
  both match all).

### 1.3 String literals

Single-quoted: `'bug'`. A literal single quote inside a string is escaped by
doubling: `'it''s'` denotes the value `it's`. Backslashes are **ordinary
characters**, never escapes. Unterminated string (no closing `'` before end
of input) → parse error at the opening quote. The empty string `''` is a valid
literal (matches nothing for text fields — see §5).

Multi-line strings are allowed (a raw newline inside quotes is part of the
value).

---

## 2. Fields

| Field      | Item property          | Value type   | Allowed operators                    |
|------------|------------------------|--------------|--------------------------------------|
| `CREATED`  | `created_at` (timestamptz) | time     | `=`, `!=`, `<`, `<=`, `>`, `>=`      |
| `UPDATED`  | `updated_at` (timestamptz) | time     | `=`, `!=`, `<`, `<=`, `>`, `>=`      |
| `LABEL`    | set of label `name`s   | text (set)  | `=`, `!=`, `IN`                      |
| `KIND`     | `kind`                 | enum text    | `=`, `!=`, `IN`                      |
| `PRIORITY` | `priority`             | number (integer 0..3) | `=`, `!=`, `<`, `<=`, `>`, `>=` |
| `OWNER`    | `owner_agent` (may be empty/absent) | text | `=`, `!=`, `IN`            |
| `TITLE`    | `title`                | text         | `=`, `!=`, `IN`                      |
| `KEY`      | item key, e.g. `PB-54` | text         | `=`, `!=`, `IN`                      |
| `CLOSED`   | `closed` (boolean)     | boolean      | `=`, `!=`                            |

No other field is queryable in v1. A field name not in the table → parse
error. An operator not allowed for the field (per §3 type rules) → parse
error (statically detectable; see §6).

---

## 3. Grammar (EBNF)

```ebnf
query      = or_expr ;

or_expr    = and_expr , { OR , and_expr } ;
and_expr   = not_expr , { AND , not_expr } ;
not_expr   = [ NOT ] , primary ;
primary    = "(" , or_expr , ")"
           | comparison ;

comparison = field , ( cmp_op , value
                     | IN , "(" , value , { "," , value } , ")" ) ;

cmp_op     = "=" | "!=" | "<" | "<=" | ">" | ">=" ;

field      = "CREATED" | "UPDATED" | "LABEL" | "KIND" | "PRIORITY"
           | "OWNER" | "TITLE" | "KEY" | "CLOSED" ;

value      = string | number | reltime | isotime | boolean ;

string     = "'" , { character | "''" } , "'" ;
number     = [ "-" ] , digit , { digit } , [ "." , digit , { digit } ] ;
reltime    = [ "-" | "+" ] , digit , { digit } , ( "d" | "h" | "m" | "w" ) ;
isotime    = date [ "T" , time , [ "Z" | ("+"/"-", hh, ":", mm) ] ] ;  (* see §4.2 *)
boolean    = "true" | "false" ;
```

Notes for implementers:

- `IN` requires **at least one** value in its list (`LABEL IN ()` is a parse
  error). A trailing comma before `)` is a parse error.
- `NOT` binds tighter than `AND`, which binds tighter than `OR`:
  `NOT a AND b OR c` ≡ `((NOT a) AND b) OR c`.
- `NOT` may stack (`NOT NOT CLOSED = true`) — double negation is legal.
- `NOT` applies to a primary, so `NOT LABEL = 'bug'` is "no label named bug";
  it is **not** field-level negation of the operator.
- Empty query (trimmed) is valid and matches all items.
- Tokens must be separated where required: `PRIORITY<=2` and `PRIORITY <= 2`
  are both fine (operators/punctuation self-delimit); `NOT(CLOSED = true)`
  is fine; `CREATED< -1d` is fine; but `NOTx` is a single unknown token.

---

## 4. Value semantics

### 4.1 Relative time (`RELTIME`)

Form: `[-+]?<digits><unit>` where `<unit>` is one of:

| Unit | Meaning |
|------|---------|
| `d`  | days (24h) |
| `h`  | hours |
| `m`  | minutes |
| `w`  | weeks (7d) |

A relative time denotes the **instant** `now + offset` computed at evaluation
time (per request, single `now` for the whole query — evaluate `now` once, do
not re-read the clock per comparison). Examples: `-1d` = 24 hours ago,
`-2h` = 2 hours ago, `-30m` = 30 minutes ago, `-2w` = 14 days ago. A positive
offset (e.g. `1d`) means the future instant `now + 1d` — legal grammar, rarely
useful.

`-0d` is valid and equals `now`. Units do not compound: `-1d30m` is a parse
error. `m` is minutes (not months).

### 4.2 ISO-8601 dates & RFC3339 timestamps (`ISOTIME`)

Two accepted forms:

1. **Date only**: `YYYY-MM-DD` (exactly; 4-2-2 digits, `-` separators).
   Denotes midnight UTC (`T00:00:00Z`) of that calendar date.
2. **RFC3339 timestamp**: `YYYY-MM-DDTHH:MM:SS[.fffffff…][Z|±HH:MM]`.
   Fractional seconds (any number of digits ≥ 1) are allowed. `T` must be
   uppercase `T` (accept lowercase `t`? — **no**, uppercase only in v1).
   A timestamp with **no** zone suffix is interpreted as **UTC**.

Out-of-range values (month 13, day 32, hour 25) → parse error at the offending
character. Leap seconds (`:60`) → parse error. `24:00:00` → parse error.

### 4.3 Numbers

Integers or decimals. For `PRIORITY` the value is compared numerically
against the item's `priority` (integer 0..3). Decimal values are allowed by
the grammar (`PRIORITY < 1.5` is valid and well-defined — an integer priority
compares fine against a decimal literal). For implementation, parse into a
suitable numeric type (`big.Rat`, `float64` is acceptable here since the
domain is tiny integers and comparisons are order-only).

### 4.4 Booleans

`true` / `false`, lowercase only. Used for `CLOSED`.

---

## 5. Type rules & comparison semantics

These rules are **static** — checked at parse time, so violations are parse
errors surfaced as HTTP 400 with position (§6). There are no runtime type
errors.

### 5.1 Time fields (`CREATED`, `UPDATED`)

- Only `=`, `!=`, `<`, `<=`, `>`, `>=`.
- The right-hand side must be a **time value**: `RELTIME` or `ISOTIME`.
  `CREATED < -1d` ✅; `CREATED < 5` ❌ (parse error); `CREATED = '2026-10-01'` ❌
  (quoted strings are never time values — use unquoted ISO).
- `IN` is not allowed on time fields.
- Semantics: instant-to-instant comparison in UTC. `CREATED < X` = created
  strictly before instant X. Example: `CREATED < -1d` matches items whose
  `created_at` is before `now - 24h`. `UPDATED >= 2026-10-04` matches items
  updated at or after 2026-10-04T00:00:00Z.

### 5.2 Text fields (`TITLE`, `OWNER`, `KEY`, `KIND`)

- Only `=`, `!=`, `IN`.
- The right-hand side must be `STRING` values only. `TITLE = 5` ❌; mixing
  types inside an `IN` list (`KIND IN ('task', 3)`) ❌.
- `=` is **exact, case-insensitive** equality (case folding per simple
  Unicode case-mapping, Go `strings.EqualFold` is the reference). `TITLE =
  'Wire Request Auth'` matches title `wire request auth`.
- `!=` is the negation of that: the item's value case-insensitively differs
  from the literal. For `LABEL` (a set) see §5.4.
- **No substring/wildcard/full-text matching in v1** (non-goal, §8).
  `TITLE = 'wire'` matches only items titled exactly `wire` (case-insensitive),
  not `wire request auth`.
- `KIND` values are the domain `milestone`, `epic`, `task`. The grammar does
  not restrict the literal; `KIND = 'story'` is a **valid query that matches
  no items** (empty result, not an error). KISS: no domain validation.
- `OWNER` compares against `owner_agent`. An item with no owner has the empty
  string as its value: `OWNER = ''` matches ownerless items; `OWNER != ''`
  matches owned ones.

### 5.3 Number fields (`PRIORITY`)

- All of `=`, `!=`, `<`, `<=`, `>`, `>=`. `IN` is not allowed.
- Right-hand side must be a `NUMBER`. `PRIORITY = 'high'` ❌.
- Semantics: numeric comparison against `priority` (0=P0 blocker … 3=P3
  low). `PRIORITY <= 1` matches P0 and P1 items. A literal outside `0..3`
  (e.g. `PRIORITY = 7`) is a valid query matching nothing — not an error.
  `PRIORITY` has no NULL; it defaults to 2.

### 5.4 `LABEL` (set-valued)

- Only `=`, `!=`, `IN`.
- `LABEL = 'bug'` — **true iff ANY** of the item's labels case-insensitively
  equals `bug`.
- `LABEL != 'bug'` — the negation: **no** label equals `bug` (vacuously true
  for items with no labels).
- `LABEL IN ('bug', 'feat')` — true iff any label equals any listed value.
  (So `LABEL IN (x)` ≡ `LABEL = x`; both forms are allowed.)
- Right-hand side: `STRING` values only.
- Label comparison uses the label **name**, never color or id.

### 5.5 Boolean field (`CLOSED`)

- Only `=` and `!=`.
- Right-hand side must be `BOOL`. `CLOSED = 'true'` ❌ (quoted string, not
  boolean). `CLOSED < true` ❌ (no ordering on booleans).
- `CLOSED = true` matches closed items; `CLOSED = false` matches open ones;
  `!=` inverts. `closed` maps from `closed_at` being set/non-null.

### 5.6 NULLs

Every queryable property always has a defined comparison value in v1:
`OWNER` is the empty string when unset, `closed` is a boolean, `updated_at`
equals `created_at` until first update. There is **no SQL-style NULL
three-valued logic** — every comparison is true or false. (Implementers
backing the evaluator with SQL must map `OWNER = ''` to `owner_agent = '' OR
owner_agent IS NULL`.)

---

## 6. Error model

All lexical, syntactic and static-semantic failures are **parse errors**
reported to the caller as HTTP `400` with the standard error envelope:

```json
{"error":{"code":"validation","message":"<message>"}}
```

(Transport tickets PB-58/PB-59 map this identically over REST and MCP.)

A parse error carries:

- **position**: a **byte offset** (0-based) into the query string plus
  **line** and **column** (1-based; line = count of `\n` before offset + 1,
  column = offset − last line start + 1, counting bytes not runes);
- a human-readable **message**.

The `message` format is:

```
parse error at <line>:<col> (offset <n>): <description>
```

Rules:

- The **position is relative to the TRIMMED query string**: transports trim
  leading/trailing whitespace per §1.1 before parsing, so an offset of 0 is
  the first non-whitespace character of the query as sent (PB-58 review note).
- Report the **first** error only; abort at the offending token.
- The position points at the **first character of the offending token**
  (for an unexpected end of input, offset = length of the query, line/col
  accordingly).
- Suggested messages (implementers should use these shapes so tests can
  match):
  - unknown token: `unexpected input` (then the token text is part of the
    description, e.g. `unexpected input 'created'` — hint: field names are
    uppercase);
  - unexpected end of input: `unexpected end of query`;
  - missing operator: `expected operator after FIELD`;
  - missing value: `expected value after operator`;
  - unterminated string: `unterminated string literal`;
  - type mismatch: `operator '<op>' not allowed for field '<FIELD>'` or
    `field '<FIELD>' requires a <time|number|string|boolean> value`;
  - unmatched parenthesis: `expected ')' to match '(' at <line>:<col>`;
  - empty IN list: `IN requires at least one value`;
  - mixed IN list: `IN list values must have the same type`.

Worked examples:

| Query                              | Result                                                                                             |
|------------------------------------|----------------------------------------------------------------------------------------------------|
| `CREATED < -1d AND LABEL IN ('bug')` | ✅ valid                                                                                           |
| `created < -1d`                    | ❌ `parse error at 1:1 (offset 0): unexpected input 'created'`                                      |
| `CREATED <`                        | ❌ `parse error at 1:10 (offset 9): unexpected end of query`                                        |
| `PRIORITY = 'high'`                | ❌ `parse error at 1:12 (offset 11): field 'PRIORITY' requires a number value`                     |
| `TITLE = 'wire`                    | ❌ `parse error at 1:9 (offset 8): unterminated string literal`                                    |
| `CLOSED < true`                    | ❌ `parse error at 1:8 (offset 7): operator '<' not allowed for field 'CLOSED'`                     |
| `LABEL IN ()`                      | ❌ `parse error at 1:10 (offset 9): IN requires at least one value`                                |
| `(CREATED < -1d`                   | ❌ `parse error at 1:15 (offset 14): expected ')' to match '(' at 1:1`                              |

(Exact offsets may shift by ±0; the contract is: correct field, correct
token, first error wins, message includes the position. PB-55's tests should
pin these examples.)

Malformed but syntactically fine queries (e.g. `KIND = 'story'`) are **not**
errors — they evaluate to empty results.

---

## 7. Evaluation semantics

### 7.1 Scope & result set

- A query is evaluated over the items of **one board** (the board the search
  endpoint is called on). It never spans boards or workspaces. Fields
  reflect the item view used by board snapshots (`key`, `title`,
  `owner_agent`, `priority`, `kind`, labels, `closed`, `created_at`,
  `updated_at`).
- **Archived items are excluded** by default, exactly like `GET
  /api/boards/{id}`; transport layers (PB-58) may offer the existing
  `include_archived=true` opt-in alongside `q`.
- `now` is fixed once per query evaluation (server-side clock, UTC).
- Result **ordering** matches the board snapshot: column position, then item
  `position` (numeric), then `created_at`. Search never reorders.

**Service integration (PB-57).** The query is evaluated in memory over the
full board snapshot (`GET /api/boards/{id}?q=…`): items are filtered in place
preserving board order, and each column's `item_count` is derived from the
**filtered** set (what you see is what's counted). The column keyset
pagination endpoint (`/api/boards/{id}/items`) is **not used** for queried
views and gains no `q` support; the parse happens once per request at the
service boundary (`search.Parse` at the transport seam, one `now` per
evaluation), and archived items keep the snapshot's default exclusion.

### 7.2 Truth evaluation

- Standard boolean logic: `AND`/`OR` short-circuiting, `NOT` negation.
- Every comparison yields exactly `true` or `false` (no NULL — §5.6).
- `NOT`/`AND`/`OR` apply to comparisons or parenthesized groups only; a
  bare field or value is not a truthy expression (`AND LABEL` is a parse
  error).

### 7.3 Worked examples

Assume "now" = `2026-10-05T12:00:00Z`:

| Query                                                     | Matches                                                                                                     |
|-----------------------------------------------------------|--------------------------------------------------------------------------------------------------------------|
| `CREATED < -1d AND LABEL IN ('bug')`                       | items created before 2026-10-04T12:00:00Z with a label `bug` (case-insensitive)                             |
| `PRIORITY <= 1 AND NOT CLOSED = true`                      | open P0/P1 items                                                                                            |
| `(KIND = 'epic' OR KIND = 'milestone') AND UPDATED >= -2w`  | epics and milestones updated on/after 2026-09-21T12:00:00Z (within the last 14 days)            |
| `OWNER = 'worker-spec' AND CREATED >= 2026-10-01`           | my items created since Oct 1 UTC midnight                                                                  |
| `CLOSED = false AND LABEL != 'wontfix'`                    | open items that carry no `wontfix` label (also matches unlabeled items)                                     |
| `KEY = 'pb-54'`                                            | the single item PB-54 (keys are uppercase; comparison is case-insensitive)                                   |
| `NOT (TITLE = '' OR OWNER = '')`                            | items with a non-empty title and an owner (title is never empty by API rules, so effectively: owned items)  |

---

## 8. Non-goals (v1)

- **No full-text / substring / wildcard matching** — `TITLE` is exact
  (case-insensitive) only; `description` and comment bodies are not
  queryable at all.
- **No cross-board / cross-workspace search** — one board per query.
- **No saved searches, no named filters, no query persistence.**
- **No `CONTAINS`, regex, `LIKE`, or fuzzy operators.**
- **No arithmetic**, no date arithmetic beyond the relative-time literals
  (`CREATED < -1d` yes; `CREATED < -1d - 2h` no).
- **No compound `IN` on time or number fields** (single comparison only).
- **No SQL injection surface**: the language is parsed into an AST and either
  evaluated in memory or compiled to **parameterized** SQL only — never string
  interpolation (house rule, see cookbook).
- **No case-insensitive keywords**: `and`/`And`/`in`/`not` are not keywords.
- **No localization of error messages** in v1.
- Relevance scoring / ranking: out of scope; results keep board order.

## 9. Implementation notes (non-normative)

- Suggested packages (Go): hand-rolled lexer + recursive-descent parser
  matching the EBNF in §3 (no parser generators needed at this size);
  AST nodes `Or`, `And`, `Not`, `Cmp{Field, Op, Value}`, `In{Field, Values}`;
  `time.Time` for instants (`time.Now().UTC()` once per evaluation).
- Evaluate `NOT` as logical negation, not operator rewriting.
- The static type-check in §5 can run during parsing (field+operator known
  at each comparison) — one pass, errors at the right token.
- PB-56 (in-memory evaluator) should ship a table-driven test suite using
  the examples in §6 and §7.3 verbatim.

## 10. Transport usage (normative pointers)

The query language is transport-agnostic; the transports fix the plumbing:

- **REST**: `GET /api/boards/{id}?q=<query>` returns the **searched view** —
  the full composite snapshot with only the matching `items`, board order
  preserved, and every column's `item_count` derived from the filtered set
  (spec §7.1). `include_items` is **ignored under `q`** (a queried view is
  snapshot-only: the per-column keyset endpoint silently ignores `q`);
  `include_archived` composes with `q`. A query that fails to parse → `400`
  with the standard error envelope and the §6 message verbatim; a
  **syntactically valid** query that matches nothing → `200` with empty
  items. Lookup order: the parse happens at the transport seam **before** the
  board lookup, so a parse error wins over a missing board, while a valid
  query on a nonexistent board still yields `not_found` (PB-59 review note).
- **MCP**: `get_board` takes an optional `query` string with identical
  semantics; an invalid query surfaces as a tool error, code `validation`,
  with the §6 message verbatim (show it to the caller as-is).

See `docs/api.md` (board snapshot section, MCP mapping table) for the full
request shapes; this section only pins the search-specific contract.

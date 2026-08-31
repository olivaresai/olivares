// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgcontent

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// identPattern is a safe, unquoted SQL identifier: a leading letter/underscore then
// letters/digits/underscore/$, up to PostgreSQL's 63-byte NAMEDATALEN limit. An
// identifier that matches can be double-quoted and interpolated into SQL with no
// injection surface (it cannot contain a quote, space, semicolon or comment).
var identPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// validIdent reports whether s is a safe SQL identifier to interpolate (quoted).
func validIdent(s string) bool {
	return len(s) >= 1 && len(s) <= 63 && identPattern.MatchString(s)
}

// quoteIdent double-quotes a PRE-VALIDATED identifier. Callers MUST have checked
// validIdent first (parseConfig does); it panics on an unvalidated identifier rather
// than emit unsafe SQL, so a code path that forgets the check fails loudly in tests.
func quoteIdent(s string) string {
	if !validIdent(s) {
		panic("pgcontent: quoteIdent on unvalidated identifier: " + s)
	}
	return `"` + s + `"`
}

// qualified returns the double-quoted schema.table for a validated config.
func (sc *sourceConfig) qualified() string {
	return quoteIdent(sc.schema) + "." + quoteIdent(sc.table)
}

// ErrNotReadOnly is returned by ValidateReadOnlyQuery for anything that is not a
// single read-only SELECT.
var ErrNotReadOnly = errors.New("query is not a single read-only SELECT")

// writeKeywords are the data-modifying / DDL / side-effecting statement keywords a
// read-only content query may never contain — including inside a CTE (WITH x AS
// (DELETE …)). The list is intentionally conservative and fail-closed: it favors
// rejecting a benign query that names a column after a SQL command over admitting a
// write. (A column literally named e.g. "update" must be aliased in the query.)
var writeKeywords = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|MERGE|UPSERT|TRUNCATE|DROP|CREATE|ALTER|GRANT|REVOKE|COPY|VACUUM|REINDEX|REFRESH|CLUSTER|CALL|EXECUTE|PREPARE|DEALLOCATE|LISTEN|NOTIFY|INTO|IMPORT|LOAD|LO_IMPORT|LO_EXPORT|PG_WRITE_FILE|DBLINK_EXEC|SETVAL)\b`)

// commentStrip removes -- line comments and /* */ block comments so a comment cannot
// hide a write keyword or a second statement from the guard.
var (
	lineComment  = regexp.MustCompile(`--[^\n]*`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// ValidateReadOnlyQuery is the connector-side read-only guard for an operator-
// supplied query. It is one of THREE independent read-only layers (see doc.go); it
// exists so the connector never even ATTEMPTS a non-read statement. It rejects, fail-
// closed: a query that does not begin with SELECT or WITH; any statement separator
// (a second statement); and any data-modifying / DDL / side-effecting keyword,
// including inside a CTE. Comments are stripped first so they cannot hide either.
func ValidateReadOnlyQuery(q string) error {
	stripped := blockComment.ReplaceAllString(q, " ")
	stripped = lineComment.ReplaceAllString(stripped, " ")
	stripped = strings.TrimSpace(stripped)
	if stripped == "" {
		return fmt.Errorf("%w: empty query", ErrNotReadOnly)
	}
	// A single optional trailing semicolon is allowed; any other semicolon means a
	// second statement (e.g. "SELECT 1; DELETE FROM t").
	stripped = strings.TrimRight(stripped, "; \t\r\n")
	if strings.Contains(stripped, ";") {
		return fmt.Errorf("%w: multiple statements are not allowed", ErrNotReadOnly)
	}
	// Must be a read statement: SELECT, or WITH (a read CTE feeding a SELECT). A
	// leading paren is allowed: "(SELECT …)".
	head := strings.ToUpper(strings.TrimLeft(stripped, "( \t\r\n"))
	if !strings.HasPrefix(head, "SELECT") && !strings.HasPrefix(head, "WITH") {
		return fmt.Errorf("%w: must start with SELECT or WITH", ErrNotReadOnly)
	}
	if loc := writeKeywords.FindString(stripped); loc != "" {
		return fmt.Errorf("%w: contains the disallowed keyword %q", ErrNotReadOnly, strings.ToUpper(loc))
	}
	return nil
}

// --- SELECT-only query builders ---------------------------------------------
// These are the ONLY statements the live path executes. Each begins with SELECT and
// passes ValidateReadOnlyQuery, so the connector cannot construct a write. Row
// filters/cursors are ALWAYS bound parameters ($1, …); only validated identifiers
// are interpolated.

// rowSource returns the FROM target: the qualified table, or the operator's
// validated query wrapped as a subquery so the built projection/cursor/limit apply on
// top of it.
func (sc *sourceConfig) rowSource() string {
	if sc.query != "" {
		return "(" + sc.query + ") AS pgc_src"
	}
	return sc.qualified()
}

// projection is the double-quoted, de-duplicated column list Fetch selects (every
// mapping column, including the body).
func (sc *sourceConfig) projection() string {
	return quoteList(sc.selectColumns())
}

// refProjection is the light column list List/DeltaList select — only what a DocRef
// needs (key + title + updated-at) — so paging a large table never buffers every
// row's body; Fetch pulls the full row lazily.
func (sc *sourceConfig) refProjection() string {
	seen := map[string]bool{}
	var cols []string
	add := func(cs ...string) {
		for _, c := range cs {
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			cols = append(cols, c)
		}
	}
	add(sc.keyColumns...)
	add(sc.titleColumn)
	add(sc.updatedAtCol)
	return quoteList(cols)
}

// quoteList double-quotes each validated identifier and joins them.
func quoteList(cols []string) string {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
	}
	return strings.Join(quoted, ", ")
}

// orderBy is the deterministic ordering for stable pagination: the key columns.
func (sc *sourceConfig) orderBy() string {
	quoted := make([]string, len(sc.keyColumns))
	for i, c := range sc.keyColumns {
		quoted[i] = quoteIdent(c)
	}
	return strings.Join(quoted, ", ")
}

// buildListSQL builds the full-list page query: a deterministic SELECT over all rows,
// ordered by the key, with LIMIT/OFFSET keyset by the integer offset. It never
// contains a write.
func (sc *sourceConfig) buildListSQL(offset, limit int) string {
	return fmt.Sprintf(
		"SELECT %s FROM %s ORDER BY %s LIMIT %d OFFSET %d",
		sc.projection(), sc.rowSource(), sc.orderBy(), limit, offset,
	)
}

// buildDeltaSQL builds the incremental page query: rows whose updated_at cursor is
// strictly greater than the bound $1, ordered by the cursor then key for a stable
// resume point. Requires an updated_at column (the caller checks).
func (sc *sourceConfig) buildDeltaSQL(limit int) string {
	cur := quoteIdent(sc.updatedAtCol)
	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s > $1 ORDER BY %s, %s LIMIT %d",
		sc.projection(), sc.rowSource(), cur, cur, sc.orderBy(), limit,
	)
}

// buildFetchSQL builds the single-row lookup by the (composite) key, each key column
// bound to $1..$N. It never contains a write.
func (sc *sourceConfig) buildFetchSQL() string {
	conds := make([]string, len(sc.keyColumns))
	for i, c := range sc.keyColumns {
		conds[i] = quoteIdent(c) + " = $" + strconv.Itoa(i+1)
	}
	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s LIMIT 1",
		sc.projection(), sc.rowSource(), strings.Join(conds, " AND "),
	)
}

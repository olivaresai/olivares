// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgcontent

import (
	"strings"
	"testing"
)

// TestReadOnlyGuardRejectsWrites is the core read-only proof: the guard rejects every
// write / DDL / side-effecting form an operator-supplied query could take, including
// the ones that try to hide behind a leading SELECT (CTE writes), a second statement,
// or a comment. Fail-closed is the contract: an ambiguous query is rejected.
func TestReadOnlyGuardRejectsWrites(t *testing.T) {
	writes := []string{
		"INSERT INTO t VALUES (1)",
		"insert into t values (1)", // case-insensitive
		"UPDATE t SET x = 1",
		"DELETE FROM t",
		"MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE",
		"TRUNCATE t",
		"DROP TABLE t",
		"ALTER TABLE t ADD COLUMN x int",
		"CREATE TABLE t (x int)",
		"GRANT SELECT ON t TO r",
		"REVOKE SELECT ON t FROM r",
		"COPY t TO '/tmp/x'",
		"COPY t FROM '/tmp/x'",
		"VACUUM t",
		"REINDEX TABLE t",
		"REFRESH MATERIALIZED VIEW v",
		"CALL p()",
		"DO $$ BEGIN PERFORM 1; END $$",
		"SELECT 1 INTO newtable",         // SELECT INTO creates a table
		"SELECT * FROM t; DELETE FROM t", // second statement
		"SELECT * FROM t; DROP TABLE t;", // second statement (trailing ;)
		"WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x",     // data-modifying CTE
		"WITH x AS (UPDATE t SET a=1 RETURNING *) SELECT * FROM x",  // data-modifying CTE
		"WITH x AS (INSERT INTO t VALUES (1) RETURNING *) SELECT 1", // data-modifying CTE
		"SELECT 1 /* hi */ ; DROP TABLE t",                          // second statement w/ comment
		"/* leading */ DROP TABLE t",                                // comment then DDL
		"SELECT lo_export(1, '/tmp/x')",                             // server-side file write fn
		"SELECT pg_write_file('/tmp/x', 'y')",                       // server-side file write fn
		"SELECT setval('s', 1)",                                     // sequence write
		"EXECUTE stmt",
		"PREPARE stmt AS SELECT 1",
		"NOTIFY chan",
		"", // empty
		"   ",
	}
	for _, q := range writes {
		if err := ValidateReadOnlyQuery(q); err == nil {
			t.Errorf("guard admitted a non-read-only query: %q", q)
		}
	}
}

// TestReadOnlyGuardAcceptsReads confirms the guard does not over-reject legitimate
// read queries (SELECT, read CTEs, joins, subqueries, a trailing semicolon).
func TestReadOnlyGuardAcceptsReads(t *testing.T) {
	reads := []string{
		"SELECT * FROM t",
		"select id, body from public.docs where tenant = 'x'",
		"SELECT * FROM t ORDER BY id LIMIT 100 OFFSET 200",
		"WITH recent AS (SELECT * FROM t WHERE updated_at > now() - interval '1 day') SELECT * FROM recent",
		"SELECT a.*, b.name FROM a JOIN b ON a.bid = b.id",
		"SELECT * FROM t WHERE id IN (SELECT id FROM other)",
		"(SELECT 1)",
		"SELECT 1;", // a single trailing semicolon is fine
		"SELECT /* pick */ id FROM t -- trailing comment\n",
	}
	for _, q := range reads {
		if err := ValidateReadOnlyQuery(q); err != nil {
			t.Errorf("guard rejected a legitimate read query %q: %v", q, err)
		}
	}
}

// TestValidIdent checks the identifier safety gate that stops config-driven SQL
// injection through schema/table/column names.
func TestValidIdent(t *testing.T) {
	valid := []string{"users", "user_group", "_x", "a1", "col$1", strings.Repeat("a", 63)}
	for _, s := range valid {
		if !validIdent(s) {
			t.Errorf("expected %q to be a valid identifier", s)
		}
	}
	invalid := []string{"", "1abc", "a b", "a;b", "a-b", "a.b", `a"b`, "a'b", "drop table", strings.Repeat("a", 64)}
	for _, s := range invalid {
		if validIdent(s) {
			t.Errorf("expected %q to be an INVALID identifier", s)
		}
	}
}

// TestQuoteIdentPanicsOnUnvalidated ensures a code path that forgot validIdent fails
// loudly rather than emitting unsafe SQL.
func TestQuoteIdentPanicsOnUnvalidated(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected quoteIdent to panic on an unvalidated identifier")
		}
	}()
	_ = quoteIdent("a; DROP TABLE t")
}

// TestBuildersAreSelectOnly is the "read-only by construction" proof for the live
// path: every query the connector BUILDS begins with SELECT and passes the read-only
// guard, so the connector cannot construct a write regardless of configuration.
func TestBuildersAreSelectOnly(t *testing.T) {
	sc := &sourceConfig{
		schema:       "public",
		table:        "docs",
		keyColumns:   []string{"id"},
		bodyColumns:  []string{"body"},
		titleColumn:  "title",
		updatedAtCol: "updated_at",
		aclColumns:   []string{"owner_group"},
		classColumn:  "classification",
		sensitiveCol: []string{"ssn"},
		metadataCol:  []string{"url"},
	}
	built := []string{
		sc.buildListSQL(0, 100),
		sc.buildDeltaSQL(100),
		sc.buildFetchSQL(),
	}
	for _, q := range built {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(q)), "SELECT") {
			t.Errorf("built query is not a SELECT: %q", q)
		}
		if err := ValidateReadOnlyQuery(q); err != nil {
			t.Errorf("built query failed the read-only guard %q: %v", q, err)
		}
	}
}

// TestBuildersWithOperatorQueryAreSelectOnly proves that even when the operator
// supplies a query, the wrapped statements the connector executes stay SELECT-only.
func TestBuildersWithOperatorQueryAreSelectOnly(t *testing.T) {
	sc := &sourceConfig{
		schema:       "public",
		query:        "SELECT id, body, updated_at FROM v_docs WHERE active",
		keyColumns:   []string{"id"},
		bodyColumns:  []string{"body"},
		updatedAtCol: "updated_at",
	}
	for _, q := range []string{sc.buildListSQL(0, 50), sc.buildDeltaSQL(50), sc.buildFetchSQL()} {
		if err := ValidateReadOnlyQuery(q); err != nil {
			t.Errorf("wrapped operator-query build failed the guard %q: %v", q, err)
		}
	}
}

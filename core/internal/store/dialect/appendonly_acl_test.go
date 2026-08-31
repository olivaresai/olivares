// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// The append-only ACL is the ONLY defense against TRUNCATE on an append-only table:
// the immutability trigger is BEFORE UPDATE OR DELETE ... FOR EACH ROW, and TRUNCATE
// is a statement-level operation no row trigger can see. Until this file there was no
// test anywhere — server-gated or not — asserting anything about the statement that
// carries it, which is how it went unnoticed that the statement named a compile-time
// constant while `db init --app-role` accepted any name.

// TestAppendOnlyRevokeTargetsTheEffectiveRole is the RED for the reported defect: the
// revoke must name the role the application actually connects as, not the default.
func TestAppendOnlyRevokeTargetsTheEffectiveRole(t *testing.T) {
	t.Parallel()
	dia, ok := NewForAppRole(store.EnginePostgres, "tenant_svc")
	if !ok {
		t.Fatal("NewForAppRole(postgres) returned !ok")
	}
	stmts := dia.AuditTableStmts()
	revoke := findRevoke(t, stmts)
	if !strings.Contains(revoke, "target text := $olv$tenant_svc$olv$") {
		t.Errorf("the append-only revoke does not target the effective role.\ngot: %s", revoke)
	}
	if strings.Contains(revoke, DefaultAppRole) {
		t.Errorf("the revoke still names the default role %q, so a deployment using another role keeps TRUNCATE on the ledger.\ngot: %s", DefaultAppRole, revoke)
	}
}

// TestAppendOnlyRevokeUsesTheDefaultOnlyWhenItIsCHOSEN pins what New does — and, since
// C4-12, what NewForAppRole refuses.
//
// THIS TEST WAS INVERTED, and the inversion is the finding. It used to assert that
// `NewForAppRole(postgres, "")` ALSO produced the conventional default, on the reasoning
// that an empty target "would make the existence gate silently false — the exact no-op this
// work removes". Half of that reasoning was right and the half that mattered was wrong: a
// GUESSED target makes the same gate silently false whenever the guess is not the role in
// use. Measured on 17.10 — the v6 control-plane revoke aimed at an absent `olivares_app`
// returns DO, i.e. success, while the real application role keeps INSERT on the guard
// ledger; with the target corrected to the real role the same block leaves f|f|f and the
// INSERT is denied.
//
// So "nobody could read the role" and "the role is the conventional default" are different
// facts, and the constructor is the wrong place to convert the first into the second. New
// still yields the default because its caller CHOSE it by name; an empty role is refused,
// and the boot path that would have supplied one now fails closed with a message saying so.
func TestAppendOnlyRevokeUsesTheDefaultOnlyWhenItIsCHOSEN(t *testing.T) {
	t.Parallel()

	revoke := findRevoke(t, mustDialect(t, store.EnginePostgres).AuditTableStmts())
	if !strings.Contains(revoke, "target text := $olv$"+DefaultAppRole+"$olv$") {
		t.Errorf("New: expected the revoke to target %q.\ngot: %s", DefaultAppRole, revoke)
	}

	if _, ok := NewForAppRole(store.EnginePostgres, ""); ok {
		t.Error("NewForAppRole(postgres, \"\") built a dialect. An unknown role must be REFUSED, not " +
			"defaulted: every revoke this dialect renders is gated on its target existing, so a name " +
			"nobody uses is a statement that succeeds and protects nobody")
	}
	// SQLite has no role layer — its triggers apply to every connection — so an empty role
	// is meaningless there rather than dangerous, and refusing it would be a regression.
	if _, ok := NewForAppRole(store.EngineSQLite, ""); !ok {
		t.Error("NewForAppRole(sqlite, \"\") must still build: SQLite has no role to get wrong")
	}
}

// TestAppendOnlyRevokeQuotesRoleAndTableSafely covers what stops being theoretical the
// moment the role name is runtime data instead of a constant: a name PostgreSQL
// accepts but Go must not paste into an identifier position. The name crosses into
// SQL once, inside a dollar-quoted string (where there are no escape sequences at
// all), and the identifier is quoted server-side by format('%I').
func TestAppendOnlyRevokeQuotesRoleAndTableSafely(t *testing.T) {
	t.Parallel()
	// A legal (if hostile) role name: quoted identifiers may contain a single quote.
	const hostile = `weird'; DROP TABLE audit_events; --`
	dia := mustRoleDialect(t, store.EnginePostgres, hostile)
	revoke := findRevoke(t, dia.AuditTableStmts())

	// Inside dollar quotes the quote has no power to end anything, so the name must
	// appear VERBATIM — no doubling, no escaping — wrapped in a tag it does not
	// contain. Anything else means it was routed through a quoting scheme whose
	// correctness depends on server settings (standard_conforming_strings).
	if !strings.Contains(revoke, "$olv$"+hostile+"$olv$") {
		t.Errorf("expected the role name carried verbatim inside a dollar-quoted string.\ngot: %s", revoke)
	}
	if !strings.Contains(revoke, "format('REVOKE UPDATE, DELETE, TRUNCATE ON %I FROM %I'") {
		t.Errorf("expected the identifiers to be quoted server-side via format('%%I').\ngot: %s", revoke)
	}
	// The gate must survive: REVOKE naming a role that does not exist is a hard
	// error, so dropping it would abort the migration on an unprovisioned database.
	if !strings.Contains(revoke, "FROM pg_roles WHERE rolname = target") {
		t.Errorf("the pg_roles existence gate is gone; an absent role would abort the migration.\ngot: %s", revoke)
	}
}

// TestAppendOnlyRevokeSurvivesADollarTagInTheRoleName is the regression for a real
// defect an earlier revision of this change shipped: the statement was built inside a
// FIXED $olv_ao$ dollar tag, with a comment asserting the tag could not be terminated
// early. `x$olv_ao$x` is a legal PostgreSQL role name, and it closed the block — so a
// perfectly ordinary least-privilege deployment stopped booting with a syntax error.
//
// Tags are now chosen so they do not occur in what they quote, at every level.
func TestAppendOnlyRevokeSurvivesADollarTagInTheRoleName(t *testing.T) {
	t.Parallel()
	for _, role := range []string{
		"x$olv$x",     // collides with the first tag tried
		"x$olv1$x",    // …and with the second the generator actually emits
		"$olv$$olv1$", // both, adjacent
		`back\slash`,  // no escapes inside dollar quotes, whatever standard_conforming_strings says
	} {
		dia := mustRoleDialect(t, store.EnginePostgres, role)
		revoke := findRevoke(t, dia.AuditTableStmts())
		tag := revoke[len("DO ") : len("DO ")+strings.Index(revoke[len("DO "):], "\n")]
		if strings.Count(revoke, tag) != 2 {
			t.Errorf("role %q: outer tag %q appears %d times, want exactly the opening and closing pair.\ngot: %s",
				role, tag, strings.Count(revoke, tag), revoke)
		}
		if !strings.Contains(revoke, role) {
			t.Errorf("role %q: the role name is not carried verbatim.\ngot: %s", role, revoke)
		}
	}
}

// pgScanDollarLiteral reproduces PostgreSQL's own rule for reading a dollar-quoted
// literal that starts at the beginning of s: the tag runs to the second `$`, nothing
// inside is escaped, and the literal ends at the FIRST reoccurrence of that tag. It
// returns what the server would store and what it would go on parsing as bare SQL.
//
// The tests need this because counting tags does not detect the defect below: the
// rendered block can carry a perfectly balanced pair of outer tags while the INNER
// literal closes a character early.
func pgScanDollarLiteral(s string) (content, rest string, ok bool) {
	if !strings.HasPrefix(s, "$") {
		return "", "", false
	}
	close := strings.Index(s[1:], "$")
	if close < 0 {
		return "", "", false
	}
	tag := s[:close+2]
	body := s[len(tag):]
	end := strings.Index(body, tag)
	if end < 0 {
		return "", "", false
	}
	return body[:end], body[end+len(tag):], true
}

// TestAppendOnlyRevokeLiteralsCannotCloseEarly is the regression for a defect this
// change introduced while fixing the previous one, and it is the same class of failure
// both times: a dollar tag chosen so that it does not occur INSIDE the value, which is
// not sufficient, because `$olvN$` opens and closes with the same character. A value
// whose tail is `$olvN` joins the closing delimiter and terminates the literal one
// character early.
//
// Measured on PostgreSQL 15.18 with this renderer before the fix:
//   - role `svc$olv`      -> the DO block runs with NO error and revokes from `svc`;
//     the real role keeps TRUNCATE (has_table_privilege = t). A silently ineffective
//     revoke — exactly what this whole change exists to eliminate.
//   - table `evidence$olv` -> `ERROR: syntax error at or near "olv$"`; the deployment
//     cannot boot.
//
// Counting tags cannot catch either case, so this asserts the only thing that matters:
// scanning the rendered literals the way the server does must give back the values
// verbatim, with nothing left over.
func TestAppendOnlyRevokeLiteralsCannotCloseEarly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, role, table string }{
		{"role tail is the tag less its final dollar", `svc$olv`, "audit_events"},
		{"role IS the tag less its final dollar", `$olv`, "audit_events"},
		{"role tail is the escalated tag less its final dollar", `x$olv1`, "audit_events"},
		{"table tail is the tag less its final dollar", DefaultAppRole, `evidence$olv`},
		{"table tail is the escalated tag", DefaultAppRole, `evidence$olv2`},
		{"both hostile", `r$olv`, `t$olv`},
		{"tag strictly inside the value", `x$olv$x`, "audit_events"},
	} {
		stmt := pgRevokeMutations(tc.table, tc.role)

		body, after, ok := pgScanDollarLiteral(strings.TrimPrefix(stmt, "DO "))
		if !ok {
			t.Errorf("%s: the outer DO block is not a well-formed dollar-quoted body.\ngot: %s", tc.name, stmt)
			continue
		}
		if after != "" {
			t.Errorf("%s: the outer literal closes early, leaving %q as bare SQL.\ngot: %s", tc.name, after, stmt)
		}

		const decl = "target text := "
		i := strings.Index(body, decl)
		if i < 0 {
			t.Errorf("%s: no role declaration in the rendered block.\ngot: %s", tc.name, stmt)
			continue
		}
		got, rest, ok := pgScanDollarLiteral(body[i+len(decl):])
		if !ok || got != tc.role {
			t.Errorf("%s: the server would read the role as %q, want %q.\ngot: %s", tc.name, got, tc.role, stmt)
		}
		if !strings.HasPrefix(rest, ";") {
			t.Errorf("%s: the role literal closes early, leaving %q before the statement end.\ngot: %s",
				tc.name, rest[:min(len(rest), 24)], stmt)
		}

		const fmtCall = "format('REVOKE UPDATE, DELETE, TRUNCATE ON %I FROM %I', "
		j := strings.Index(body, fmtCall)
		if j < 0 {
			t.Errorf("%s: no format() call in the rendered block.\ngot: %s", tc.name, stmt)
			continue
		}
		got, rest, ok = pgScanDollarLiteral(body[j+len(fmtCall):])
		if !ok || got != tc.table {
			t.Errorf("%s: the server would read the table as %q, want %q.\ngot: %s", tc.name, got, tc.table, stmt)
		}
		// Anything other than the next argument here is stray text inside an argument
		// list, where PostgreSQL allows no alias to absorb it: a hard parse error.
		if !strings.HasPrefix(rest, ", target)") {
			t.Errorf("%s: the table literal closes early, leaving %q inside format()'s arguments.\ngot: %s",
				tc.name, rest[:min(len(rest), 24)], stmt)
		}
	}
}

// TestAppendOnlyCatalogRevokeNamesItsSchema pins the second half of the same lesson:
// the provisioning repair must scan and revoke in the schema it is TOLD, not in
// whatever search_path a maintenance connection happens to carry — otherwise it
// inspects somewhere else and repairs nothing while reporting success.
func TestAppendOnlyCatalogRevokeNamesItsSchema(t *testing.T) {
	t.Parallel()
	stmt := AppendOnlyCatalogRevokeStmt("tenant_svc", "public")
	if strings.Contains(stmt, "current_schema()") {
		t.Errorf("the catalog repair resolves the schema from search_path.\ngot: %s", stmt)
	}
	for _, want := range []string{"$olv$public$olv$", "n.nspname = sch", "%I.%I FROM %I"} {
		if !strings.Contains(stmt, want) {
			t.Errorf("expected %q in the catalog repair.\ngot: %s", want, stmt)
		}
	}
}

// TestAppendOnlyACLStmtsCoverEveryTableAndSkipSQLite proves the re-assertion surface:
// one statement per table on Postgres (so a failure names its table), nothing at all
// on SQLite, which has no role layer and no TRUNCATE.
func TestAppendOnlyACLStmtsCoverEveryTableAndSkipSQLite(t *testing.T) {
	t.Parallel()
	pg := mustRoleDialect(t, store.EnginePostgres, "tenant_svc")
	tables := []string{"audit_events", "compliance_evidence", "knowledge_lineage"}
	stmts := pg.AppendOnlyACLStmts(tables)
	if len(stmts) != len(tables) {
		t.Fatalf("got %d statements for %d tables, want one each", len(stmts), len(tables))
	}
	for i, tbl := range tables {
		if !strings.Contains(stmts[i], "$olv$"+tbl+"$olv$") {
			t.Errorf("statement %d does not name table %q: %s", i, tbl, stmts[i])
		}
		if !strings.Contains(stmts[i], "$olv$tenant_svc$olv$") {
			t.Errorf("statement %d does not target the effective role: %s", i, stmts[i])
		}
	}
	if got := pg.AppendOnlyACLStmts(nil); got != nil {
		t.Errorf("no tables should render no statements, got %v", got)
	}
	lite := mustDialect(t, store.EngineSQLite)
	if got := lite.AppendOnlyACLStmts(tables); got != nil {
		t.Errorf("SQLite has no role layer; expected no ACL statements, got %v", got)
	}
}

func findRevoke(t *testing.T, stmts []string) string {
	t.Helper()
	for _, s := range stmts {
		if strings.Contains(s, "REVOKE UPDATE, DELETE, TRUNCATE") {
			return s
		}
	}
	t.Fatalf("no append-only revoke found in %d statements — the ACL leg is gone", len(stmts))
	return ""
}

func mustDialect(t *testing.T, engine store.Engine) Dialect {
	t.Helper()
	d, ok := New(engine)
	if !ok {
		t.Fatalf("New(%q) returned !ok", engine)
	}
	return d
}

func mustRoleDialect(t *testing.T, engine store.Engine, role string) Dialect {
	t.Helper()
	d, ok := NewForAppRole(engine, role)
	if !ok {
		t.Fatalf("NewForAppRole(%q, %q) returned !ok", engine, role)
	}
	return d
}

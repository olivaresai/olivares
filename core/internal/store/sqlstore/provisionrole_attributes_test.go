// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// These are the PLANNING and DIAGNOSTIC controls for the role stage: what statement it
// decides to issue, in what order it issues them, and what it is willing to say when
// one of them fails. They run without a server on purpose — the decision is a pure
// function of the desired class and the observed catalog row, and a control that needed
// PostgreSQL to prove "NOSUPERUSER is not named when the role is already NOSUPERUSER"
// would be skipped on every box that has no server, which is where most of this code is
// read and changed.
//
// The server-side half — that these statements are ACCEPTED or REFUSED by a real
// 16.15 under a real executor identity — is provisionrole_attributes_pg_test.go, and
// neither file substitutes for the other. Nothing here claims any behavior of
// ProvisionPostgres as a whole, of the database or grant stages, or of any major other
// than the one the sibling file measures.
//
// The expected SQL below is written out as LITERAL TEXT rather than assembled from the
// same helpers under test. Deriving it would make every assertion agree with any
// rendering the code happened to produce, including a wrong one.

// syntheticSecret stands in for a credential in the failure controls. It is not a
// password for anything: it exists so an assertion can prove the returned diagnostic
// does not carry it. Real generated credentials are never printed by these tests.
const syntheticSecret = "synthetic-not-a-credential-9d41f0"

// --- planning ---------------------------------------------------------------

// TestDesiredRoleAttrsIsClosed pins the two published sets to the postures they name,
// and pins the refusal for anything else. An attribute set that reached the server as
// arbitrary SQL, or that quietly became "no restricted attributes at all", would be a
// silently privileged application role.
func TestDesiredRoleAttrsIsClosed(t *testing.T) {
	t.Parallel()

	app, err := desiredRoleAttrs(attrsUnprivileged)
	if err != nil {
		t.Fatalf("attrsUnprivileged is not a known set: %v", err)
	}
	if want := (roleAttrs{Login: true}); app != want {
		t.Errorf("attrsUnprivileged posture = %+v, want %+v", app, want)
	}
	reader, err := desiredRoleAttrs(attrsAdmin)
	if err != nil {
		t.Fatalf("attrsAdmin is not a known set: %v", err)
	}
	if want := (roleAttrs{Login: true, BypassRLS: true}); reader != want {
		t.Errorf("attrsAdmin posture = %+v, want %+v", reader, want)
	}

	for _, unknown := range []string{"", "NOSUPERUSER", "SUPERUSER", attrsUnprivileged + " CREATEDB", strings.ToLower(attrsUnprivileged)} {
		if _, err := desiredRoleAttrs(unknown); err == nil {
			t.Errorf("desiredRoleAttrs(%q) was accepted; the controlled sets must be closed", unknown)
		}
	}
}

// TestTheClosedPosturesStillPrintTheirPublishedConstants ties the internal postures to
// the strings `db init --print-sql` shows an operator. If the two ever drift the plan
// stops describing the model the executor converges to, and nothing else would notice:
// the render path and the executor no longer share a code path.
func TestTheClosedPosturesStillPrintTheirPublishedConstants(t *testing.T) {
	t.Parallel()

	render := func(attrs string) string {
		posture, err := desiredRoleAttrs(attrs)
		if err != nil {
			t.Fatalf("desiredRoleAttrs(%q): %v", attrs, err)
		}
		words := make([]string, 0, len(convergeableRoleOptions))
		for _, o := range convergeableRoleOptions {
			words = append(words, o.word(o.Get(posture)))
		}
		return strings.Join(words, " ")
	}

	if got, want := render(attrsUnprivileged), "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION"; got != want {
		t.Errorf("application/owner posture renders %q, want %q", got, want)
	}
	if got := render(attrsUnprivileged); got != attrsUnprivileged {
		t.Errorf("application/owner posture renders %q but the published constant is %q", got, attrsUnprivileged)
	}
	if got, want := render(attrsAdmin), "NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION"; got != want {
		t.Errorf("runtime reader posture renders %q, want %q", got, want)
	}
	if got := render(attrsAdmin); got != attrsAdmin {
		t.Errorf("runtime reader posture renders %q but the published constant is %q", got, attrsAdmin)
	}
}

// TestCreateRoleSQLIsNologinAndCarriesNoPassword is the F4 shape: creation names every
// desired attribute but NOT the credential, so a refused creation never carries a
// password literal to a server that logs failed statements.
func TestCreateRoleSQLIsNologinAndCarriesNoPassword(t *testing.T) {
	t.Parallel()

	app, _ := desiredRoleAttrs(attrsUnprivileged)
	reader, _ := desiredRoleAttrs(attrsAdmin)

	if got, want := createRoleSQL("olv_app", app), "CREATE ROLE olv_app WITH NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION"; got != want {
		t.Errorf("createRoleSQL(app)\n got %q\nwant %q", got, want)
	}
	if got, want := createRoleSQL("olv_reader", reader), "CREATE ROLE olv_reader WITH NOLOGIN NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION"; got != want {
		t.Errorf("createRoleSQL(reader)\n got %q\nwant %q", got, want)
	}
	// Token-wise, because "NOLOGIN" contains "LOGIN": what must be absent is the
	// WORD LOGIN and the word PASSWORD, not a substring of the negated option.
	for _, sqlText := range []string{createRoleSQL("olv_app", app), createRoleSQL("olv_reader", reader)} {
		for _, tok := range strings.Fields(sqlText) {
			if tok == "LOGIN" || tok == "PASSWORD" {
				t.Errorf("creation statement names %q, so it is a usable login or carries a credential: %q", tok, sqlText)
			}
		}
	}
}

// TestRoleCredentialTemplateIsIdentifierLoginAndPassword pins the credential step to
// exactly three things. A template that grew an attribute would put a privileged option
// back on the statement that carries the password.
func TestRoleCredentialTemplateIsIdentifierLoginAndPassword(t *testing.T) {
	t.Parallel()

	if got, want := roleCredentialTemplate("olv_app"), "ALTER ROLE olv_app WITH LOGIN PASSWORD %L"; got != want {
		t.Errorf("roleCredentialTemplate\n got %q\nwant %q", got, want)
	}
}

// TestAlterRoleAttributesSQLNamesOnlyRealDrift is the heart of the repair. Every case
// is an independent literal: the observed catalog row on the left, the statement the
// stage is allowed to issue on the right.
func TestAlterRoleAttributesSQLNamesOnlyRealDrift(t *testing.T) {
	t.Parallel()

	appConverged := roleAttrs{Login: true}
	readerConverged := roleAttrs{Login: true, BypassRLS: true}

	cases := []struct {
		name  string
		class string
		have  roleAttrs
		want  string
	}{
		{
			name:  "application role with no drift issues LOGIN and nothing else",
			class: attrsUnprivileged, have: appConverged,
			want: "ALTER ROLE olv_app WITH LOGIN",
		},
		{
			name:  "runtime reader with no drift keeps BYPASSRLS unnamed",
			class: attrsAdmin, have: readerConverged,
			want: "ALTER ROLE olv_app WITH LOGIN",
		},
		{
			name:  "observed NOLOGIN converges through the LOGIN that is always there",
			class: attrsUnprivileged, have: roleAttrs{},
			want: "ALTER ROLE olv_app WITH LOGIN",
		},
		{
			name:  "superuser drift",
			class: attrsUnprivileged, have: roleAttrs{Login: true, Superuser: true},
			want: "ALTER ROLE olv_app WITH LOGIN NOSUPERUSER",
		},
		{
			name:  "bypassrls drift on an application role",
			class: attrsUnprivileged, have: roleAttrs{Login: true, BypassRLS: true},
			want: "ALTER ROLE olv_app WITH LOGIN NOBYPASSRLS",
		},
		{
			name:  "bypassrls drift on a runtime reader is the other direction",
			class: attrsAdmin, have: roleAttrs{Login: true},
			want: "ALTER ROLE olv_app WITH LOGIN BYPASSRLS",
		},
		{
			name:  "createrole drift",
			class: attrsUnprivileged, have: roleAttrs{Login: true, CreateRole: true},
			want: "ALTER ROLE olv_app WITH LOGIN NOCREATEROLE",
		},
		{
			name:  "createdb drift",
			class: attrsUnprivileged, have: roleAttrs{Login: true, CreateDB: true},
			want: "ALTER ROLE olv_app WITH LOGIN NOCREATEDB",
		},
		{
			name:  "replication drift",
			class: attrsUnprivileged, have: roleAttrs{Login: true, Replication: true},
			want: "ALTER ROLE olv_app WITH LOGIN NOREPLICATION",
		},
		{
			name:  "two drifted attributes keep the statement order",
			class: attrsUnprivileged, have: roleAttrs{Login: true, CreateDB: true, BypassRLS: true},
			want: "ALTER ROLE olv_app WITH LOGIN NOBYPASSRLS NOCREATEDB",
		},
		{
			name:  "createrole and replication",
			class: attrsUnprivileged, have: roleAttrs{Login: true, CreateRole: true, Replication: true},
			want: "ALTER ROLE olv_app WITH LOGIN NOCREATEROLE NOREPLICATION",
		},
		{
			name:  "every attribute wrong is the full published list, LOGIN first",
			class: attrsUnprivileged,
			have:  roleAttrs{Superuser: true, BypassRLS: true, CreateRole: true, CreateDB: true, Replication: true},
			want:  "ALTER ROLE olv_app WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION",
		},
		{
			name:  "every attribute wrong for a runtime reader names BYPASSRLS positively",
			class: attrsAdmin,
			have:  roleAttrs{Superuser: true, CreateRole: true, CreateDB: true, Replication: true},
			want:  "ALTER ROLE olv_app WITH LOGIN NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			desired, err := desiredRoleAttrs(tc.class)
			if err != nil {
				t.Fatalf("desiredRoleAttrs(%q): %v", tc.class, err)
			}
			if got := alterRoleAttributesSQL("olv_app", desired, tc.have); got != tc.want {
				t.Errorf("\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestEveryConvergenceStatementRetainsLogin is the mutation control for the one option
// that is NOT conditional. Dropping LOGIN from a zero-drift rerun would leave a
// statement with no options at all — or no statement — and a rerun that issues nothing
// silently passes a role the executor has no authority to administer.
func TestEveryConvergenceStatementRetainsLogin(t *testing.T) {
	t.Parallel()

	desired, _ := desiredRoleAttrs(attrsUnprivileged)
	for _, have := range []roleAttrs{
		{Login: true},
		{},
		{Login: true, Superuser: true},
		{Login: false, BypassRLS: true, CreateDB: true},
		{Login: true, Superuser: true, BypassRLS: true, CreateRole: true, CreateDB: true, Replication: true},
	} {
		got := alterRoleAttributesSQL("olv_app", desired, have)
		if !strings.HasPrefix(got, "ALTER ROLE olv_app WITH LOGIN") {
			t.Errorf("observed %+v produced %q, which does not administer the role with LOGIN", have, got)
		}
	}
}

// TestFirstRoleAttrMismatchNamesAFieldNotAPosture keeps the postcondition diagnostic
// printable: a field name, in statement order, never a rendering of either posture.
func TestFirstRoleAttrMismatchNamesAFieldNotAPosture(t *testing.T) {
	t.Parallel()

	want := roleAttrs{Login: true}
	cases := []struct {
		got   roleAttrs
		field string
	}{
		{roleAttrs{Login: true}, ""},
		{roleAttrs{}, "login"},
		{roleAttrs{Login: true, Superuser: true}, "superuser"},
		{roleAttrs{Login: true, BypassRLS: true}, "bypassrls"},
		{roleAttrs{Login: true, CreateRole: true}, "createrole"},
		{roleAttrs{Login: true, CreateDB: true}, "createdb"},
		{roleAttrs{Login: true, Replication: true}, "replication"},
		{roleAttrs{Superuser: true, Replication: true}, "login"},
	}
	for _, tc := range cases {
		if got := firstRoleAttrMismatch(want, tc.got); got != tc.field {
			t.Errorf("firstRoleAttrMismatch(want, %+v) = %q, want %q", tc.got, got, tc.field)
		}
	}
}

// --- sequence and refusal ---------------------------------------------------

// TestUpsertRoleRefusesBadInputBeforeTouchingTheServer covers both pre-flight refusals.
// The point of the assertion is the empty call log: an unsafe identifier and an unknown
// attribute set must not become a catalog read, let alone a statement.
func TestUpsertRoleRefusesBadInputBeforeTouchingTheServer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		role  string
		attrs string
	}{
		{"identifier that provisioning will not quote", "Robert'); DROP ROLE --", attrsUnprivileged},
		{"identifier that is empty", "", attrsUnprivileged},
		{"attribute set nobody published", "olv_app", "SUPERUSER"},
		{"attribute set that grew an option", "olv_app", attrsUnprivileged + " CREATEDB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, db := newScriptedRoleServer(t)
			if err := upsertRole(context.Background(), db, tc.role, tc.attrs, "pw"); err == nil {
				t.Fatal("accepted an input the role stage must refuse")
			}
			if calls := srv.statements(); len(calls) != 0 {
				t.Errorf("refusal reached the server: %q", calls)
			}
		})
	}
}

// TestUpsertRoleCreatesNologinThenSetsTheCredential pins the CREATE sequence exactly:
// look, identify, create without a credential, format the credential, apply it, verify.
// The order is the property — attributes before credential — not merely the set.
func TestUpsertRoleCreatesNologinThenSetsTheCredential(t *testing.T) {
	t.Parallel()

	srv, db := newScriptedRoleServer(t)
	srv.lookups = []lookupAnswer{
		{found: false},
		{found: true, row: observedRole{OID: 40001, Name: "olv_app", Attrs: roleAttrs{Login: true}}},
	}
	if err := upsertRole(context.Background(), db, "olv_app", attrsUnprivileged, syntheticSecret); err != nil {
		t.Fatalf("create path: %v", err)
	}
	srv.wantStages(t,
		stageLookup, stageIdentity, stageCreate, stageCredentialFormat, stageCredentialExec, stageLookup)

	created := srv.statementFor(t, stageCreate)
	if created != "CREATE ROLE olv_app WITH NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION" {
		t.Errorf("creation statement was %q", created)
	}
	if strings.Contains(created, syntheticSecret) {
		t.Error("the creation statement carried the credential")
	}
}

// TestUpsertRoleRerunsWithLoginAndPreservesAnAbsentPassword is the rerun `db init` is
// built for: converge the attributes, keep the credential the operator did not supply.
func TestUpsertRoleRerunsWithLoginAndPreservesAnAbsentPassword(t *testing.T) {
	t.Parallel()

	existing := observedRole{OID: 40002, Name: "olv_app", Attrs: roleAttrs{Login: true, CreateDB: true}}
	converged := existing
	converged.Attrs.CreateDB = false

	srv, db := newScriptedRoleServer(t)
	srv.lookups = []lookupAnswer{{found: true, row: existing}, {found: true, row: converged}}
	if err := upsertRole(context.Background(), db, "olv_app", attrsUnprivileged, ""); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	srv.wantStages(t, stageLookup, stageIdentity, stageAlterAttrs, stageLookup)
	if got, want := srv.statementFor(t, stageAlterAttrs), "ALTER ROLE olv_app WITH LOGIN NOCREATEDB"; got != want {
		t.Errorf("convergence statement %q, want %q", got, want)
	}
}

// TestUpsertRoleWillNotCreateWithoutACredential keeps the rule that predates this
// change: a login role with no password is unusable, so it is not created at all. The
// call log proves the refusal happened BEFORE the write, not after it.
func TestUpsertRoleWillNotCreateWithoutACredential(t *testing.T) {
	t.Parallel()

	srv, db := newScriptedRoleServer(t)
	srv.lookups = []lookupAnswer{{found: false}}
	err := upsertRole(context.Background(), db, "olv_app", attrsUnprivileged, "")
	if err == nil {
		t.Fatal("created a role with no password")
	}
	if !strings.Contains(err.Error(), "without a password") {
		t.Errorf("diagnostic does not name the missing credential: %v", err)
	}
	srv.wantStages(t, stageLookup, stageIdentity)
}

// TestUpsertRoleRefusesItsOwnIdentity is F3 as identity equality: the executor's login
// and its effective role after SET ROLE are both refused, and neither becomes a write.
// The guard is OID equality, so it holds for a target whose NAME resembles nothing in
// particular — provisioning acquires no heuristic about privileged names.
func TestUpsertRoleRefusesItsOwnIdentity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		targetOID  int64
		sessionOID int64
		currentOID int64
	}{
		{"target is the login this transaction authenticated as", 700, 700, 900},
		{"target is the effective role after SET ROLE", 800, 700, 800},
		{"target is both", 700, 700, 700},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, db := newScriptedRoleServer(t)
			srv.sessionOID, srv.currentOID = tc.sessionOID, tc.currentOID
			srv.lookups = []lookupAnswer{{found: true, row: observedRole{
				OID: tc.targetOID, Name: "olv_app", Attrs: roleAttrs{Login: true, CreateDB: true},
			}}}
			err := upsertRole(context.Background(), db, "olv_app", attrsUnprivileged, syntheticSecret)
			if err == nil {
				t.Fatal("provisioning converged the identity it is running as")
			}
			if !strings.Contains(err.Error(), "running as") {
				t.Errorf("diagnostic does not say why: %v", err)
			}
			srv.wantStages(t, stageLookup, stageIdentity)
		})
	}
}

// TestUpsertRoleRefusesAnExistingSuperuserTarget is the other half of F3: an observed
// rolsuper is an administrative identity, not application-role drift, and it is refused
// even when the executor is a superuser and the demotion would succeed.
func TestUpsertRoleRefusesAnExistingSuperuserTarget(t *testing.T) {
	t.Parallel()

	srv, db := newScriptedRoleServer(t)
	srv.sessionOID, srv.currentOID = 10, 10 // a superuser executor, and NOT the target
	srv.lookups = []lookupAnswer{{found: true, row: observedRole{
		OID: 4242, Name: "olv_app", Attrs: roleAttrs{Login: true, Superuser: true},
	}}}
	err := upsertRole(context.Background(), db, "olv_app", attrsUnprivileged, syntheticSecret)
	if err == nil {
		t.Fatal("provisioning demoted a superuser as ordinary drift")
	}
	if !strings.Contains(err.Error(), "SUPERUSER") {
		t.Errorf("diagnostic does not name the refused posture: %v", err)
	}
	srv.wantStages(t, stageLookup, stageIdentity)
}

// TestUpsertRoleRefusesWhenItCannotNameItself covers the identity query returning no
// row at all. Provisioning that cannot resolve its own session or effective role cannot
// tell whether the next statement administers itself, so it stops.
func TestUpsertRoleRefusesWhenItCannotNameItself(t *testing.T) {
	t.Parallel()

	srv, db := newScriptedRoleServer(t)
	srv.identityMissing = true
	srv.lookups = []lookupAnswer{{found: true, row: observedRole{OID: 5, Name: "olv_app", Attrs: roleAttrs{Login: true}}}}
	err := upsertRole(context.Background(), db, "olv_app", attrsUnprivileged, "")
	if err == nil {
		t.Fatal("provisioning administered a role from an identity it could not resolve")
	}
	if !errors.Is(err, errExecutorIdentityUnresolved) {
		t.Errorf("diagnostic is not the unresolved-identity refusal: %v", err)
	}
	srv.wantStages(t, stageLookup, stageIdentity)
}

// TestARefusedAttributeStepNeverFormatsOrSendsTheCredential is the F4 property stated
// as a sequence: when the privileged attribute is refused, the credential is not
// formatted, not sent, and not in the error.
func TestARefusedAttributeStepNeverFormatsOrSendsTheCredential(t *testing.T) {
	t.Parallel()

	srv, db := newScriptedRoleServer(t)
	srv.lookups = []lookupAnswer{{found: true, row: observedRole{
		OID: 4243, Name: "olv_reader", Attrs: roleAttrs{Login: true},
	}}}
	srv.failures[stageAlterAttrs] = &pgconn.PgError{
		Severity: "ERROR", Code: "42501",
		Message: "permission denied to alter role",
		Detail:  "Only roles with the BYPASSRLS attribute may change the BYPASSRLS attribute.",
	}
	err := upsertRole(context.Background(), db, "olv_reader", attrsAdmin, syntheticSecret)
	if err == nil {
		t.Fatal("a refused attribute administration was reported as success")
	}
	srv.wantStages(t, stageLookup, stageIdentity, stageAlterAttrs)
	for _, s := range srv.statements() {
		if strings.Contains(s, syntheticSecret) || strings.Contains(s, "PASSWORD") {
			t.Errorf("a credential reached the server after the attribute step was refused: %q", s)
		}
	}
	if !strings.Contains(err.Error(), "SQLSTATE 42501") {
		t.Errorf("diagnostic drops the server's SQLSTATE: %v", err)
	}
}

// TestRoleStageDiagnosticsCarryNoServerText injects a failure at EVERY stage, each one
// carrying the synthetic secret and a full server message, and requires the returned
// error to name the stage and the SQLSTATE and nothing else. A helper that wrapped the
// driver error with %w would fail every row here.
func TestRoleStageDiagnosticsCarryNoServerText(t *testing.T) {
	t.Parallel()

	leaky := func(code string) error {
		return &pgconn.PgError{
			Severity: "ERROR", Code: code,
			Message: fmt.Sprintf("permission denied; statement was ALTER ROLE olv_app WITH LOGIN PASSWORD '%s'", syntheticSecret),
			Detail:  "Only roles with the SUPERUSER attribute may change the SUPERUSER attribute.",
			Hint:    "try again as " + syntheticSecret,
			Where:   "PL/pgSQL function inline_code_block line 1",
			Routine: "AlterRole",
		}
	}

	cases := []struct {
		name    string
		stage   roleStage
		exists  bool
		wantMsg string
	}{
		{"catalog lookup", stageLookup, true, roleStageLookup},
		{"executor identity", stageIdentity, true, roleStageIdentity},
		{"create", stageCreate, false, roleStageCreate},
		{"attribute administration", stageAlterAttrs, true, roleStageAttributes},
		{"credential format", stageCredentialFormat, true, roleStageCredential},
		{"credential exec", stageCredentialExec, true, roleStageCredential},
		{"postcondition", stagePostcondition, true, roleStagePostcondition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, db := newScriptedRoleServer(t)
			row := observedRole{OID: 4244, Name: "olv_app", Attrs: roleAttrs{Login: true}}
			if tc.exists {
				srv.lookups = []lookupAnswer{{found: true, row: row}, {found: true, row: row}}
			} else {
				srv.lookups = []lookupAnswer{{found: false}, {found: true, row: row}}
			}
			srv.failures[tc.stage] = leaky("42501")

			err := upsertRole(context.Background(), db, "olv_app", attrsUnprivileged, syntheticSecret)
			if err == nil {
				t.Fatalf("stage %s failed on the server and the helper reported success", tc.stage)
			}
			got := err.Error()
			if strings.Contains(got, syntheticSecret) {
				t.Errorf("diagnostic carries the credential: %s", got)
			}
			for _, leak := range []string{"permission denied", "Only roles with", "AlterRole", "PL/pgSQL", "try again as"} {
				if strings.Contains(got, leak) {
					t.Errorf("diagnostic carries server text %q: %s", leak, got)
				}
			}
			if !strings.Contains(got, tc.wantMsg) {
				t.Errorf("diagnostic does not name the stage %q: %s", tc.wantMsg, got)
			}
			if !strings.Contains(got, "SQLSTATE 42501") {
				t.Errorf("diagnostic does not carry the SQLSTATE: %s", got)
			}
		})
	}
}

// TestRoleStageErrorOnlyRepeatsAWellFormedSQLSTATE keeps the one thing the diagnostic
// IS allowed to repeat inside its defined shape. A driver that put a message, an empty
// string or a lower-case token in the Code field must not have it printed as a SQLSTATE.
func TestRoleStageErrorOnlyRepeatsAWellFormedSQLSTATE(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"", "4250", "425011", "4250x", "permission denied " + syntheticSecret, "42-01"} {
		err := roleStageError(roleStageAttributes, "olv_app", &pgconn.PgError{Code: code, Message: syntheticSecret})
		if strings.Contains(err.Error(), "SQLSTATE") {
			t.Errorf("code %q was printed as a SQLSTATE: %v", code, err)
		}
		if strings.Contains(err.Error(), syntheticSecret) {
			t.Errorf("code %q leaked the message: %v", code, err)
		}
	}
	for _, code := range []string{"42501", "25P02", "XX000", "00000"} {
		err := roleStageError(roleStageAttributes, "olv_app", &pgconn.PgError{Code: code, Message: syntheticSecret})
		if !strings.Contains(err.Error(), "SQLSTATE "+code) {
			t.Errorf("well-formed code %q was dropped: %v", code, err)
		}
	}
	if err := roleStageError(roleStageLookup, "olv_app", errors.New("dial tcp: "+syntheticSecret)); strings.Contains(err.Error(), syntheticSecret) {
		t.Errorf("a driver error without a SQLSTATE leaked its text: %v", err)
	}
}

// TestUpsertRolePreservesCancellationIdentity keeps the one distinction a caller can
// still act on: a canceled or timed-out role stage stays recognizable as such, and never
// becomes a created role.
//
// It covers FOUR points, and they are not interchangeable. Cancellation before the
// lookup; cancellation while the catalog read is in flight; cancellation at the
// executor-identity boundary, which is the first point at which the stage is known to
// hold a COMPLETED lookup result; and a deadline, which must keep its own identity.
//
// The third is the one the construction's evidence paragraph asks for by name, and the
// second does not stand in for it. An in-flight cancellation can interrupt the read
// itself, so it proves the outcome — canceled, nothing created — without establishing
// that the stage ever reached the state where a helper could have mistaken "canceled"
// for "absent". upsertRole issues the identity query only after readRoleFromCatalog
// returned a completed row-or-absent result with a nil error, so cancelling exactly
// there is that state, deterministically.
func TestUpsertRolePreservesCancellationIdentity(t *testing.T) {
	t.Parallel()

	t.Run("canceled before the lookup", func(t *testing.T) {
		t.Parallel()
		srv, db := newScriptedRoleServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := upsertRole(ctx, db, "olv_app", attrsUnprivileged, syntheticSecret)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		for _, s := range srv.statements() {
			if strings.HasPrefix(s, "CREATE ROLE") {
				t.Errorf("a canceled context selected creation: %q", s)
			}
		}
	})

	// Cancellation WHILE THE CATALOG READ IS IN FLIGHT. The hook fires as the row is
	// being served, before the caller's Scan completes, so which side of that race the
	// execution takes is not asserted and must not be read into this control: what it
	// proves is that an interrupted read is reported as a cancellation and never as the
	// absence that would select creation.
	t.Run("canceled while the catalog read is in flight", func(t *testing.T) {
		t.Parallel()
		srv, db := newScriptedRoleServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		srv.lookups = []lookupAnswer{{found: false}}
		srv.afterLookup = cancel
		err := upsertRole(ctx, db, "olv_app", attrsUnprivileged, syntheticSecret)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		for _, s := range srv.statements() {
			if strings.HasPrefix(s, "CREATE ROLE") {
				t.Errorf("a cancellation around the catalog read still created the role: %q", s)
			}
		}
	})

	// Cancellation AT THE EXECUTOR-IDENTITY BOUNDARY, after one completed lookup that
	// answered "absent". This is the interleaving that matters: the stage is holding the
	// result a careless helper would have treated as permission to create, and the
	// context is gone. The recorded sequence is the proof of position — exactly one
	// target lookup, then the identity query — and it is deterministic because the
	// scripted server records what it RECEIVES and the hook cancels there.
	t.Run("canceled at the executor-identity boundary after a completed absent lookup", func(t *testing.T) {
		t.Parallel()
		srv, db := newScriptedRoleServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		srv.lookups = []lookupAnswer{{found: false}}
		srv.beforeIdentity = cancel

		err := upsertRole(ctx, db, "olv_app", attrsUnprivileged, syntheticSecret)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if !strings.Contains(err.Error(), roleStageIdentity) {
			t.Errorf("the diagnostic does not name the stage the cancellation was observed at: %v", err)
		}
		srv.wantStages(t, stageLookup, stageIdentity)
		for _, s := range srv.statements() {
			if strings.HasPrefix(s, "CREATE ROLE") || strings.HasPrefix(s, "ALTER ROLE") || strings.Contains(s, "pg_catalog.format(") {
				t.Errorf("a write or a credential formatting followed the cancellation: %q", s)
			}
		}
		// Recorded so the run's own stream carries the measurement rather than only the
		// verdict of an assertion. Neither value can contain a credential: the statements
		// are the two catalog reads, and the diagnostic is stage plus role name.
		t.Logf("observed boundary: stages=%v diagnostic=%v", srv.stages(), err)
	})

	t.Run("deadline exceeded keeps its own identity", func(t *testing.T) {
		t.Parallel()
		_, db := newScriptedRoleServer(t)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		err := upsertRole(ctx, db, "olv_app", attrsUnprivileged, syntheticSecret)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	})
}

// TestAnUnreadableCatalogIsNeverACreate is the fail-closed rule spelled out: a refused
// or unavailable catalog read is an error, never the absence that selects creation.
func TestAnUnreadableCatalogIsNeverACreate(t *testing.T) {
	t.Parallel()

	for _, injected := range []error{
		&pgconn.PgError{Code: "42501", Message: "permission denied for view pg_roles"},
		&pgconn.PgError{Code: "25P02", Message: "current transaction is aborted"},
		errors.New("connection reset by peer"),
	} {
		srv, db := newScriptedRoleServer(t)
		srv.lookups = []lookupAnswer{{err: injected}}
		if err := upsertRole(context.Background(), db, "olv_app", attrsUnprivileged, syntheticSecret); err == nil {
			t.Fatal("an unreadable catalog was reported as a provisioned role")
		}
		srv.wantStages(t, stageLookup)
	}
}

// TestUpsertRolePostconditionRefusesEveryDivergence exercises the reread through the
// seam: a role that vanished, a role the catalog resolves to a different name, a role
// whose OID moved under the transaction, and every flag that did not stick. Each is a
// failure of this role transaction, and each names its field or its stage.
func TestUpsertRolePostconditionRefusesEveryDivergence(t *testing.T) {
	t.Parallel()

	before := observedRole{OID: 40010, Name: "olv_app", Attrs: roleAttrs{Login: true, CreateDB: true}}
	converged := roleAttrs{Login: true}

	cases := []struct {
		name  string
		after lookupAnswer
		want  string
	}{
		{"the role is gone", lookupAnswer{found: false}, "absent from the catalog"},
		{
			"the catalog resolved another name",
			lookupAnswer{found: true, row: observedRole{OID: 40010, Name: "olv_app_other", Attrs: converged}},
			"different role name",
		},
		{
			"the role was dropped and recreated under the transaction",
			lookupAnswer{found: true, row: observedRole{OID: 40011, Name: "olv_app", Attrs: converged}},
			"catalog identity changed",
		},
		{
			"login did not stick",
			lookupAnswer{found: true, row: observedRole{OID: 40010, Name: "olv_app", Attrs: roleAttrs{}}},
			"login did not converge",
		},
		{
			"the drifted attribute did not stick",
			lookupAnswer{found: true, row: observedRole{OID: 40010, Name: "olv_app", Attrs: roleAttrs{Login: true, CreateDB: true}}},
			"createdb did not converge",
		},
		{
			"a privileged attribute appeared instead",
			lookupAnswer{found: true, row: observedRole{OID: 40010, Name: "olv_app", Attrs: roleAttrs{Login: true, Superuser: true}}},
			"superuser did not converge",
		},
		{
			"bypassrls appeared on an application role",
			lookupAnswer{found: true, row: observedRole{OID: 40010, Name: "olv_app", Attrs: roleAttrs{Login: true, BypassRLS: true}}},
			"bypassrls did not converge",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, db := newScriptedRoleServer(t)
			srv.lookups = []lookupAnswer{{found: true, row: before}, tc.after}
			err := upsertRole(context.Background(), db, "olv_app", attrsUnprivileged, "")
			if err == nil {
				t.Fatal("the postcondition accepted a posture that did not converge")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("diagnostic %q does not name %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), roleStagePostcondition) {
				t.Errorf("diagnostic %q does not name the postcondition stage", err)
			}
		})
	}
}

// TestUpsertRoleAcceptsAConvergedPostcondition is the positive control for the block
// above: with the reread reporting the desired posture, the stage returns nil. Without
// it, a postcondition that refused everything would pass every negative case.
func TestUpsertRoleAcceptsAConvergedPostcondition(t *testing.T) {
	t.Parallel()

	before := observedRole{OID: 40020, Name: "olv_reader", Attrs: roleAttrs{Login: true}}
	after := observedRole{OID: 40020, Name: "olv_reader", Attrs: roleAttrs{Login: true, BypassRLS: true}}

	srv, db := newScriptedRoleServer(t)
	srv.lookups = []lookupAnswer{{found: true, row: before}, {found: true, row: after}}
	if err := upsertRole(context.Background(), db, "olv_reader", attrsAdmin, syntheticSecret); err != nil {
		t.Fatalf("a converged runtime reader was refused: %v", err)
	}
	srv.wantStages(t, stageLookup, stageIdentity, stageAlterAttrs, stageCredentialFormat, stageCredentialExec, stageLookup)
	if got, want := srv.statementFor(t, stageAlterAttrs), "ALTER ROLE olv_reader WITH LOGIN BYPASSRLS"; got != want {
		t.Errorf("convergence statement %q, want %q", got, want)
	}
}

// --- the scripted server ----------------------------------------------------

// roleStage classifies a statement the role stage issues, so a control can script an
// answer for it without depending on the exact SQL text.
type roleStage string

const (
	stageLookup           roleStage = "lookup"
	stageIdentity         roleStage = "identity"
	stageCreate           roleStage = "create"
	stageAlterAttrs       roleStage = "alter-attributes"
	stageCredentialFormat roleStage = "credential-format"
	stageCredentialExec   roleStage = "credential-exec"
	// stagePostcondition is not a distinct statement: it is the SECOND lookup. It
	// exists so a control can inject a failure there and not at the first read.
	stagePostcondition roleStage = "postcondition"
)

func classifyRoleStatement(q string) roleStage {
	switch {
	case strings.Contains(q, "SESSION_USER"):
		return stageIdentity
	case strings.Contains(q, "pg_catalog.pg_roles"):
		return stageLookup
	case strings.Contains(q, "pg_catalog.format("):
		return stageCredentialFormat
	case strings.HasPrefix(q, "CREATE ROLE"):
		return stageCreate
	case strings.HasPrefix(q, "ALTER ROLE") && strings.Contains(q, "PASSWORD"):
		return stageCredentialExec
	case strings.HasPrefix(q, "ALTER ROLE"):
		return stageAlterAttrs
	}
	return roleStage("unclassified: " + q)
}

// lookupAnswer is one scripted catalog read. The queue is consumed in order and the
// last entry repeats, so a control that cares about only the first read writes one.
type lookupAnswer struct {
	row   observedRole
	found bool
	err   error
}

// roleServer is a scripted PostgreSQL stand-in behind a real database/sql driver, so
// the code under test runs against the *sql.DB it expects — the execQuerier seam is
// production's, not a test double slipped into the product.
type roleServer struct {
	mu       sync.Mutex
	calls    []roleCall
	lookups  []lookupAnswer
	failures map[roleStage]error

	sessionOID, currentOID int64
	identityMissing        bool

	lookupsServed int
	// afterLookup fires while a catalog read is being served, BEFORE the caller's Scan
	// has completed. beforeIdentity fires when the executor-identity query arrives,
	// which the role stage only issues after a target lookup returned a completed
	// result. They are two different boundaries and the controls name them as such.
	afterLookup    func()
	beforeIdentity func()
}

type roleCall struct {
	stage roleStage
	sql   string
}

func newScriptedRoleServer(t *testing.T) (*roleServer, *sql.DB) {
	t.Helper()
	srv := &roleServer{
		failures:   map[roleStage]error{},
		sessionOID: 111,
		currentOID: 111,
	}
	db := sql.OpenDB(roleConnector{srv: srv})
	t.Cleanup(func() { _ = db.Close() })
	return srv, db
}

func (s *roleServer) record(q string) roleStage {
	stage := classifyRoleStatement(q)
	s.mu.Lock()
	defer s.mu.Unlock()
	if stage == stageLookup {
		s.lookupsServed++
		if s.lookupsServed > 1 {
			stage = stagePostcondition
		}
	}
	s.calls = append(s.calls, roleCall{stage: stage, sql: q})
	return stage
}

// fault returns the scripted failure for a stage. The two lookups share one statement
// shape, so a failure scripted for stageLookup applies to the first read and one
// scripted for stagePostcondition applies to the reread.
func (s *roleServer) fault(stage roleStage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures[stage]
}

func (s *roleServer) nextLookup() lookupAnswer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lookups) == 0 {
		return lookupAnswer{found: false}
	}
	i := s.lookupsServed - 1
	if i >= len(s.lookups) {
		i = len(s.lookups) - 1
	}
	return s.lookups[i]
}

func (s *roleServer) statements() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.calls))
	for _, c := range s.calls {
		out = append(out, c.sql)
	}
	return out
}

func (s *roleServer) stages() []roleStage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]roleStage, 0, len(s.calls))
	for _, c := range s.calls {
		stage := c.stage
		if stage == stagePostcondition {
			stage = stageLookup // the caller reasons about reads, not about which one
		}
		out = append(out, stage)
	}
	return out
}

func (s *roleServer) wantStages(t *testing.T, want ...roleStage) {
	t.Helper()
	got := s.stages()
	if len(got) != len(want) {
		t.Fatalf("statement sequence\n got %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("statement sequence\n got %v\nwant %v", got, want)
		}
	}
}

func (s *roleServer) statementFor(t *testing.T, stage roleStage) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c.stage == stage {
			return c.sql
		}
	}
	t.Fatalf("no statement was issued for stage %s", stage)
	return ""
}

// --- database/sql plumbing for the scripted server --------------------------

type roleConnector struct{ srv *roleServer }

// roleConn carries the same single field, so handing the server over is a conversion.
func (c roleConnector) Connect(context.Context) (driver.Conn, error) { return roleConn(c), nil }
func (c roleConnector) Driver() driver.Driver                        { return roleDriver{} }

type roleDriver struct{}

func (roleDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("the scripted role server is only reachable through sql.OpenDB")
}

type roleConn struct{ srv *roleServer }

func (roleConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("the scripted role server answers ExecContext/QueryContext only")
}
func (roleConn) Close() error              { return nil }
func (roleConn) Begin() (driver.Tx, error) { return nil, errors.New("no transactions here") }

func (c roleConn) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stage := c.srv.record(q)
	if err := c.srv.fault(stage); err != nil {
		return nil, err
	}
	return roleResult{}, nil
}

func (c roleConn) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	// record() means "the driver RECEIVED this statement", and it runs before the
	// context check on purpose: the post-lookup control cancels exactly at the identity
	// boundary, and the recorded statement is the evidence that the boundary was
	// reached. database/sql refuses to hand out a connection once a context is done, so
	// a statement the role stage issues AFTER that point never arrives here at all.
	stage := c.srv.record(q)
	if stage == stageIdentity && c.srv.beforeIdentity != nil {
		c.srv.beforeIdentity()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.srv.fault(stage); err != nil {
		return nil, err
	}
	switch stage {
	case stageIdentity:
		if c.srv.identityMissing {
			return &roleRows{cols: []string{"session", "current"}}, nil
		}
		return &roleRows{
			cols: []string{"session", "current"},
			rows: [][]driver.Value{{c.srv.sessionOID, c.srv.currentOID}},
		}, nil
	case stageLookup, stagePostcondition:
		answer := c.srv.nextLookup()
		if hook := c.srv.afterLookup; hook != nil {
			hook()
		}
		if answer.err != nil {
			return nil, answer.err
		}
		rows := &roleRows{cols: []string{"oid", "rolname", "login", "super", "bypassrls", "createrole", "createdb", "replication"}}
		if answer.found {
			a := answer.row.Attrs
			rows.rows = [][]driver.Value{{
				answer.row.OID, answer.row.Name,
				a.Login, a.Superuser, a.BypassRLS, a.CreateRole, a.CreateDB, a.Replication,
			}}
		}
		return rows, nil
	case stageCredentialFormat:
		// What a real server returns: the identifier, LOGIN, and the password as a
		// quoted literal. The controls assert this text never reaches a diagnostic.
		return &roleRows{
			cols: []string{"format"},
			rows: [][]driver.Value{{"ALTER ROLE olv_app WITH LOGIN PASSWORD '" + syntheticSecret + "'"}},
		}, nil
	}
	return nil, fmt.Errorf("the scripted role server was asked something it does not know: %q", q)
}

type roleResult struct{}

func (roleResult) LastInsertId() (int64, error) {
	return 0, errors.New("role administration returns no identifier")
}
func (roleResult) RowsAffected() (int64, error) { return 0, nil }

type roleRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *roleRows) Columns() []string { return r.cols }
func (r *roleRows) Close() error      { return nil }
func (r *roleRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

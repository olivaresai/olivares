// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgtest

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// The provisioning and teardown themselves need a real Postgres and are exercised
// wherever the DSNs are configured — locally against the dev container's live
// server and in CI. Everything that decides WHAT gets provisioned — the gating,
// the generated identifiers, the DSN rewriting and the identifier guard — is pure,
// and is covered here so a regression cannot wait for a server-gated leg to catch it.

// TestClassify pins the gating table, including the case the whole guard exists
// for: the legacy shared DSN set WITHOUT the superuser DSN must be a loud
// misconfiguration, never a silent skip that deletes the Postgres leg.
func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		name              string
		super, app, admin string
		want              gate
	}{
		{"neither set", "", "", "", gateSkip},
		{"superuser only", "postgres://s@h/db", "", "", gateRun},
		{"all set", "postgres://s@h/db", "postgres://a@h/db", "postgres://x@h/db", gateRun},
		{"legacy app DSN only", "", "postgres://a@h/db", "", gateMisconfigured},
		{"legacy admin DSN only", "", "", "postgres://x@h/db", gateMisconfigured},
	} {
		if got := classify(tc.super, tc.app, tc.admin); got != tc.want {
			t.Errorf("%s: classify(%q, %q, %q) = %v, want %v", tc.name, tc.super, tc.app, tc.admin, got, tc.want)
		}
	}
	// The remediation must name the variable that ACTUALLY tripped the gate. Naming
	// only EnvAppDSN sent an operator whose ADMIN DSN tripped it down a dead end:
	// they would unset a variable that was never set and still hard-fail.
	appOnly := misconfiguredMsg("postgres://a@h/db", "")
	if !strings.Contains(appOnly, EnvAppDSN) || strings.Contains(appOnly, EnvAdminDSN) {
		t.Errorf("app-only message must name %s and not %s, got %q", EnvAppDSN, EnvAdminDSN, appOnly)
	}
	adminOnly := misconfiguredMsg("", "postgres://x@h/db")
	if !strings.Contains(adminOnly, EnvAdminDSN) || strings.Contains(adminOnly, EnvAppDSN) {
		t.Errorf("admin-only message must name %s and not %s, got %q", EnvAdminDSN, EnvAppDSN, adminOnly)
	}
	for _, msg := range []string{appOnly, adminOnly} {
		if !strings.Contains(msg, EnvSuperuserDSN) {
			t.Errorf("every message must name the variable to SET, got %q", msg)
		}
	}
}

// TestSuffixIsActuallyUnique is the regression for the defect this package was
// written around: the suites' previous uniqueSuffix() was
// string(model.NewID())[:8] over a UUIDv7, i.e. the top 32 bits of a millisecond
// timestamp, so 1000 consecutive calls returned ONE value and "isolated"
// databases silently shared a name.
func TestSuffixIsActuallyUnique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		seen[Suffix(t)] = struct{}{}
	}
	if len(seen) != n {
		t.Fatalf("%d calls produced %d distinct suffixes, want %d (a colliding suffix rejoins two tests on one database)", n, len(seen), n)
	}
	// Shape, not just distinctness: these names authorize a DROP, so pin the width
	// (128 bits = 32 hex chars) and the alphabet. Distinctness alone would still pass
	// with 16 bits, where the birthday bound bites well before 1000 draws.
	//
	// Split the suffix into a process tag plus the random half. The entropy
	// requirement is unchanged and is asserted on the RANDOM half alone: the tag is
	// provenance, not uniqueness, and counting its digits toward the width would let
	// the random half shrink while the total length still looked right.
	tag := procTag()
	for suffix := range seen {
		if !strings.HasPrefix(suffix, tag) {
			t.Fatalf("suffix %q does not carry this process's tag %q; cluster-state audits attribute objects by it", suffix, tag)
		}
		random := strings.TrimPrefix(suffix, tag)
		if len(random) != 32 {
			t.Fatalf("suffix %q has a %d-char random half, want 32 (128 bits)", suffix, len(random))
		}
		if strings.Trim(random, "0123456789abcdef") != "" {
			t.Fatalf("suffix %q is not lower-case hex after its tag; it must survive the SQL identifier guard", suffix)
		}
	}
}

// TestGeneratedIdentifiersAreProvisionable proves every name Isolate derives
// satisfies the provisioning guard (a plain lower-case SQL identifier, ≤63
// chars), so provisioning can never be refused for a name this package built.
func TestGeneratedIdentifiersAreProvisionable(t *testing.T) {
	ids := newIdentifiers(Suffix(t))
	for name, ident := range map[string]string{"database": ids.database, "tempOwner": ids.tempOwner, "tempAdmin": ids.tempAdmin} {
		if !safeIdent.MatchString(ident) {
			t.Errorf("%s identifier %q is not a plain lower-case SQL identifier", name, ident)
		}
		if len(ident) > 63 {
			t.Errorf("%s identifier %q is %d chars, over Postgres's 63-char limit", name, ident, len(ident))
		}
	}
	// The database and the throwaway owner role must not share a name, or teardown
	// would drop the wrong object.
	if ids.database == ids.tempOwner || ids.database == ids.tempAdmin || ids.tempOwner == ids.tempAdmin {
		t.Errorf("identifiers are not distinct: %+v", ids)
	}
}

// TestRoleDSNRewrite proves the DSN builder swaps credentials and database while
// preserving host, port and query (sslmode), and — the reason it parses instead of
// doing string surgery — that a password containing URL metacharacters survives a
// round trip. The helper it replaced concatenated strings and would have corrupted
// such a DSN.
func TestRoleDSNRewrite(t *testing.T) {
	super, err := url.Parse("postgres://postgres:postgres@localhost:5432/olivares?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	const pw = "p@ss:w/rd?x&y#z"
	got := roleDSN(super, "olv_ta_deadbeef", pw, "olv_t_deadbeef")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("rewritten DSN does not parse: %v (%q)", err, got)
	}
	if u.Host != "localhost:5432" {
		t.Errorf("host = %q, want localhost:5432", u.Host)
	}
	if u.Path != "/olv_t_deadbeef" {
		t.Errorf("database = %q, want /olv_t_deadbeef", u.Path)
	}
	if u.User.Username() != "olv_ta_deadbeef" {
		t.Errorf("user = %q, want olv_ta_deadbeef", u.User.Username())
	}
	if p, _ := u.User.Password(); p != pw {
		t.Errorf("password round-trip = %q, want %q (metacharacters must be escaped, not concatenated)", p, pw)
	}
	if u.Query().Get("sslmode") != "disable" {
		t.Errorf("sslmode = %q, want disable (the superuser DSN's query must survive)", u.Query().Get("sslmode"))
	}
	// The superuser URL must not have been mutated in place.
	if super.Path != "/olivares" || super.User.Username() != "postgres" {
		t.Errorf("roleDSN mutated its input: %v", super)
	}
}

// TestExecIdentRefusesUnsafeIdentifier proves the teardown statements — the only
// place this package interpolates an identifier, because Postgres cannot bind one
// — refuse anything that is not a plain identifier, and refuse it BEFORE touching
// the database (a nil *sql.DB would panic if the guard let it through).
func TestExecIdentRefusesUnsafeIdentifier(t *testing.T) {
	for _, bad := range []string{
		`olivares"; DROP DATABASE olivares; --`,
		"olivares olivares",
		"Olivares",   // upper case
		"9olivares",  // leading digit
		"",           // empty
		"olivares-1", // hyphen
		strings.Repeat("a", 64),
	} {
		var nilDB *sql.DB
		err := execIdent(context.Background(), nilDB, "DROP DATABASE IF EXISTS %s", bad)
		if err == nil {
			t.Errorf("execIdent accepted unsafe identifier %q", bad)
			continue
		}
		if !strings.Contains(err.Error(), "unsafe identifier") {
			t.Errorf("execIdent(%q) failed with %v, want the guard's refusal", bad, err)
		}
	}
	// A well-formed identifier must get PAST the guard. Proven by the fact that it
	// then reaches the nil *sql.DB and panics — and the panic is asserted to be the
	// nil-dereference, not something else, so this cannot pass for an unrelated
	// reason (or silently stop panicking if the guard were moved).
	var nilDB *sql.DB
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Error("a well-formed identifier did not reach the database call: the guard rejected it")
				return
			}
			if _, ok := r.(runtime.Error); !ok {
				t.Errorf("expected the nil *sql.DB runtime panic, got %[1]T: %[1]v", r)
			}
		}()
		_ = execIdent(context.Background(), nilDB, "DROP DATABASE IF EXISTS %s", "olv_t_deadbeef")
	}()
}

// TestAppRole pins the credential rule that turned on: the application
// role's password is taken from the configured DSN and is NEVER invented.
//
// Inventing one is destructive, not merely sloppy: ProvisionPostgres re-asserts a
// role's password on every call, so a guessed password would ROTATE the shared,
// production-named `olivares_app` on whatever server the suite was pointed at —
// with a value published in this repository.
func TestAppRole(t *testing.T) {
	t.Setenv(EnvAppDSN, "postgres://olivares_app:apppw@h:5432/olivares?sslmode=disable")
	got, err := appRole()
	if err != nil {
		t.Fatalf("a well-formed DSN must be accepted: %v", err)
	}
	if got.Name != dialect.DefaultAppRole || got.Password != "apppw" {
		t.Errorf("appRole() = %q/%q, want %q/apppw", got.Name, got.Password, dialect.DefaultAppRole)
	}

	for _, tc := range []struct{ name, dsn string }{
		{"unset — must not invent a password", ""},
		{"libpq keyword form", "host=localhost user=olivares_app password=apppw dbname=olivares"},
		{"no userinfo", "postgres://host:5432/db"},
		{"username but no password", "postgres://olivares_app@h:5432/db"},
		{"empty password", "postgres://olivares_app:@h:5432/db"},
		{"a different role name", "postgres://other_role:pw@h:5432/db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvAppDSN, tc.dsn)
			got, err := appRole()
			if err == nil {
				t.Fatalf("appRole() returned %q/%q instead of an error", got.Name, got.Password)
			}
		})
	}
}

// TestParseDSNNeverLeaksThePassword is the password-leak regression. url.Parse fails
// with a *url.Error whose Error() embeds the RAW connection string, password and
// all — and these errors are printed by tb.Fatalf, teed into a CI log and posted
// verbatim as a PR comment by .github/actions/pr-failure-report.
func TestParseDSNNeverLeaksThePassword(t *testing.T) {
	const secret = "sup3rs3cr3t"
	// A bare % is an invalid URL escape, which is what makes url.Parse fail.
	dsn := "postgres://postgres:" + secret + "%ZZ@localhost:5432/olivares?sslmode=disable"

	if _, err := url.Parse(dsn); err == nil || !strings.Contains(err.Error(), secret) {
		t.Fatalf("precondition: url.Parse must leak the password, else this test proves nothing (err=%v)", err)
	}
	_, err := parseDSN(dsn)
	if err == nil {
		t.Fatal("parseDSN must reject a malformed DSN")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("parseDSN leaked the password into its error: %q", err)
	}
	if strings.Contains(err.Error(), "localhost:5432") {
		t.Errorf("parseDSN leaked the raw DSN into its error: %q", err)
	}
}

// TestRefuseUnownedDrop is the unowned-drop regression: Drop is exported and takes
// free-form strings, so the "never pass a shared role" contract has to live in
// code, not in a doc comment.
func TestRefuseUnownedDrop(t *testing.T) {
	ids := newIdentifiers(Suffix(t))
	for _, ok := range []struct{ kind, name string }{
		{"database", ids.database},
		{"database", "olv_" + Suffix(t)}, // the dbsetup_test shape
		{"role", ids.tempOwner},
		{"role", ids.tempAdmin},
		{"role", "olv_app_" + Suffix(t)},
		{"role", "olv_own_" + Suffix(t)},
	} {
		if msg := refuseUnowned(ok.kind, ok.name); msg != "" {
			t.Errorf("%s %q is minted by this package but was refused: %s", ok.kind, ok.name, msg)
		}
	}
	for _, bad := range []struct{ kind, name string }{
		{"database", "olivares"},         // the real CI database
		{"database", "postgres"},         // the maintenance database
		{"role", dialect.DefaultAppRole}, // the shared app role
		{"role", defaultAdminRole},       // the shared BYPASSRLS role
		{"role", "postgres"},             // the superuser
		{"database", "olv_t_"},           // prefix with no entropy
		{"role", "olv_to_"},              // prefix with no entropy
	} {
		if refuseUnowned(bad.kind, bad.name) == "" {
			t.Errorf("%s %q was ACCEPTED for deletion; teardown must only remove what pgtest minted", bad.kind, bad.name)
		}
	}
}

// TestDSNsRedact is the DSN-redaction regression: every DSN field carries a password,
// so one stray %+v in any suite would put four of them in a CI log.
func TestDSNsRedact(t *testing.T) {
	d := DSNs{
		Database:  "olv_t_deadbeef",
		App:       "postgres://olivares_app:apppw@h/olv_t_deadbeef",
		Owner:     "postgres://olivares_app:apppw@h/olv_t_deadbeef",
		Admin:     "postgres://olv_tx_x:adminpw@h/olv_t_deadbeef",
		Superuser: "postgres://postgres:superpw@h/olv_t_deadbeef",
	}
	for _, rendered := range []string{fmt.Sprintf("%v", d), fmt.Sprintf("%+v", d), fmt.Sprintf("%#v", d), fmt.Sprintf("%s", d)} {
		for _, secret := range []string{"apppw", "adminpw", "superpw"} {
			if strings.Contains(rendered, secret) {
				t.Errorf("rendering leaked %q: %s", secret, rendered)
			}
		}
		if !strings.Contains(rendered, "olv_t_deadbeef") {
			t.Errorf("rendering must still identify the database, got %s", rendered)
		}
	}
}

// TestDefaultAppRoleMatchesDialect is the anti-drift guard for MUST-FIX 1: the
// role pgtest provisions for MUST be the one the schema revokes from.
func TestDefaultAppRoleMatchesDialect(t *testing.T) {
	if dialect.DefaultAppRole != "olivares_app" {
		t.Fatalf("dialect.DefaultAppRole = %q: deploy/postgres/01-app-role.sql and mainline-ci.yml provision olivares_app; if this changed on purpose, change them too", dialect.DefaultAppRole)
	}
	t.Setenv(EnvAppDSN, "postgres://"+dialect.DefaultAppRole+":pw@h:5432/olivares")
	got, err := appRole()
	if err != nil {
		t.Fatalf("the production role must be accepted: %v", err)
	}
	if got.Name != dialect.DefaultAppRole {
		t.Errorf("isolated databases would be owned by %q, not the role the append-only REVOKE targets (%q)", got.Name, dialect.DefaultAppRole)
	}
}

// TestAppRoleMismatchRefuses proves Isolate refuses to run as any role other than
// the one the schema revokes from. Without this, pointing OLIVARES_TEST_POSTGRES_DSN
// at a differently-named role would silently stop testing the append-only ACL while
// every test still passed.
func TestAppRoleMismatchRefuses(t *testing.T) {
	if msg := appRoleMismatch(dialect.DefaultAppRole); msg != "" {
		t.Errorf("the production role must be accepted, got refusal %q", msg)
	}
	for _, bad := range []string{"olv_ta_deadbeef", "postgres", "olivares_admin", ""} {
		msg := appRoleMismatch(bad)
		if msg == "" {
			t.Errorf("role %q was accepted; it is not %q, so the append-only REVOKE would target nobody", bad, dialect.DefaultAppRole)
			continue
		}
		if !strings.Contains(msg, dialect.DefaultAppRole) || !strings.Contains(msg, EnvAppDSN) {
			t.Errorf("refusal for %q must name both the expected role and the variable to fix, got %q", bad, msg)
		}
	}
}

// TestTheMajorClaimIsWhatWasDeclaredAndNothingElse pins the pure half of the §2.6.1 assertion,
// including the case the first version got WRONG.
//
// That version read the four-server matrix DSN as "this run is a certified leg" and refused a run
// that carried it without a declaration. The same variable turns on the LOCAL matrix mode that
// guardmajormatrix_pg_test.go's header documents and that this lane's own resumption brief hands
// out complete, so the refusal broke a versioned workflow — measured, failing in 0.00s under
// exactly the documented environment.
//
// "I could not look" is a real third answer and lives inside the declared path. "Nobody asked me
// to look" is a different state, and this harness cannot tell a CI leg from a developer running
// the same matrix by hand. Closing that gap is the job's: four PG_MAJOR_ASSERTED receipts.
func TestTheMajorClaimIsWhatWasDeclaredAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name, expect string
		want         majorClaim
	}{
		{"nothing declared: nobody claimed anything", "", majorClaimNone},
		{"a major declared", "16", majorClaimDeclared},
		{"whitespace is not a declaration", "   ", majorClaimNone},
		{"a declaration with surrounding space is still one", " 17 ", majorClaimDeclared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyMajorClaim(tc.expect); got != tc.want {
				t.Errorf("classifyMajorClaim(%q) = %d, want %d", tc.expect, got, tc.want)
			}
		})
	}
}

// TestTheDocumentedLocalMatrixModeStillWorks is the regression for what the first version broke.
//
// The four-server DSN alone, with a superuser DSN and no declaration, is the environment the
// matrix test's header and this lane's resumption brief both publish. It must reach a server, not
// a refusal. Asserting it here rather than in sqlstore keeps the property next to the code that
// decides it.
func TestTheDocumentedLocalMatrixModeStillWorks(t *testing.T) {
	super := os.Getenv(EnvSuperuserDSN)
	if super == "" {
		t.Skipf("set %s to exercise the documented local matrix environment", EnvSuperuserDSN)
	}
	t.Setenv(EnvExpectMajor, "")
	t.Setenv("OLIVARES_TEST_POSTGRES_MAJOR_DSNS", "15=a,16=b,17=c,18=d")
	if err := verifyExpectedMajor(t, super); err != nil {
		t.Fatalf("the documented local matrix environment was refused: %v", err)
	}
}

// TestTheDeclaredMajorIsProvenOnTheHarnessOwnConnection is the half that needs a server, and it
// is the one pg-majors.yml says out loud it does not cover.
//
// The job's own probe asks each PORT for server_version_num and emits PG_MAJOR_DSN_VERIFIED. Its
// comment names the gap: "a harness that ignored the DSN would not be caught by this line". The
// DSN pointing at major N and the harness USING it are two claims, and four legs could report
// four majors while every test ran against one server.
func TestTheDeclaredMajorIsProvenOnTheHarnessOwnConnection(t *testing.T) {
	super := os.Getenv(EnvSuperuserDSN)
	if super == "" {
		t.Skipf("set %s to prove the declared major against a real server; TestTheMajorClaimHasThreeAnswers covers the decision itself with no server at all", EnvSuperuserDSN)
	}
	db, err := sql.Open("pgx", super)
	if err != nil {
		t.Fatalf("open the superuser DSN: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var num int
	if err := db.QueryRowContext(context.Background(), `SELECT current_setting('server_version_num')::int`).Scan(&num); err != nil {
		t.Fatalf("read server_version_num: %v", err)
	}
	// The connected major is READ, never assumed: a fixture that hard-coded one would start
	// asserting against a server nobody pointed it at the day CI moved image.
	connected := num / 10000

	t.Run("the declared major is the connected one", func(t *testing.T) {
		t.Setenv(EnvExpectMajor, strconv.Itoa(connected))
		if err := verifyExpectedMajor(t, super); err != nil {
			t.Fatalf("this harness is connected to PostgreSQL %d and a pass declaring %d was refused: %v", connected, connected, err)
		}
	})

	t.Run("a pass measuring another server is REFUSED naming both", func(t *testing.T) {
		other := connected + 1
		t.Setenv(EnvExpectMajor, strconv.Itoa(other))
		err := verifyExpectedMajor(t, super)
		if err == nil {
			t.Fatalf("this harness is connected to PostgreSQL %d, the pass declared %d, and it was accepted: the matrix would report one leg per major while every test ran on the same server",
				connected, other)
		}
		// BOTH numbers, because one of them alone sends an operator to the wrong end: "wrong
		// major" is either a DSN that points somewhere else or a declaration that is stale.
		for _, must := range []string{strconv.Itoa(connected), strconv.Itoa(other)} {
			if !strings.Contains(err.Error(), must) {
				t.Errorf("the refusal never names %s: %v", must, err)
			}
		}
	})

	t.Run("a declaration that is not a number is UNVERIFIED", func(t *testing.T) {
		t.Setenv(EnvExpectMajor, "sixteen")
		err := verifyExpectedMajor(t, super)
		if err == nil {
			t.Fatal("a pass declaring something this harness cannot compare was accepted")
		}
		if !strings.Contains(err.Error(), "UNVERIFIED") {
			t.Errorf("the refusal does not label itself UNVERIFIED: %v", err)
		}
	})

	t.Run("a run that cannot look is UNVERIFIED, not a pass", func(t *testing.T) {
		t.Setenv(EnvExpectMajor, strconv.Itoa(connected))
		err := verifyExpectedMajor(t, "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=2")
		if err == nil {
			t.Fatal("a pass whose server could not be reached was accepted: a gate that cannot tell clean from I-could-not-look approves without examining anything")
		}
		if !strings.Contains(err.Error(), "UNVERIFIED") {
			t.Errorf("the refusal does not label itself UNVERIFIED: %v", err)
		}
	})
}

// envWiringDoor sends the helper process at ONE of the two production entry points.
const envWiringDoor = "OLIVARES_PGTEST_WIRING_DOOR"

// TestIsolateAndProvisionRefuseBeforeTheyProvision is the WIRING regression, and it exists
// because a check can be correct and never called.
//
// The server-backed test above drives verifyExpectedMajor DIRECTLY, so deleting both production
// call sites would leave every other regression in this file green. That is level two of this
// campaign's coverage taxonomy: the mutant that cuts the wire rather than the predicate, and the
// contrast was right that a manual run on one SHA is not a regression.
//
// It re-executes this test binary as a helper process, because the assertion ends the test with
// t.Fatal and a Fatal cannot be observed from inside the process it happens in. The Provisioner
// handed in PANICS: if it ever runs, the assertion did not come first — so this pins the ORDER as
// well as the call, which matters because refusing after a database has been created is a
// different and worse behavior.
func TestIsolateAndProvisionRefuseBeforeTheyProvision(t *testing.T) {
	super := os.Getenv(EnvSuperuserDSN)
	if super == "" {
		t.Skipf("set %s to prove the wiring against a real server", EnvSuperuserDSN)
	}
	if door := os.Getenv(envWiringDoor); door != "" {
		refuseBeforeProvisioning(t, door, super)
		return
	}

	db, err := sql.Open("pgx", super)
	if err != nil {
		t.Fatalf("open the superuser DSN: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var num int
	if err := db.QueryRowContext(context.Background(), `SELECT current_setting('server_version_num')::int`).Scan(&num); err != nil {
		t.Fatalf("read server_version_num: %v", err)
	}
	wrong := strconv.Itoa(num/10000 + 1)

	for _, door := range []string{"Isolate", "Provision"} {
		t.Run(door, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestIsolateAndProvisionRefuseBeforeTheyProvision$", "-test.v")
			cmd.Env = append(os.Environ(), envWiringDoor+"="+door, EnvExpectMajor+"="+wrong)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s ran to completion while %s declared PostgreSQL %s against a server that is not one: the assertion is not wired into it\n%s",
					door, EnvExpectMajor, wrong, out)
			}
			// It must fail for THIS reason. A helper that died of a missing binary or a
			// compile error would satisfy err != nil and prove nothing.
			if !bytes.Contains(out, []byte("declares this pass measures PostgreSQL")) {
				t.Errorf("%s failed for some other reason, so this says nothing about the wiring:\n%s", door, out)
			}
			if bytes.Contains(out, []byte("the provisioner ran")) {
				t.Errorf("%s refused only AFTER provisioning started: a database was created against a server nobody had proven\n%s", door, out)
			}
		})
	}
}

// refuseBeforeProvisioning is the helper process's body: one entry point, a provisioner that
// must never run, and no cleanup to do because nothing should be created.
func refuseBeforeProvisioning(t *testing.T, door, super string) {
	provision := func(context.Context, string, store.PgProvisionSpec, bool) (store.PgProvisionResult, error) {
		panic("the provisioner ran: the declared-major assertion did not come first")
	}
	switch door {
	case "Isolate":
		Isolate(t, provision, SingleRole)
	case "Provision":
		Provision(t, super, store.PgProvisionSpec{Database: "olv_wiring_never_created"}, provision)
	default:
		t.Fatalf("unknown door %q", door)
	}
	t.Fatalf("%s returned without refusing a declared major that is not the connected one", door)
}

// TestLockSharedRoleExcludesAConcurrentProvisioner pins the property that makes the
// cluster-global application role safe to mutate from a test: LockSharedRole takes the
// SAME advisory lock Provision takes, so the two cannot overlap.
//
// WHY IT IS A DETERMINISTIC TEST OF A RACE. The bug it guards against was measured on
// 2026-08-06 (mainline-ci run 31102617448) and is probabilistic by nature: two backends
// updating one pg_authid tuple raise `tuple concurrently updated`, but only when they
// land together. Asserting on the race itself would be a flaky test of a flaky bug.
// The EXCLUSION, though, is a total property and is testable outright: while the lock is
// held, a pg_try_advisory_lock on the same key from another session must FAIL.
//
// It discriminates every way the fix could rot, and each of these was checked by mutation:
//
//   - LockSharedRole keyed on a DIFFERENT string  -> the try SUCCEEDS while held -> red.
//   - LockSharedRole not taking the lock at all   -> the try SUCCEEDS while held -> red.
//   - the release not releasing                   -> the try still FAILS after   -> red.
//
// It deliberately does NOT re-measure the race. The race is measured once, by hand, and
// written down; a suite that tries to reproduce it on every run buys flakiness, not proof.
func TestLockSharedRoleExcludesAConcurrentProvisioner(t *testing.T) {
	super := os.Getenv(EnvSuperuserDSN)
	if super == "" {
		t.Skipf("set %s: this leg needs a real server to prove two SESSIONS contend", EnvSuperuserDSN)
	}
	ctx := context.Background()

	// The observer is a SEPARATE session, which is the whole point: an advisory lock is
	// session-scoped and re-entrant within one session, so probing on the holder's own
	// connection would succeed no matter what and the test would assert nothing.
	observer, err := sql.Open("pgx", super)
	if err != nil {
		t.Fatalf("open the observer connection: %v", err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	conn, err := observer.Conn(ctx)
	if err != nil {
		t.Fatalf("pin the observer connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// tryTake reports whether ANOTHER session can take provisionLockKey right now. The key
	// is derived from the same constant and the same hash the implementation uses, so a
	// change to either is a change to both — a hard-coded number here would keep passing
	// after the implementation moved to a different lock.
	tryTake := func() bool {
		var got bool
		if err := conn.QueryRowContext(ctx,
			`SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, provisionLockKey).Scan(&got); err != nil {
			t.Fatalf("try the provisioning lock from the observer: %v", err)
		}
		if got {
			// Give it straight back: leaving it held would poison every later Provision in
			// this binary and the failure would surface somewhere else entirely.
			if _, err := conn.ExecContext(ctx,
				`SELECT pg_advisory_unlock(hashtextextended($1, 0))`, provisionLockKey); err != nil {
				t.Fatalf("release the lock the observer just took: %v", err)
			}
		}
		return got
	}

	// THIS TEST MUST NOT ASSUME IT IS ALONE, and the first version of it did — which is the
	// same mistake as the bug it guards. It opened by requiring the lock to be FREE, so that a
	// server where somebody else held it could not make the exclusion pass vacuously. Measured
	// in the gate: `go test ./...` runs packages in PARALLEL and several of them provision
	// through this very lock, so a legitimate holder turned the test red. A false red about
	// concurrency, inside the fixture that exists to make concurrency safe.
	//
	// The vacuity it was guarding against is real, but the guard was wrong. Taking the lock
	// FIRST removes it outright: LockSharedRole waits its turn (that is its contract), so from
	// the moment it returns THIS session is the holder, and the exclusion below is about this
	// code rather than about whatever the server happened to be doing.
	release := LockSharedRole(t)
	if tryTake() {
		release()
		t.Fatal("another session took the provisioning lock while LockSharedRole held it: the two are not on the same key, so a test mutating the cluster-global role still races every package's Provision")
	}

	// AND THE RELEASE. Bounded retry rather than one shot, for the same reason: another lane
	// may grab the lock in the instant after ours frees it, and that is correct behavior, not
	// a leak. A leak is different in kind — it never frees — so a window this wide separates
	// them: under any real workload the lock is idle between provisions many times over 30 s,
	// while a lock nobody owns stays held for ever.
	release()
	freed := false
	for i := 0; i < 60 && !freed; i++ {
		if tryTake() {
			freed = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !freed {
		t.Fatal("the provisioning lock never became takeable in 30s after LockSharedRole released it: every later Provision in this cluster would queue behind a lock nobody owns")
	}
}

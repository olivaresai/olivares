// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package pgtest provisions ISOLATED Postgres databases for the suites that
// exercise the real engine against a real server.
//
// WHY. CI points OLIVARES_TEST_POSTGRES_DSN, _ADMIN_DSN and
// _SUPERUSER_DSN at ONE database named `olivares`
// (.github/workflows/mainline-ci.yml), so every Postgres-backed test in the run
// shared it. That is unsound because parts of the schema are GLOBAL rather than
// tenant-scoped — most sharply the auth partition's `users` table: once ANY test
// creates a user, core/auth.HasAnyUser reports true for the whole database and
// BootstrapSuperadmin answers ErrSetupComplete, so every later /v1/setup gets
// 409. Unique tenant slugs cannot fix a global singleton. The same shared
// database also hands every test the one `olivares.leader.v1` advisory lock and
// the one `olivares.migrate.v1` DDL lock.
//
// Isolate gives a test its own DATABASE, created and dropped around it. The APP
// role stays the production one (dialect.DefaultAppRole): it is cluster-global and
// shared by every isolated database, so minting one per test would multiply login
// roles on whatever server the suite is pointed at. The FORCE-RLS tenant backstop IS
// exercised against this role (it is NOSUPERUSER NOBYPASSRLS, and requirePosture
// verifies it).
//
// The append-only ACL no longer depends on that choice. The revoke used to name a
// compile-time constant, so a per-test role would have left it targeting nobody;
// it now follows the role the application pool actually authenticates as
// (dialect.NewForAppRole, bound from the connection's own posture at boot), and the
// store re-asserts and then VERIFIES it on every boot. The regression that was
// missing here — a TRUNCATE/UPDATE refused as the app role, under a role whose name
// is NOT the default — lives in
// core/internal/store/sqlstore/appendonly_acl_pg_test.go and goes through Provision
// rather than Isolate for exactly that reason.
//
// The ADMIN and owner roles are minted PER TEST and dropped, because neither name
// is baked into anything.
//
// The provisioning is delegated to the PRODUCTION path (`olivares db init`'s
// ProvisionPostgres) through the Provisioner function parameter instead of an
// import. That keeps this package a LEAF: core/internal/store/sqlstore's tests
// are in-package (`package sqlstore`, they reach the unexported pool), so a
// helper that imported sqlstore — directly or via core/engine — would form
// sqlstore -> pgtest -> sqlstore and fail to build. Passing the function in
// avoids the edge while keeping ONE implementation of the isolation semantics.
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for the maintenance connection

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// Environment variables the Postgres-backed suites are gated on.
const (
	// EnvSuperuserDSN is the maintenance DSN isolated databases are provisioned
	// from. Its presence is what enables every Postgres leg.
	EnvSuperuserDSN = "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN"
	// EnvAppDSN no longer selects a DATABASE — each test gets its own. It now
	// supplies the application ROLE's name and password, so the isolated databases
	// are owned by the same role CI provisions and production documents.
	EnvAppDSN = "OLIVARES_TEST_POSTGRES_DSN"
	// EnvAdminDSN is the legacy shared BYPASSRLS DSN. pgtest no longer reads
	// credentials from it — it mints a per-test admin role — but a run that sets it
	// WITHOUT the superuser DSN is still a loud misconfiguration rather than a
	// silent skip.
	EnvAdminDSN = "OLIVARES_TEST_POSTGRES_ADMIN_DSN"
	// EnvExpectMajor is a matrix pass's declaration "this run measures PostgreSQL
	// major N" (CI spec §2.6.1). See assertExpectedMajor for what the harness owes
	// a run that declares one.
	EnvExpectMajor = "OLIVARES_TEST_PG_EXPECT_MAJOR"
	// EnvRequired declares "this run MUST have a working Postgres". It decides what
	// an UNREACHABLE server means, and nothing else — see Available.
	//
	// It exists because "configured" and "available" are different facts and this
	// package used to answer the first while being asked the second. On a developer
	// box a dead server is an absence and the honest answer is a skip that SAYS SO;
	// in CI the same silence would delete a Postgres leg from a run that was built to
	// have one. Only the run itself knows which it is, so only the run can declare it.
	EnvRequired = "OLIVARES_TEST_POSTGRES_REQUIRED"
)

// majorClaim is what a run has said about the major it measures.
type majorClaim int

const (
	// majorClaimNone: nobody declared a major. There is nothing for this harness to
	// prove, and inventing something to prove is how a check starts refusing correct
	// runs.
	majorClaimNone majorClaim = iota
	// majorClaimDeclared: a major is declared. The harness must prove it on its OWN
	// connection.
	majorClaimDeclared
)

// classifyMajorClaim is the PURE decision, split out so both branches are testable
// without a server — the same reason classify above is pure.
//
// WHAT THIS DELIBERATELY DOES NOT DECIDE, and the first version got it wrong. It read
// OLIVARES_TEST_POSTGRES_MAJOR_DSNS as the marker of "this run is a certified leg", so
// a run carrying the matrix and no declaration was refused as UNVERIFIED. That
// conflated two different states:
//
//	"I could not look"      — a real third answer, and it lives inside the declared
//	                          path below: the version is unreadable or unparseable.
//	"nobody asked me to look" — not the same thing, and not this package's business.
//
// The four-server DSN ALSO turns on the local matrix mode that
// core/internal/store/sqlstore/guardmajormatrix_pg_test.go documents in its header and
// that the lane's own resumption brief hands out complete. Refusing there broke a
// versioned workflow, measured: TestPostgresGuardShapeDeclarationMatchesTheLiveCatalog
// failed in 0.00s under exactly the documented environment.
//
// A leg that FORGOT to declare its major is a real gap and it is the JOB's to close,
// not the harness's: the harness has no way to tell a CI leg from a developer running
// the same matrix locally, and CI-detection variables are set by mainline-ci too. The
// spec already states the closing condition as four PG_MAJOR_ASSERTED receipts (§5.4);
// scripts/pg-majors-evaluate.py consumes only PG_MAJOR_DSN_VERIFIED today, which is
// the hub's file and a proposal rather than a change made here.
func classifyMajorClaim(expectMajor string) majorClaim {
	if strings.TrimSpace(expectMajor) != "" {
		return majorClaimDeclared
	}
	return majorClaimNone
}

// assertExpectedMajor proves, ON THIS HARNESS'S OWN CONNECTION, that the server it
// provisions from is the major the pass declared.
//
// WHY THE JOB'S OWN PROBE IS NOT ENOUGH, in the job's own words. pg-majors.yml emits
// PG_MAJOR_DSN_VERIFIED per leg by asking each port for server_version_num, and its
// comment says exactly what that leaves open: "a harness that ignored the DSN would
// not be caught by this line". The DSN pointing at major N and the harness USING it
// are two different claims, and only one of them was ever checked. Four legs could
// report four majors while every test ran against the same server.
//
// THREE ANSWERS, NEVER TWO:
//
//   - correct — the declared major and the connected server agree: emit
//     PG_MAJOR_ASSERTED and go on.
//   - incorrect — they disagree: refuse, naming BOTH, because "wrong major" and
//     "some Postgres error" send an operator to different places.
//   - could not look — a major IS declared and the version cannot be read or parsed:
//     refuse as UNVERIFIED. A gate that cannot tell "clean" from "I could not look"
//     approves without examining anything, which is the failure this repository has
//     now paid for in three separate lanes.
//
// A run that declares NOTHING is none of those three: see classifyMajorClaim for why
// that is the job's gap to close and not this package's.
//
// It runs ONCE per process (the verdict is cached and re-applied), because every
// isolated database in a pass provisions from the same DSN and a roundtrip per test
// buys nothing. The receipt is emitted once for the same reason; pg-majors runs
// `go test -json`, which reports t.Log for PASSING tests — plain `go test` does not,
// measured — so the receipt survives into the log the evaluator reads.
var (
	majorAssertOnce sync.Once
	majorAssertErr  error
)

func assertExpectedMajor(tb testing.TB, superDSN string) {
	tb.Helper()
	majorAssertOnce.Do(func() { majorAssertErr = verifyExpectedMajor(tb, superDSN) })
	// Re-applied on EVERY call, not just the first. sync.Once runs the body once; a
	// later caller that skipped the check because someone else had already failed it
	// would be a test running against an unproven server.
	if majorAssertErr != nil {
		tb.Fatal(majorAssertErr)
	}
}

func verifyExpectedMajor(tb testing.TB, superDSN string) error {
	tb.Helper()
	expect := os.Getenv(EnvExpectMajor)
	if classifyMajorClaim(expect) == majorClaimNone {
		return nil
	}
	want, err := strconv.Atoi(strings.TrimSpace(expect))
	if err != nil || want <= 0 {
		return fmt.Errorf("UNVERIFIED: %s is %q, which is not a PostgreSQL major number: the pass declares something this harness cannot compare against a server",
			EnvExpectMajor, expect)
	}
	db, err := sql.Open("pgx", superDSN)
	if err != nil {
		return fmt.Errorf("UNVERIFIED: %s declares this pass measures PostgreSQL %d and the superuser DSN could not be opened: %w",
			EnvExpectMajor, want, err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var num int
	if err := db.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&num); err != nil {
		return fmt.Errorf("UNVERIFIED: %s declares this pass measures PostgreSQL %d and server_version_num could not be read from the DSN this harness provisions from: %w. A pass that cannot look is not a pass",
			EnvExpectMajor, want, err)
	}
	// server_version_num is major*10000 + minor from 10 onward, which covers the whole
	// supported range; nothing here has to reason about the 9.x three-part scheme.
	got := num / 10000
	if got != want {
		return fmt.Errorf("%s declares this pass measures PostgreSQL %d and the DSN this harness provisions from is PostgreSQL %d (server_version_num=%d). "+
			"The matrix would report a leg per major while the tests ran somewhere else",
			EnvExpectMajor, want, got, num)
	}
	tb.Logf("PG_MAJOR_ASSERTED|expected=%d|connected=%d|server_version_num=%d", want, got, num)
	return nil
}

// defaultAdminRole is the SHARED admin role CI provisions. pgtest deliberately does
// NOT use it — it mints a per-test admin role instead — and names it here only so
// teardown can refuse to drop it. There are no invented passwords in this package:
// see appRole for why one would be destructive.
const defaultAdminRole = "olivares_admin"

// Provisioner is the production provisioning entry point
// (core/engine.ProvisionPostgres, i.e. sqlstore.ProvisionPostgres). It is taken
// as a parameter rather than imported so this package stays a leaf; see the
// package doc.
type Provisioner func(ctx context.Context, superuserDSN string, spec store.PgProvisionSpec, execute bool) (store.PgProvisionResult, error)

// Mode selects the privilege topology of the isolated database.
type Mode int

const (
	// SingleRole makes the application role itself the database owner. This is
	// the posture CI provisioned for the shared database
	// (deploy/postgres/01-app-role.sql, mainline-ci.yml), and therefore the
	// drop-in for suites that previously used OLIVARES_TEST_POSTGRES_DSN.
	SingleRole Mode = iota
	// SplitOwner provisions a SEPARATE least-privilege owner role that owns the
	// database and runs DDL/migrations, with the app role holding only DML via
	// ALTER DEFAULT PRIVILEGES — the store.Config.OwnerDSN topology.
	SplitOwner
)

// DSNs are the connection strings for one isolated database. Every role is
// NOSUPERUSER; App and Owner are additionally NOBYPASSRLS, so row-level security
// is enforced against them for real.
type DSNs struct {
	// Database is the provisioned database name (useful in failure messages).
	Database string
	// App is the runtime-traffic role: NOSUPERUSER NOBYPASSRLS.
	App string
	// Owner owns the database and runs DDL. In SingleRole mode it is identical
	// to App.
	Owner string
	// Admin is the cross-tenant read role: NOSUPERUSER BYPASSRLS.
	Admin string
	// Superuser is the maintenance role pointed at THIS database. It exists for
	// the adversarial tests that must present a privileged role to the boot guard
	// without doing so against a database other tests are using.
	Superuser string
	// Result is what the production provisioner reported, so a test whose subject
	// IS the provisioning (the DSN hints, the verified role postures) can assert
	// on it without having to drive ProvisionPostgres by hand.
	Result store.PgProvisionResult
}

// gate is the PURE gating decision, split out from Available/Isolate so it can be
// unit-tested without a Postgres server (the provisioning itself cannot be).
type gate int

const (
	// gateSkip: no Postgres configured at all — skip the leg.
	gateSkip gate = iota
	// gateRun: a superuser DSN is present, so an isolated database can be made.
	gateRun
	// gateMisconfigured: the legacy shared DSN is set but the superuser DSN is
	// not. That combination used to mean "Postgres is configured", so silently
	// skipping would DELETE a Postgres leg from a run that expected it.
	gateMisconfigured
)

func classify(superDSN, appDSN, adminDSN string) gate {
	switch {
	case superDSN != "":
		return gateRun
	case appDSN != "", adminDSN != "":
		// Either legacy DSN alone used to mean "Postgres is configured".
		return gateMisconfigured
	default:
		return gateSkip
	}
}

// misconfiguredMsg names the variable that ACTUALLY tripped the gate, so the
// remediation it offers is one the operator can follow. Naming only EnvAppDSN sent
// anyone whose admin DSN tripped it down a dead end.
func misconfiguredMsg(appDSN, adminDSN string) string {
	var set []string
	if appDSN != "" {
		set = append(set, EnvAppDSN)
	}
	if adminDSN != "" {
		set = append(set, EnvAdminDSN)
	}
	which := strings.Join(set, " and ")
	return fmt.Sprintf("%s is set but %s is not: isolated test databases are provisioned from the "+
		"superuser DSN, so the Postgres leg would silently vanish. Set %s (see "+
		".github/workflows/mainline-ci.yml) or unset %s to skip Postgres deliberately.",
		which, EnvSuperuserDSN, EnvSuperuserDSN, which)
}

// String redacts. Every DSN field carries a password, so the default %v/%+v of this
// struct would print four of them; one stray t.Logf("%+v", pg) in any suite would
// put them in a CI log that gets posted as a PR comment.
func (d DSNs) String() string {
	return fmt.Sprintf("pgtest.DSNs{Database: %q, App/Owner/Admin/Superuser: <redacted DSNs>}", d.Database)
}

// GoString redacts for %#v as well.
func (d DSNs) GoString() string { return d.String() }

// Available reports whether an isolated Postgres can be provisioned. It runs the
// SAME validation Isolate will — not just the env gate — so a misconfiguration
// fails at the top of the test, where the message is attributable, instead of deep
// inside a subtest. It FAILS tb on a misconfiguration rather than answering false,
// so a Postgres leg can never silently vanish.
//
// ⛔ It also OPENS A CONNECTION, and until 2026-08-14 it did not. Every branch above
// reads environment variables and parses strings; a server that is configured and
// dead answered `true`, and the suites went on to fail deep inside provisioning —
// which is how the canon's "the full gate passes PARTIALLY" line became, in practice,
// a red nobody could attribute. The function is called Available and it was reporting
// CONFIGURED. Naming a probe after the property you want does not make it measure it.
//
// There are four answers, not two, and the fourth is the one that was missing:
//
//	nothing configured                  -> false, skip (an absence, honestly stated)
//	legacy DSN without the superuser one -> FATAL   (a misconfiguration)
//	configured and reachable             -> true
//	configured and UNREACHABLE           -> FATAL when the run declared EnvRequired,
//	                                        otherwise false WITH the reason logged
//
// The last line is the whole design. Answering false in silence is what a probe does
// when it would rather not be the bearer of bad news, and it is exactly how a leg
// disappears from CI without anyone noticing. Only the run knows whether Postgres is
// an expectation or a convenience, so the run declares it and this decides nothing.
func Available(tb testing.TB) bool {
	tb.Helper()
	switch classify(os.Getenv(EnvSuperuserDSN), os.Getenv(EnvAppDSN), os.Getenv(EnvAdminDSN)) {
	case gateMisconfigured:
		tb.Fatal(misconfiguredMsg(os.Getenv(EnvAppDSN), os.Getenv(EnvAdminDSN)))
		return false // unreachable from a test goroutine; explicit so a stray caller cannot fall through to "unavailable"
	case gateSkip:
		return false
	}
	superDSN := os.Getenv(EnvSuperuserDSN)
	if _, err := parseDSN(superDSN); err != nil {
		tb.Fatalf("%s must be a libpq URL (postgres://user:pw@host:port/db?sslmode=…): %v", EnvSuperuserDSN, err)
		return false
	}
	if _, err := appRole(); err != nil {
		tb.Fatal(err)
		return false
	}
	required, err := postgresRequired()
	if err != nil {
		tb.Fatal(err)
		return false
	}
	if err := reachErr(superDSN); err != nil {
		if required {
			tb.Fatalf("UNVERIFIED: %s declares this run REQUIRES PostgreSQL and the configured server does not answer within %s: %v. "+
				"A leg that was built to run is not allowed to vanish quietly", EnvRequired, reachProbeTimeout, err)
			return false
		}
		// Not a failure here, but it is NOT "clean" either, and the difference has to
		// reach the log: a reader scanning for why a Postgres leg produced nothing
		// gets the reason and the one-variable way to turn it into a red.
		tb.Logf("PG_UNREACHABLE|skipped|%s is set but the server does not answer within %s: %v|set %s=1 to make this a FAILURE instead",
			EnvSuperuserDSN, reachProbeTimeout, err, EnvRequired)
		return false
	}
	return true
}

// reachProbeTimeout bounds the reachability probe. It is deliberately short: this
// runs before any test body, and a box with no server should learn that in seconds
// rather than inheriting the driver's default wait.
const reachProbeTimeout = 5 * time.Second

var (
	reachMu    sync.Mutex
	reachCache = map[string]error{}
)

// reachErr answers whether superDSN's server ANSWERS, caching the verdict per DSN.
//
// The cache is what makes this affordable: Available has thirteen call sites and each
// runs per test. The first caller for a DSN pays the probe while holding the mutex —
// parallel tests block behind it once, which is a worse trade than a per-key once and
// a much better one than thirteen sockets, and it keeps the failure attributable to
// one place.
func reachErr(superDSN string) error {
	reachMu.Lock()
	defer reachMu.Unlock()
	if err, seen := reachCache[superDSN]; seen {
		return err
	}
	err := probeReach(superDSN)
	reachCache[superDSN] = err
	return err
}

// probeReach opens the maintenance connection and pings it. sql.Open alone proves
// nothing — it does not connect — which is the same shape of mistake this whole
// change is about, so the ping is the point and not a flourish.
func probeReach(superDSN string) error {
	db, err := sql.Open("pgx", superDSN)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), reachProbeTimeout)
	defer cancel()
	return db.PingContext(ctx)
}

// postgresRequired reads EnvRequired. An unparseable value is a misconfiguration,
// not a "no": someone who writes OLIVARES_TEST_POSTGRES_REQUIRED=maybe is trying to
// say something, and silently reading it as false is how a CI run stops requiring
// the thing its author believed it required.
func postgresRequired() (bool, error) {
	raw := strings.TrimSpace(os.Getenv(EnvRequired))
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s is %q, which is not a boolean (1/0, true/false): this harness will not guess whether this run requires PostgreSQL", EnvRequired, raw)
	}
	return v, nil
}

// Isolate provisions a private DATABASE for tb — owned by the PRODUCTION
// application role, not a per-test one (see the comment on the app role below) —
// and registers a t.Cleanup that drops it. It SKIPS tb when no superuser DSN is
// configured (no Postgres server available), and FAILS tb when a legacy shared
// DSN is set without the superuser DSN — that combination used to mean "Postgres
// is configured", so a silent skip would delete a Postgres leg from the run.
//
// Cleanup ordering is correct by construction: Isolate registers its drop before
// the caller opens a store (whose own Close cleanup is registered later), and
// t.Cleanup runs LIFO, so the store is closed before its database is dropped.
func Isolate(tb testing.TB, provision Provisioner, mode Mode) DSNs {
	tb.Helper()
	super := os.Getenv(EnvSuperuserDSN)
	switch classify(super, os.Getenv(EnvAppDSN), os.Getenv(EnvAdminDSN)) {
	case gateMisconfigured:
		tb.Fatal(misconfiguredMsg(os.Getenv(EnvAppDSN), os.Getenv(EnvAdminDSN)))
	case gateSkip:
		tb.Skipf("set %s to run the Postgres leg (an isolated database is provisioned per test)", EnvSuperuserDSN)
	}
	superURL, err := parseDSN(super)
	if err != nil {
		tb.Fatalf("%s must be a libpq URL (postgres://user:pw@host:port/db?sslmode=…): %v", EnvSuperuserDSN, err)
	}
	// BEFORE ANY DATABASE IS CREATED. A pass that is measuring the wrong server should say so
	// instead of provisioning against it and reporting whatever that server happens to answer.
	assertExpectedMajor(tb, super)

	names := newIdentifiers(Suffix(tb))
	database := names.database
	// The APP role is deliberately NOT per-test: it is cluster-global, shared by
	// every isolated database, and absent from tempRoles below so teardown can never
	// drop it out from under a test running beside this one. Isolation is the
	// DATABASE's job.
	//
	// This used to be load-bearing for correctness as well — the append-only REVOKE
	// named a compile-time constant, so a freshly minted per-test role would have
	// left that ACL protecting nobody while every assertion still passed. It no
	// longer is: the revoke follows the role the application pool authenticates as.
	app, err := appRole()
	if err != nil {
		tb.Fatal(err)
	}
	// The ADMIN role, unlike the app role, has no name baked into the schema — so it
	// is minted PER TEST and dropped afterwards. Reusing a fixed `olivares_admin`
	// with a password from this repo would leave a permanent BYPASSRLS login role
	// (which defeats the FORCE-RLS tenant backstop) on whatever server the suite was
	// pointed at.
	admin := store.PgRole{Name: names.tempAdmin, Password: Suffix(tb)}

	spec := store.PgProvisionSpec{
		Database: database,
		App:      app,
		Admin:    &admin,
		SSLMode:  superURL.Query().Get("sslmode"),
	}
	// tempRoles is exactly what teardown may DROP. The app role is absent from it on
	// purpose: it is cluster-global and reused by every isolated database, so
	// dropping it would break the tests running beside this one.
	tempRoles := []string{admin.Name}
	owner := app
	if mode == SplitOwner {
		owner = store.PgRole{Name: names.tempOwner, Password: Suffix(tb)}
		spec.Owner = owner
		tempRoles = append(tempRoles, owner.Name)
	}

	res := Provision(tb, super, spec, provision, tempRoles...)
	// A nil error is NOT proof the roles are usable: ProvisionPostgres records a
	// failed verification reconnect in the posture rather than returning an error,
	// so an unreachable or privilege-drifted role would otherwise surface later as
	// a confusing failure inside the test under study.
	requirePosture(tb, "app", res.AppPosture)
	requirePosture(tb, "admin", res.AdminPosture)
	if mode == SplitOwner {
		requirePosture(tb, "owner", res.OwnerPosture)
	}

	return DSNs{
		Database: database,
		App:      roleDSN(superURL, app.Name, app.Password, database),
		Owner:    roleDSN(superURL, owner.Name, owner.Password, database),
		Admin:    roleDSN(superURL, admin.Name, admin.Password, database),
		Superuser: func() string {
			u := *superURL
			u.Path = "/" + database
			return u.String()
		}(),
		Result: res,
	}
}

// appRoleMismatch returns a refusal message when name is not the role these
// fixtures provision for, and "" when it is. Pure, so it is unit-testable without a
// server.
//
// The refusal is kept, but its reason has changed and the message must not claim the
// old one. It USED to be that the schema hard-coded the role into its append-only
// REVOKE, so any other name silently disabled that ACL; the revoke now follows the
// effective role, so that hazard is gone. What remains is fixture ownership: this
// role is cluster-global, shared by every isolated database and deliberately absent
// from the teardown set, so pointing the suite at some other role would leave
// Isolate provisioning databases owned by a role it neither created nor may drop.
// A test that genuinely needs a different app role should call Provision directly
// with its own spec — see appendonly_acl_pg_test.go in sqlstore.
func appRoleMismatch(name string) string {
	if name == dialect.DefaultAppRole {
		return ""
	}
	return fmt.Sprintf("%s names role %q, but these fixtures provision for %q and never drop it "+
		"(it is cluster-global and shared by every isolated database). Point the DSN at %q, unset it, "+
		"or call pgtest.Provision directly with your own spec if the test needs another role.",
		EnvAppDSN, name, dialect.DefaultAppRole, dialect.DefaultAppRole)
}

// RequireAbsent fails tb unless the database AND every temporary role are absent,
// so Isolate only ever drops what it created. Both are covered: teardown drops
// both, so adopting either would destroy someone else's object.
func RequireAbsent(tb testing.TB, superDSN, database string, tempRoles ...string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", superDSN)
	if err != nil {
		tb.Fatalf("pgtest: open maintenance connection: %v", err)
	}
	defer db.Close() //nolint:errcheck

	probe := func(query, kind, name string) {
		var exists bool
		if err := db.QueryRowContext(ctx, query, name).Scan(&exists); err != nil {
			tb.Fatalf("pgtest: probe for %s %q: %v", kind, name, err)
		}
		if exists {
			tb.Fatalf("pgtest: %s %q already exists; refusing to adopt (and later DROP) an object this test did not create", kind, name)
		}
	}
	probe(`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, "database", database)
	for _, role := range tempRoles {
		probe(`SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)`, "role", role)
	}
}

// provisionLockKey names the advisory lock that serializes test provisioning.
const provisionLockKey = "olivares.pgtest.provision.v1"

// lockProvisioning takes provisionLockKey on a PINNED connection (an advisory lock
// is session-scoped, so lock and unlock must share one connection) and returns the
// release.
//
// It FAILS CLOSED. Continuing unserialized was wrong: the lock is what stops two
// concurrently-running test binaries from issuing ALTER ROLE against the same
// pg_authid tuple, so "could not lock, carry on" walks straight into the race the
// lock exists to prevent — and does so silently.
//
// The caller MUST `defer` the returned release. It is not merely tidy: tb.Fatalf
// between acquisition and release runs runtime.Goexit, which runs deferred calls
// but not ordinary ones, and a leaked session-scoped lock on a connection that is
// never closed would block every later Isolate in the process.
func lockProvisioning(ctx context.Context, tb testing.TB, superDSN string) func() {
	tb.Helper()
	db, err := sql.Open("pgx", superDSN)
	if err != nil {
		tb.Fatalf("pgtest: open maintenance connection for the provisioning lock: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		tb.Fatalf("pgtest: acquire a connection for the provisioning lock: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		`SELECT pg_advisory_lock(hashtextextended($1, 0))`, provisionLockKey); err != nil {
		_ = conn.Close()
		_ = db.Close()
		tb.Fatalf("pgtest: take the provisioning lock %q: %v. Provisioning mutates the SHARED application "+
			"role, so it is not safe to continue unserialized.", provisionLockKey, err)
	}
	return func() {
		// Bounded: an unbounded WithoutCancel here would hang teardown forever on an
		// unresponsive server. Closing the connection releases the lock regardless.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, provisionLockKey)
		_ = conn.Close()
		_ = db.Close()
	}
}

// LockSharedRole takes provisionLockKey for a test that MUTATES the cluster-global
// application role itself, and returns the release. It is the OTHER HALF of that
// lock, and without it the lock never was mutual exclusion.
//
// WHY IT EXISTS (measured 2026-08-06, mainline-ci run 31102617448). lockProvisioning
// states the lock's purpose exactly: it "stops two concurrently-running test binaries
// from issuing ALTER ROLE against the same pg_authid tuple". But only Provision ever
// took it. A test that ALTERs the shared role directly — guardrollout's split-topology
// case flips it NOINHERIT — bypassed the very lock built for that statement, so one
// writer was serialized and the other was not. PostgreSQL answers two backends updating
// one pg_authid tuple with `tuple concurrently updated` (SQLSTATE XX000), and it
// surfaced in whichever package happened to be inside Provision at that instant:
// a red test that never contained the defect, in a different package on every run.
//
// The exclusion is NOT only about the catalog write. The application role is
// cluster-global by design (see the package doc), so while it is NOINHERIT every other
// isolated database on the server sees a role stripped of its implicit membership in
// pg_database_owner — the mutation is visible far beyond the test that made it. The
// lock therefore has to span the whole window the role is altered, not just the ALTER.
//
// CONTRACT, and it is not optional:
//
//   - Call it AFTER provisioning, never inside it. The lock is session-scoped and this
//     takes a NEW connection, so acquiring it while Provision holds it deadlocks until
//     the 90 s deadline turns it into a red test rather than a hang.
//   - Register the release with t.Cleanup BEFORE registering the restore of whatever
//     you are about to change. Cleanups run LIFO, so that ordering releases the lock
//     AFTER the role is put back, which is the only ordering that keeps the window closed.
//
// It FAILS CLOSED for the same reason lockProvisioning does: continuing unserialized
// walks straight into the race the lock exists to prevent.
func LockSharedRole(tb testing.TB) func() {
	tb.Helper()
	super := os.Getenv(EnvSuperuserDSN)
	if super == "" {
		tb.Fatalf("pgtest: LockSharedRole needs %s: mutating the cluster-global application role "+
			"without the provisioning lock races every other package's Provision.", EnvSuperuserDSN)
	}
	// SEPARATE deadline from the caller's: a queue behind this lock is a delay, and a
	// delay must not be charged to the test's own budget.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	release := lockProvisioning(ctx, tb, super)
	return func() {
		release()
		cancel()
	}
}

// Provision is the ownership-safe provisioning sequence, shared by Isolate and by
// the tests whose SUBJECT is ProvisionPostgres itself: take the cluster-wide
// provisioning lock, prove every name this call will later DROP is absent, register
// teardown, then provision — all inside the lock, so the absence proof cannot be
// invalidated between the probe and the CREATE.
//
// It matters because ProvisionPostgres is idempotent: ensureDatabase ALTERs an
// EXISTING database's owner rather than failing. Without the absence proof a name
// collision would silently ADOPT someone else's database and then authorize
// teardown to drop it.
//
// tempRoles are the roles teardown may remove; never pass a shared production role.
func Provision(tb testing.TB, superDSN string, spec store.PgProvisionSpec, provision Provisioner, tempRoles ...string) store.PgProvisionResult {
	tb.Helper()
	// Isolate is not the only door: the append-only ACL regression provisions directly, so the
	// assertion belongs at BOTH entry points or a matrix pass could reach a server through the
	// one that does not check.
	assertExpectedMajor(tb, superDSN)
	lockCtx, cancelLock := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelLock()
	// SEPARATE deadlines: a single context covering both the lock WAIT and the
	// provisioning meant a queue could consume the whole budget and then hand
	// provisioning an already-expired context, turning a delay into a hard red test.
	unlock := lockProvisioning(lockCtx, tb, superDSN)
	defer unlock()

	RequireAbsent(tb, superDSN, spec.Database, tempRoles...)
	tb.Cleanup(func() { Drop(tb, superDSN, spec.Database, tempRoles...) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := provision(ctx, superDSN, spec, true)
	if err != nil {
		tb.Fatalf("provision isolated postgres database %q: %v", spec.Database, err)
	}
	return res
}

// requirePosture fails tb unless the provisioner actually reconnected as role and
// found the privilege posture that role is supposed to have. Every role is checked;
// the admin role is not waved through, it is held to a DIFFERENT bar — it must
// bypass RLS (that is its purpose) but must still not be a superuser.
func requirePosture(tb testing.TB, label string, p *store.RolePosture) {
	tb.Helper()
	if p == nil {
		tb.Fatalf("provisioning reported no %s role posture: the role was never verified", label)
		return // unreachable with a real TB (Fatalf ends in runtime.Goexit); see below
	}
	if !p.Reachable {
		tb.Fatalf("provisioned %s role %q is not reachable: %s", label, p.Role, p.Err)
	}
	if label == "admin" {
		// A non-BYPASSRLS admin makes cross-tenant reads silently return nothing; a
		// SUPERUSER admin passes every check for the wrong reason.
		if !p.BypassRLS {
			tb.Fatalf("provisioned admin role %q lacks BYPASSRLS: cross-tenant reads would silently return empty", p.Role)
		}
		if p.Superuser {
			tb.Fatalf("provisioned admin role %q is a SUPERUSER: it must be NOSUPERUSER BYPASSRLS, or the test proves nothing about the admin pool", p.Role)
		}
		return
	}
	if p.RLSUnsafe() {
		tb.Fatalf("provisioned %s role %q is %s: row-level security would be inert and the test would prove nothing",
			label, p.Role, p.Why())
	}
}

// parseDSN parses a libpq URL and returns an error that CANNOT leak the password.
//
// url.Parse fails with a *url.Error whose Error() embeds the RAW connection
// string — password included — and these errors are printed by tb.Fatalf, teed
// into $RUNNER_TEMP/ci-fail-*.log and posted verbatim as a PR comment by
// .github/actions/pr-failure-report. pgx strips the same wrapper for the same
// reason. Unwrapping keeps the useful cause and drops the URL.
func parseDSN(dsn string) (*url.URL, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // drop urlErr.URL: it is the DSN, with the password in it
		}
		return nil, fmt.Errorf("not a parseable URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("missing scheme or host")
	}
	return u, nil
}

// appRole returns the application role the isolated databases are provisioned for.
//
// Its NAME is fixed to dialect.DefaultAppRole because these fixtures neither create
// nor drop it (see Isolate) — not, as this comment used to say, because the schema
// compiles that name into its append-only REVOKE; the revoke now follows the role
// the application actually connects as.
// Its PASSWORD must come from EnvAppDSN and is never invented, because
// ProvisionPostgres re-asserts a role's password on every call: inventing one would
// ROTATE the shared, production-named role's password on whatever server the suite
// was pointed at — which, for a role named `olivares_app` on a dev or staging box,
// is a destructive act with a password published in this repository.
func appRole() (store.PgRole, error) {
	dsn := os.Getenv(EnvAppDSN)
	if dsn == "" {
		return store.PgRole{}, fmt.Errorf("%s is set but %s is not: the isolated databases are owned by the "+
			"fixed role %q (cluster-global, provisioned outside these fixtures), and its password must come "+
			"from %s rather than be invented — inventing one would rotate that role's password on this server. "+
			"See .github/workflows/mainline-ci.yml for the pair CI sets.",
			EnvSuperuserDSN, EnvAppDSN, dialect.DefaultAppRole, EnvAppDSN)
	}
	u, err := parseDSN(dsn)
	if err != nil {
		return store.PgRole{}, fmt.Errorf("%s must be a libpq URL carrying credentials "+
			"(postgres://role:password@host:port/db): %v", EnvAppDSN, err)
	}
	if u.User == nil {
		return store.PgRole{}, fmt.Errorf("%s carries no credentials: it must be postgres://role:password@host:port/db", EnvAppDSN)
	}
	name := u.User.Username()
	pw, hasPw := u.User.Password()
	if !hasPw || pw == "" {
		return store.PgRole{}, fmt.Errorf("%s carries no password: it must be postgres://role:password@host:port/db, "+
			"because provisioning re-asserts this role's password and a guessed one would rotate it", EnvAppDSN)
	}
	if msg := appRoleMismatch(name); msg != "" {
		return store.PgRole{}, errors.New(msg)
	}
	return store.PgRole{Name: name, Password: pw}, nil
}

type identifiers struct{ database, tempOwner, tempAdmin string }

// procTag is the OS-process marker every generated identifier carries, so a role
// or database can be attributed to the test BINARY that created it.
//
// `go test ./a/... ./b/...` runs one process per package, CONCURRENTLY, against the
// same server. Cluster-scoped objects are global, so a package auditing its own
// fixtures sees another package's live objects appear inside its before/after window
// and cannot tell them from its own leak. That is not hypothetical: adding the first
// pgtest user outside sqlstore made sqlstore's cluster-state guard report a leaked
// olv_tx_ role that belonged to the concurrent package and was later cleaned up
// correctly. Attribution is the missing fact, so it is carried in the name.
func procTag() string { return "p" + strconv.Itoa(os.Getpid()) + "_" }

// ForeignFixtureObject reports whether name is a pgtest-generated database or role
// belonging to a DIFFERENT test process. A cluster-state audit uses it to skip
// objects it could never be responsible for, WITHOUT weakening what it does own:
// a name that carries no process tag (a hand-named fixture role) is never foreign,
// and a name carrying THIS process's tag is never foreign either.
func ForeignFixtureObject(name string) bool {
	i := strings.Index(name, "_p")
	if i < 0 {
		return false
	}
	rest := name[i+2:]
	end := strings.IndexByte(rest, '_')
	if end <= 0 {
		return false
	}
	pid, err := strconv.Atoi(rest[:end])
	if err != nil {
		return false
	}
	return pid != os.Getpid()
}

// newIdentifiers derives those names. Split out so a unit test can assert they
// satisfy the provisioning guard (validIdent: a plain lower-case SQL identifier of
// at most 63 characters) without needing a server.
func newIdentifiers(suffix string) identifiers {
	return identifiers{
		database:  "olv_t_" + suffix,
		tempOwner: "olv_to_" + suffix,
		tempAdmin: "olv_tx_" + suffix,
	}
}

// safeIdent mirrors the provisioning guard: a plain lower-case SQL identifier.
// Every name here is machine-generated, so a mismatch is a bug in this package —
// but the DROP statements interpolate identifiers (Postgres cannot bind them), so
// they are validated rather than trusted.
var safeIdent = regexp.MustCompile(store.SafeIdentPattern)

// roleDSN rewrites the superuser URL's credentials and database, keeping its
// host, port and query (sslmode). Rewriting the parsed URL — rather than doing
// string surgery on the DSN — keeps passwords correctly escaped.
func roleDSN(super *url.URL, role, password, database string) string {
	u := *super
	u.User = url.UserPassword(role, password)
	u.Path = "/" + database
	return u.String()
}

// Suffix returns 32 lower-case hex characters (128 bits) from crypto/rand, for
// tests that build their own database/role names instead of calling Isolate.
//
// 128 bits, not 64: these names authorize a DESTRUCTIVE teardown
// (DROP DATABASE / DROP ROLE), so the margin against any accidental reuse of a
// name that another run is still using should not be the cheap one.
//
// It deliberately does NOT reuse the suites' uniqueSuffix() helper, which is
// string(model.NewID())[:8] over a UUIDv7: those 8 characters are the TOP 32 bits
// of the 48-bit millisecond timestamp, so they are identical for every call
// within the same ~65 second window (measured: 1000 consecutive calls produced
// one distinct value). Colliding database names would silently rejoin two tests
// on one database, which is the exact defect this package exists to remove.
//
// It is prefixed with the OS process id (procTag) so a cluster-scoped object can be
// attributed to the test binary that created it — see ForeignFixtureObject. The
// random half is unchanged and still carries the uniqueness; the tag only adds
// provenance. 63-char identifier budget: the longest prefix is "olv_evfence_" (12)
// plus "p<pid>_" (≤9) plus 32 hex = 53.
func Suffix(tb testing.TB) string {
	tb.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		tb.Fatalf("pgtest: read entropy: %v", err)
	}
	return procTag() + hex.EncodeToString(b[:])
}

// Drop removes database and then any TEMPORARY roles passed in. Teardown failures
// are reported (a leaked database accumulates on the CI server and would
// eventually break provisioning). The database goes first on purpose: a role's
// grants and ALTER DEFAULT PRIVILEGES entries live inside it and vanish with it,
// so DROP ROLE afterwards has no dependency left to trip over.
//
// Callers must NOT pass the shared production roles: they are cluster-global and
// reused by every isolated database, so dropping one would break the tests running
// beside this one.
func Drop(tb testing.TB, superDSN, database string, roles ...string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	db, err := sql.Open("pgx", superDSN)
	if err != nil {
		tb.Errorf("pgtest: open maintenance connection to drop %q: %v", database, err)
		return
	}
	defer db.Close() //nolint:errcheck

	// WITH (FORCE) terminates remaining backends AND drops in one statement
	// (Postgres 13+; CI runs postgres:16). The two-step
	// pg_terminate_backend-then-DROP it replaces had a reconnect race: a pooled
	// connection could re-attach between the two statements and fail the drop.
	if msg := refuseUnowned("database", database); msg != "" {
		tb.Error(msg)
		return
	}
	for _, role := range roles {
		if msg := refuseUnowned("role", role); msg != "" {
			tb.Error(msg)
			return
		}
	}
	if err := execIdent(ctx, db, "DROP DATABASE IF EXISTS %s WITH (FORCE)", database); err != nil {
		tb.Errorf("pgtest: drop database %q: %v", database, err)
		// The roles still hold privileges inside the surviving database; dropping
		// them would fail with a confusing dependency error. Leave them with the
		// database so the leak is one object group, not two.
		return
	}
	for _, role := range roles {
		if err := execIdent(ctx, db, "DROP ROLE IF EXISTS %s", role); err != nil {
			tb.Errorf("pgtest: drop role %q: %v", role, err)
		}
	}
}

// ownedDatabase / ownedTempRole are the name shapes this package mints. Teardown
// refuses anything else: Drop is exported and takes free-form strings, so without
// this the "never pass a shared role" contract would live only in a doc comment —
// and pgtest.Drop(tb, dsn, "olivares", "olivares_admin") is shape-valid SQL.
//
// The optional `p<pid>_` group is the process tag Suffix mints (see procTag). It is
// OPTIONAL rather than required so a server still carrying untagged objects from a
// binary built before can still be torn down: tightening this to tagged-only
// would make teardown REFUSE those, converting a stale name into a permanent leak —
// the precise failure this package's own suite caught when the tag was added and
// these patterns were not.
var (
	ownedDatabase = regexp.MustCompile(`^olv_(t_)?(p[0-9]+_)?[0-9a-f]{8,}$`)
	ownedTempRole = regexp.MustCompile(`^olv_(to|tx|app|own)_(p[0-9]+_)?[0-9a-f]{8,}$`)
)

// refuseUnowned returns a refusal message when name is not something this package
// mints, and "" when it is. Pure, so it is unit-testable without a server.
func refuseUnowned(kind, name string) string {
	pattern := ownedDatabase
	if kind == "role" {
		pattern = ownedTempRole
	}
	if pattern.MatchString(name) && name != dialect.DefaultAppRole && name != defaultAdminRole {
		return ""
	}
	return fmt.Sprintf("pgtest: refusing to DROP %s %q: it does not match the name shape this package mints "+
		"(%s). Teardown only ever removes objects pgtest created; the shared production roles and any real "+
		"database are off limits.", kind, name, pattern)
}

// execIdent runs a statement whose only dynamic part is a validated identifier.
func execIdent(ctx context.Context, db *sql.DB, format, ident string) error {
	if !safeIdent.MatchString(ident) {
		return fmt.Errorf("refusing to interpolate unsafe identifier %q", ident)
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf(format, ident))
	return err
}

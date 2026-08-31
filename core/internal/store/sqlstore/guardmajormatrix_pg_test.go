// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardmajormatrix_pg_test.go is the per-major certification the shape comparison and the
// escalation closure were both waiting for.
//
// WHY IT COULD NOT EXIST BEFORE. Two of this campaign's findings — the CHECK predicate compared
// only on the major this repository had run, and the PostgreSQL 16 membership model that
// pg_has_role(..., 'MEMBER') over-reports — were blocked on the same missing fact: a server of
// each supported major. The container had 15 and nothing else, so the honest answer was a
// declared limitation. It now has 15, 16, 17 and 18, and a declared limitation that CAN be
// measured is a defect rather than a limit.
//
// HOW TO POINT IT AT THEM. One env var, `OLIVARES_TEST_POSTGRES_MAJOR_DSNS`, holding
// `major=dsn` pairs separated by commas:
//
// It must name EXACTLY the certified majors — the parser below refuses a partial matrix — so the
// example carries all four. The previous one listed 16, 17 and 18 and failed when copied
// verbatim, which is a documentation bug that costs a real minute every time.
//
//	OLIVARES_TEST_POSTGRES_MAJOR_DSNS='15=postgres://postgres:postgres@127.0.0.1:5556/postgres?sslmode=disable,16=…:5557…,17=…:5558…,18=…:5559…'
//
// When it is unset AND no pass declares a major, these tests SKIP and the message names exactly
// which majors went unmeasured. When a pass DOES declare one, an absent matrix is refused instead
// — see classifyMatrixEnv — because a silent skip on a matrix test is indistinguishable from a
// matrix that passed, which is the failure mode this file exists to end.

// matrixScope is what keeps two lanes running this matrix at once from destroying each other.
//
// THE SERVERS ON 5436/5437/5438 ARE SHARED. A database or role named for the major alone —
// `olv_shape_16` — is the same name in every container, and scratchDatabaseOn opens with
// `DROP DATABASE IF EXISTS … WITH (FORCE)`, which would evict the other lane's connections and
// take its fixtures with them. That is the failure mode with a different surface: work
// that is invisible until it collides.
//
// The process id is the discriminator because it is unique among the processes that could be
// running concurrently on this host and stable for the life of the test binary, which is what
// the cleanups registered against these names need.
func matrixScope() string { return strconv.Itoa(os.Getpid()) }

// majorDSN is one server the matrix runs against.
type majorDSN struct {
	Major int
	DSN   string
}

// envMatrixDSNs and envExpectMajor are the two variables the majors job sets.
//
// envExpectMajor is the job's per-pass declaration "this run measures major N" (CI spec §2.6.1).
// It will grow a second reader when the harness asserts the connected major against the server;
// here it is used only as the marker of a pass that CLAIMS to be measuring one.
const (
	envMatrixDSNs  = "OLIVARES_TEST_POSTGRES_MAJOR_DSNS"
	envExpectMajor = "OLIVARES_TEST_PG_EXPECT_MAJOR"
)

// matrixEnvVerdict is what a run must do with the matrix environment it was handed.
type matrixEnvVerdict int

const (
	// matrixEnvRun: the DSNs are there. Measure.
	matrixEnvRun matrixEnvVerdict = iota
	// matrixEnvUncovered: nobody claimed this run measures a major and there is no matrix.
	// Skipping is the honest answer, and the message names what went unmeasured.
	matrixEnvUncovered
	// matrixEnvIncomplete: the run DECLARES it is measuring a specific major and carries no
	// matrix. Skipping there would delete three tests from the one job that exists to run them.
	matrixEnvIncomplete
)

// classifyMatrixEnv decides between the three, and is PURE so all three are testable without a
// server — the same reason pgtest.appRoleMismatch is pure.
//
// WHY THE MARKER IS envExpectMajor AND NOT OLIVARES_GATE_STRICT_PG. They are different claims.
// STRICT_PG says "a run without PostgreSQL must fail"; envExpectMajor says "this pass asserts it
// is measuring major N", which is exactly the condition under which silently dropping the matrix
// tests is unacceptable. Keying on the first would couple two unrelated properties.
//
// The failure this closes was MEASURED, not imagined: the majors workflow exported envExpectMajor
// per pass and no matrix DSNs, so all three matrix tests self-skipped in all four passes — and the
// job's evaluator, which fails on any skip in a matrix package, would have gone red naming the
// count and not the cause. The hub fixed its half; this is the half that keeps the hole shut if
// that line is ever refactored away.
func classifyMatrixEnv(matrixDSNs, expectMajor string) matrixEnvVerdict {
	switch {
	case strings.TrimSpace(matrixDSNs) != "":
		return matrixEnvRun
	case strings.TrimSpace(expectMajor) != "":
		return matrixEnvIncomplete
	default:
		return matrixEnvUncovered
	}
}

// matrixEnvReporter is the slice of testing.TB that turning a verdict into an action needs.
//
// IT EXISTS BECAUSE THE VERDICT WAS TESTED AND THE ACTION WAS NOT. Round nine's contrast
// mutated `Fatalf` to `Skipf` at the site below and every test in this file stayed green,
// matrices 15..18 included: classifyMatrixEnv was covered by a pure table, and nothing checked
// that the harness OBEYED it. A refusal nobody exercises is a skip with better prose — which is
// exactly the hole the refusal was added to close, one level further down.
type matrixEnvReporter interface {
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
}

// applyMatrixEnvVerdict performs the action a verdict demands and reports whether the caller may
// go on. It is the seam that makes the mapping testable without a server and without a subprocess.
//
// THE BOOLEAN IS NOT DECORATION. Under a real *testing.T both Fatalf and Skipf end the goroutine,
// so returning is unreachable there; under the fake reporter a regression drives, nothing stops,
// and a function that relied on Goexit to enforce its own control flow would run on and report a
// second failure for a different reason. Making the cut explicit is what lets the property be
// observed instead of inferred from a runtime side effect.
func applyMatrixEnvVerdict(r matrixEnvReporter, v matrixEnvVerdict, expectMajor string) bool {
	switch v {
	case matrixEnvIncomplete:
		r.Fatalf("this run declares %s=%s, so it is a matrix pass, and %s is empty. Skipping here would delete the matrix tests from the one job that exists to run them, and its zero-skip rule would report a count instead of this cause. Export all four as 15=…,16=…,17=…,18=… in EVERY pass",
			envExpectMajor, expectMajor, envMatrixDSNs)
		return false
	case matrixEnvUncovered:
		r.Skipf("%s is unset, so majors %d..%d are NOT covered by this run; the declared shape is certified only on %d",
			envMatrixDSNs, supportedPostgresMajorMin, supportedPostgresMajorMax, verifiedPostgresMajor)
		return false
	case matrixEnvRun:
	}
	return true
}

// postgresMajorMatrix reads the environment and hands the WHOLE decision to resolveMatrixEnv.
//
// It is two lines on purpose. Everything a mutant could bend lives below, where a fake reporter
// can watch it.
func postgresMajorMatrix(t *testing.T) []majorDSN {
	t.Helper()
	return refuseAnEmptyMatrix(t,
		resolveMatrixEnv(t, strings.TrimSpace(os.Getenv(envMatrixDSNs)), os.Getenv(envExpectMajor)))
}

// coverageOf records which majors a matrix test ACTUALLY exercised, and assertCoveredEveryMajor
// makes the test fail if that set is not the certified one.
//
// THIS LIVES IN THE CONSUMER ON PURPOSE, and round twelve is why. Every guard I put on the
// PRODUCER side — the verdict, the action, the wiring between them, the empty-slice gate — was
// defeated by a mutant that replaced the producer wholesale: `postgresMajorMatrix -> return nil`
// left six tests green over zero servers with `certified=[]`. That is the fourth level of the
// same defect, and the lesson generalises: a guard on the path can always be removed by a
// mutation that removes the path. What cannot be removed that way is a test asserting what IT
// measured — the producer can lie about what it returns, and the consumer still counts nothing.
// THE UNIT OF COVERAGE IS THE PAIR, not the major, and round fifteen is why. Truncating the four
// adversarial vectors to one removed TWELVE of sixteen measurements and the package still reported
// 861 pass, 0 skip, 0 fail — over the CI floor of 438. Counting majors said "all four reached"
// while three quarters of what those majors were reached FOR had been deleted. A coverage unit
// coarser than the thing being varied cannot see the variation disappear.
type majorCoverage struct {
	mu   sync.Mutex
	seen map[string]bool
	want func() []string
}

func newMajorCoverage() *majorCoverage {
	return &majorCoverage{seen: map[string]bool{}, want: everyCertifiedMajorOnce}
}

// newMajorCaseCoverage requires the full cross product of majors and named cases.
func newMajorCaseCoverage(names []string) *majorCoverage {
	return &majorCoverage{seen: map[string]bool{}, want: func() []string {
		var out []string
		for _, m := range certifiedPostgresMajors() {
			for _, n := range names {
				out = append(out, fmt.Sprintf("%d/%s", m, n))
			}
		}
		return out
	}}
}

func everyCertifiedMajorOnce() []string {
	var out []string
	for _, m := range certifiedPostgresMajors() {
		out = append(out, strconv.Itoa(m))
	}
	return out
}

func (c *majorCoverage) mark(major int) { c.markCase(strconv.Itoa(major)) }

// requireCases raises the bar from "every major" to "every major x every case", and is called by
// the table-driven test AFTER its case list exists.
func (c *majorCoverage) requireCases(names []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.want = func() []string {
		var out []string
		for _, m := range certifiedPostgresMajors() {
			for _, n := range names {
				out = append(out, fmt.Sprintf("%d/%s", m, n))
			}
		}
		return out
	}
}

// markCase records one (major, case) pair. Guarded because the subtests run in parallel.
func (c *majorCoverage) markCase(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[key] = true
}

func (c *majorCoverage) assertCoveredEveryCertifiedMajor(t *testing.T) {
	t.Helper()
	// AN HONEST SKIP IS NOT A FALSE GREEN, and telling them apart is the whole subtlety.
	//
	// This reads the environment ITSELF rather than asking the producer what it read, because
	// round thirteen refuted the previous shape by making the producer ignore its input: it then
	// classified the run as uncovered, SKIPPED, and the skip happened before the cleanup was even
	// registered. Registering first fixed the ordering and left the other half — a skip still
	// silences the check — so the check now decides for itself whether a skip was legitimate.
	//
	// No matrix in the environment: not covered here, and the skip says so. A matrix IS in the
	// environment and the test still measured nothing: that is the false green, whatever the
	// producer decided on the way.
	if strings.TrimSpace(os.Getenv(envMatrixDSNs)) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var missing []string
	for _, want := range c.want() {
		if !c.seen[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		seen := make([]string, 0, len(c.seen))
		for k := range c.seen {
			seen = append(seen, k)
		}
		sort.Strings(seen)
		t.Errorf("this test measured %v and never reached %v: it reports success over ground it did not cover, which is the false green the whole matrix exists to prevent",
			seen, missing)
	}
}

// refuseAnEmptyMatrix is the last gate, and round eleven is why it exists.
//
// Making applyMatrixEnvVerdict return false for matrixEnvRun sent resolveMatrixEnv down its early
// return, so every matrix test received an EMPTY slice, iterated nothing, logged `certified=[]`
// and passed — five green tests, zero skips, nothing measured. Closing a hole with an early
// return opened a vacuity one line below it, which is the third time in this campaign that a fix
// moved the defect instead of removing it.
//
// A matrix test that iterates zero servers proves less than no test, because it reports success.
func refuseAnEmptyMatrix(r matrixEnvReporter, out []majorDSN) []majorDSN {
	if len(out) == 0 {
		r.Fatalf("the matrix resolved to ZERO servers while this run was neither refused nor skipped: a matrix test that iterates nothing passes vacuously and logs certified=[], which is exactly the false green %s exists to prevent",
			envMatrixDSNs)
	}
	return out
}

// resolveMatrixEnv is classify + obey + parse in ONE testable call, and that composition is the
// point.
//
// ROUND TEN BROKE THE PREVIOUS SHAPE AND THE LESSON IS GENERAL. There were two seams —
// classifyMatrixEnv and applyMatrixEnvVerdict — each with its own test, each green. The contrast
// left both untouched and mutated the CALL SITE instead, degrading the verdict on its way from
// one to the other: `applyMatrixEnvVerdict(t, matrixEnvUncovered, expect)`. Every test stayed
// green while the matrix passes silently skipped. Testing two halves is not testing the whole,
// and the wiring between them is code like any other.
//
// So the seam is the whole path now. A fake reporter driving this function sees whatever the
// classifier decided AND whatever the harness did about it, which is the statement that matters.
func resolveMatrixEnv(r matrixEnvReporter, raw, expect string) []majorDSN {
	if !applyMatrixEnvVerdict(r, classifyMatrixEnv(raw, expect), expect) {
		return nil
	}
	var out []majorDSN
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.Index(pair, "=")
		if eq <= 0 {
			r.Fatalf("OLIVARES_TEST_POSTGRES_MAJOR_DSNS entry %q is not major=dsn", pair)
			return nil
		}
		major, err := strconv.Atoi(strings.TrimSpace(pair[:eq]))
		if err != nil {
			r.Fatalf("OLIVARES_TEST_POSTGRES_MAJOR_DSNS entry %q has a non-numeric major: %v", pair, err)
			return nil
		}
		out = append(out, majorDSN{Major: major, DSN: strings.TrimSpace(pair[eq+1:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Major < out[j].Major })

	// THE SET MUST BE EXACTLY THE CERTIFIED ONE, and a partial env is a FAILURE rather than a
	// pass over fewer servers.
	//
	// Round four measured why: running this file with `16=<dsn>` alone left both matrix tests
	// GREEN and logged `certified=[16] supported=15..18`. A test named EveryMajor that passes
	// without every major is worse than no test — the slice in production says four numbers are
	// certified, and the only thing standing behind that sentence is this run. Skipping when the
	// variable is ABSENT is different and stays: that is "not measured here", and it says so.
	want := certifiedPostgresMajors()
	seen := map[int]int{}
	for _, m := range out {
		seen[m.Major]++
	}
	var missing, duplicated, unexpected []int
	for _, w := range want {
		switch seen[w] {
		case 0:
			missing = append(missing, w)
		case 1:
		default:
			duplicated = append(duplicated, w)
		}
		delete(seen, w)
	}
	for major := range seen {
		unexpected = append(unexpected, major)
	}
	sort.Ints(unexpected)
	if len(missing) > 0 || len(duplicated) > 0 || len(unexpected) > 0 {
		r.Fatalf("OLIVARES_TEST_POSTGRES_MAJOR_DSNS must name exactly the certified majors %v: missing %v, duplicated %v, unexpected %v — a partial matrix cannot certify the range production declares",
			want, missing, duplicated, unexpected)
	}
	return out
}

// scratchDatabaseOn provisions a database of its own on that server and returns a pool for it.
//
// A database per test rather than a shared one, because the control plane's relations are
// created by name in the engine schema and two tests sharing a server would collide on them.
func scratchDatabaseOn(t *testing.T, m majorDSN, name string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	admin, err := sql.Open("pgx", m.DSN)
	if err != nil {
		t.Fatalf("open the admin pool for major %d: %v", m.Major, err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	// Registered BEFORE the database exists, so a failure between the two still drops it.
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
	})
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
		t.Fatalf("clear a stale scratch database on major %d: %v", m.Major, err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatalf("create the scratch database on major %d: %v", m.Major, err)
	}
	db, err := sql.Open("pgx", replaceDatabase(m.DSN, name))
	if err != nil {
		t.Fatalf("open the scratch pool on major %d: %v", m.Major, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// replaceDatabase swaps the database component of a postgres URL.
func replaceDatabase(dsn, name string) string {
	q := ""
	if i := strings.Index(dsn, "?"); i >= 0 {
		q, dsn = dsn[i:], dsn[:i]
	}
	if i := strings.LastIndex(dsn, "/"); i >= 0 {
		dsn = dsn[:i+1] + name
	}
	return dsn + q
}

// TestPostgresTheDeclaredShapeIsTheShapeEveryMajorCreates is F-10's certification.
//
// It applies THIS binary's own control-plane DDL on a scratch database of each major, projects
// the three relations back out of that server's catalog, and compares them against the declared
// shape WITH THE PREDICATE TEXT — the layer production runs only on a certified major. A major
// on which this passes is a major where the strongest comparison can be turned on; a major on
// which it fails tells us exactly which constraint the deparser renders differently, which is
// the certification profile that was missing.
func TestPostgresTheDeclaredShapeIsTheShapeEveryMajorCreates(t *testing.T) {
	coverage := newMajorCoverage()
	t.Cleanup(func() { coverage.assertCoveredEveryCertifiedMajor(t) })
	matrix := postgresMajorMatrix(t)
	ctx := context.Background()
	covered := make([]string, 0, len(matrix))

	for _, m := range matrix {
		t.Run(fmt.Sprintf("major %d", m.Major), func(t *testing.T) {
			db := scratchDatabaseOn(t, m, fmt.Sprintf("olv_shape_%d_%s", m.Major, matrixScope()))
			serverMajor, err := postgresServerMajor(ctx, db)
			if err != nil {
				t.Fatalf("read the server major: %v", err)
			}
			if serverMajor != m.Major {
				t.Fatalf("the DSN labeled major %d reports %d; the matrix would be certifying the wrong server", m.Major, serverMajor)
			}
			if _, err := db.ExecContext(ctx,
				`CREATE FUNCTION `+dialect.BlockMutationFn+`() RETURNS trigger LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION 'immutable'; END $fn$`); err != nil {
				t.Fatalf("create the shared guard function: %v", err)
			}
			dia, ok := dialect.NewForAppRole(store.EnginePostgres, "probe_app")
			if !ok {
				t.Fatal("bind the dialect")
			}
			for i, stmt := range dia.GuardControlPlaneStmts() {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("statement %d of the control plane DDL on major %d: %v", i+1, m.Major, err)
				}
			}

			observed, err := projectGuardControlPlaneShape(ctx, db)
			if err != nil {
				t.Fatalf("project the shape on major %d: %v", m.Major, err)
			}
			divergent := 0
			for _, want := range dialect.GuardControlPlaneShapePostgres() {
				got := observed[want.Relation]
				if !got.Found {
					t.Fatalf("%s does not exist after its own DDL ran on major %d", want.Relation, m.Major)
				}
				// TRUE: the predicate TEXT included. That is the whole question this test asks.
				if diff := guardShapeDifference(want, got, true); diff != "" {
					divergent++
					t.Errorf("on major %d the declared shape of %s does not match what its own DDL created: %s",
						m.Major, want.Relation, diff)
				}
			}
			if divergent == 0 {
				covered = append(covered, strconv.Itoa(m.Major))
				t.Logf("GUARD_SHAPE_CERTIFIED|major=%d|relations=%d|predicates_compared=true",
					m.Major, len(dialect.GuardControlPlaneShapePostgres()))
			}
			// THE MARK IS THE LAST STATEMENT, so it records work DONE rather than an iteration
			// STARTED. Round fourteen made every measurement body skip and the parents stayed
			// green over certified=[15 16 17 18]: marking at loop entry counts the for, not the
			// test. Anything that empties or skips this body now leaves its major unmarked.
			coverage.mark(m.Major)
		})
	}
	t.Logf("GUARD_SHAPE_MATRIX|certified=[%s]|declared_verified=%d|supported=%d..%d",
		strings.Join(covered, " "), verifiedPostgresMajor, supportedPostgresMajorMin, supportedPostgresMajorMax)
}

// TestPostgresTheEscalationPredicateIsRightOnEveryMajor is F-09's other half, measured.
//
// THE THREE CASES ARE THE THREE ANSWERS, and until this ran they were three guesses:
//
//   - a membership granted WITH SET FALSE, INHERIT FALSE, ADMIN FALSE conveys nothing and must
//     be ACCEPTED. pg_has_role(..., 'MEMBER') reports it, which is the over-rejection round two
//     found;
//   - the same inert edge coexisting with a live chain app -> mid -> owner must be REFUSED.
//     This is the counterexample that killed the direct-row refinement: reading the direct
//     edge's options says "conveys nothing" while SET ROLE owner still works;
//   - a membership with ADMIN TRUE and nothing else must be REFUSED: it cannot assume the role
//     today and can grant itself the right to tomorrow.
func TestPostgresTheEscalationPredicateIsRightOnEveryMajor(t *testing.T) {
	coverage := newMajorCoverage()
	t.Cleanup(func() { coverage.assertCoveredEveryCertifiedMajor(t) })
	matrix := postgresMajorMatrix(t)
	ctx := context.Background()

	for _, m := range matrix {
		t.Run(fmt.Sprintf("major %d", m.Major), func(t *testing.T) {
			db := scratchDatabaseOn(t, m, fmt.Sprintf("olv_escal_%d_%s", m.Major, matrixScope()))
			serverMajor, err := postgresServerMajor(ctx, db)
			if err != nil {
				t.Fatalf("read the server major: %v", err)
			}
			// Scoped for the same reason the database is: roles are cluster-global, so a fixed
			// name is the same role in every container sharing this server.
			suffix := strconv.Itoa(m.Major) + "_" + matrixScope()
			roles := []string{"e_app_" + suffix, "e_mid_" + suffix, "e_owner_" + suffix, "e_admin_" + suffix}
			t.Cleanup(func() {
				for _, r := range roles {
					_, _ = db.ExecContext(context.Background(), `DROP ROLE IF EXISTS "`+r+`"`)
				}
			})
			for _, r := range roles {
				if _, err := db.ExecContext(ctx, `DROP ROLE IF EXISTS "`+r+`"`); err != nil {
					t.Fatalf("clear role %s: %v", r, err)
				}
				if _, err := db.ExecContext(ctx, `CREATE ROLE "`+r+`" NOSUPERUSER NOCREATEROLE`); err != nil {
					t.Fatalf("create role %s: %v", r, err)
				}
			}
			app, mid, owner, adminOnly := roles[0], roles[1], roles[2], roles[3]

			// The grant syntax differs by major: the three options arrived in 16.
			inert := `GRANT "` + owner + `" TO "` + app + `"`
			adminGrant := `GRANT "` + adminOnly + `" TO "` + app + `" WITH ADMIN OPTION`
			if serverMajor >= 16 {
				inert = `GRANT "` + owner + `" TO "` + app + `" WITH SET FALSE, INHERIT FALSE, ADMIN FALSE`
				adminGrant = `GRANT "` + adminOnly + `" TO "` + app + `" WITH SET FALSE, INHERIT FALSE, ADMIN TRUE`
			}
			for _, stmt := range []string{inert, adminGrant} {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("provision the membership graph on major %d (%s): %v", m.Major, stmt, err)
				}
			}

			// THE PREDICATE UNDER TEST IS THE PRODUCTION ONE, assembled the way production
			// assembles it: the CTE, then the predicate, with r.oid bound to the role asked
			// about and $2 to the subject.
			reach := func(role string) bool {
				t.Helper()
				var v bool
				q := guardReachableCTE(serverMajor) + `SELECT ` +
					strings.ReplaceAll(guardRoleReachability(serverMajor), "r.oid", "$1::regrole")
				if err := db.QueryRowContext(ctx, strings.ReplaceAll(q, "$2", `'`+app+`'`), role).Scan(&v); err != nil {
					t.Fatalf("evaluate the reachability predicate for %s on major %d: %v", role, m.Major, err)
				}
				return v
			}

			inertReached := reach(owner)
			adminReached := reach(adminOnly)

			// A LIVE CHAIN, added on top of the inert edge. On 15 every membership already
			// conveys SET ROLE, so the inert case cannot exist there and the chain is redundant;
			// the assertions below say so per major rather than pretending one answer fits both.
			if serverMajor >= 16 {
				for _, stmt := range []string{
					`GRANT "` + mid + `" TO "` + app + `" WITH SET TRUE`,
					`GRANT "` + owner + `" TO "` + mid + `" WITH SET TRUE`,
				} {
					if _, err := db.ExecContext(ctx, stmt); err != nil {
						t.Fatalf("provision the live chain on major %d: %v", m.Major, err)
					}
				}
			}
			chainReached := reach(owner)

			switch {
			case serverMajor >= 16:
				if inertReached {
					t.Errorf("on major %d a membership granted WITH SET FALSE, INHERIT FALSE, ADMIN FALSE conveys nothing and was still treated as reachable",
						m.Major)
				}
				if !adminReached {
					t.Errorf("on major %d a membership with ADMIN TRUE lets the role grant itself SET, and was not treated as reachable", m.Major)
				}
				if !chainReached {
					t.Errorf("on major %d an inert direct edge beside a live app->mid->owner chain was treated as unreachable; that is the counterexample the direct-row refinement failed",
						m.Major)
				}
			default:
				if !inertReached {
					t.Errorf("on major %d every membership carries the right to SET ROLE, so it must be reachable", m.Major)
				}
			}
			// AND THE ESCALATION A DIRECT PREDICATE CANNOT SEE: ADMIN over an INTERMEDIARY.
			//
			// app holds ADMIN on mid and nothing at all on a fourth role; mid can SET to that
			// role. pg_has_role(app, fourth, 'SET') is false — "not reachable" — and app can
			// grant ITSELF mid WITH SET at any moment and then reach it. Measured on 16.14 and
			// 18.4 before this closure existed; the transitive CTE is what sees it.
			if serverMajor >= 16 {
				far := "e_far_" + suffix
				t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP ROLE IF EXISTS "`+far+`"`) })
				for _, stmt := range []string{
					`CREATE ROLE "` + far + `" NOSUPERUSER NOCREATEROLE`,
					// app -> adminOnly is already SET FALSE, INHERIT FALSE, ADMIN TRUE.
					`GRANT "` + far + `" TO "` + adminOnly + `" WITH SET TRUE`,
				} {
					if _, err := db.ExecContext(ctx, stmt); err != nil {
						t.Fatalf("provision the ADMIN-over-intermediary graph on major %d: %v", m.Major, err)
					}
				}
				var direct bool
				if err := db.QueryRowContext(ctx,
					`SELECT pg_catalog.pg_has_role($1, $2::regrole, 'SET')`, app, far).Scan(&direct); err != nil {
					t.Fatalf("read the direct predicate: %v", err)
				}
				if direct {
					t.Fatalf("the direct SET predicate already reaches %s, so this case does not model the escalation", far)
				}
				if !reach(far) {
					t.Errorf("on major %d, ADMIN over an intermediary that can SET to %s was treated as unreachable; the role can grant itself the chain at any moment",
						m.Major, far)
				}
				t.Logf("GUARD_ESCALATION_ADMIN_HOP|major=%d|direct_SET=false|closure_reaches=%t", m.Major, reach(far))
			}
			t.Logf("GUARD_ESCALATION_MATRIX|major=%d|inert=%t|admin_only=%t|with_live_chain=%t",
				m.Major, inertReached, adminReached, chainReached)
			// THE MARK IS THE LAST STATEMENT, so it records work DONE rather than an iteration
			// STARTED. Round fourteen made every measurement body skip and the parents stayed
			// green over certified=[15 16 17 18]: marking at loop entry counts the for, not the
			// test. Anything that empties or skips this body now leaves its major unmarked.
			coverage.mark(m.Major)
		})
	}
}

// TestPostgresTheShapeRefusesAnIndexThatEnforcesSomethingElse is round four's F-10, the half
// that certification alone could not answer.
//
// CERTIFYING THAT THE DDL PRODUCES THE DECLARED SHAPE SAYS NOTHING ABOUT WHAT ELSE COMPARES
// EQUAL TO IT. Round four built four divergent shapes against a real PostgreSQL 16 and the
// projector accepted all four, because it read column NAMES and nothing about what the index
// does with them. The inherited child was closed in that same round; these three are the rest,
// and each one keeps the declared columns while enforcing a different rule:
//
//   - INCLUDE moves an attribute out of the uniqueness while leaving it in the projection;
//   - NULLS NOT DISTINCT changes which rows may coexist on nullable key columns;
//   - DEFERRABLE moves enforcement to COMMIT, so a transaction can hold a duplicate throughout.
//
// Every case is applied to a scratch database of EVERY certified major, because "PostgreSQL
// renders this the same everywhere" is exactly the kind of assumption this matrix exists to stop
// anyone making.
func TestPostgresTheShapeRefusesAnIndexThatEnforcesSomethingElse(t *testing.T) {
	coverage := newMajorCoverage()
	t.Cleanup(func() { coverage.assertCoveredEveryCertifiedMajor(t) })
	matrix := postgresMajorMatrix(t)
	ctx := context.Background()
	gate := dialect.GuardGateEventsTable

	// The constraint names are DISCOVERED, not spelled. PostgreSQL truncates a generated
	// constraint name at NAMEDATALEN-1 = 63 characters, so
	// `olivares_guard_gate_events_rollout_id_unit_id_diagnostic_fingerprint_key` exists in the
	// catalog as `..._diagnostic_finger` — and a fixture that builds the name by concatenation
	// fails with 42704 on every major, which is how this was found.
	dropUnique := func(cols int) string {
		return fmt.Sprintf(`DO $mut$ DECLARE n text; BEGIN
  SELECT conname INTO STRICT n FROM pg_catalog.pg_constraint
   WHERE conrelid = '%s'::regclass AND contype = 'u' AND array_length(conkey, 1) = %d;
  EXECUTE 'ALTER TABLE %s DROP CONSTRAINT ' || quote_ident(n);
END $mut$`, gate, cols, gate)
	}

	cases := []struct {
		name string
		// tuple is the uniquely-indexed tuple this vector attacks, and the refusal must name it.
		//
		// MEASURED, and the measurement is why this field exists: three of the four vectors
		// produced the SAME refusal text, so asserting only that SOME difference was found let
		// one vector's SQL be swapped for another's and still pass. Naming the tuple separates
		// nulls_not_distinct from the other three; it does NOT separate those three from each
		// other, which is what `property` below is for.
		tuple string
		// property is the pg_catalog column this vector moves, and the refusal must name it.
		//
		// THIS IS WHAT MAKES THE FOUR REFUSALS DISTINGUISHABLE. The deviation used to be a
		// suffix concatenated onto the string the multiset comparison holds, and a declared
		// tuple sorts BEFORE its annotated twin — so the report always took the "declared and
		// absent" branch and told an operator the index was MISSING while it sat there
		// enforcing another rule. Three of these four vectors read identically because of it,
		// and an operator cannot act on any of them.
		property string
		// mutate replaces a uniqueness with one that carries the same columns and enforces
		// something else. It runs AFTER the canonical DDL.
		mutate []string
	}{{
		name:     "include_payload",
		tuple:    "rollout_id,event_ordinal",
		property: "indnkeyatts",
		mutate: []string{
			dropUnique(2),
			`CREATE UNIQUE INDEX gate_u2_include ON ` + gate + ` (rollout_id) INCLUDE (event_ordinal)`,
		},
	}, {
		name:     "nulls_not_distinct",
		tuple:    "rollout_id,unit_id,diagnostic_fingerprint",
		property: "indnullsnotdistinct",
		mutate: []string{
			dropUnique(3),
			`CREATE UNIQUE INDEX gate_u3_nnd ON ` + gate +
				` (rollout_id, unit_id, diagnostic_fingerprint) NULLS NOT DISTINCT`,
		},
	}, {
		name:     "deferrable_uniqueness",
		tuple:    "rollout_id,event_ordinal",
		property: "indimmediate",
		mutate: []string{
			dropUnique(2),
			`ALTER TABLE ` + gate + ` ADD CONSTRAINT gate_u2_deferred` +
				` UNIQUE (rollout_id, event_ordinal) DEFERRABLE INITIALLY DEFERRED`,
		},
	}, {
		// THE FOURTH VECTOR, and the one the first three did not reach. Round five found that
		// comparing indcollation against attcollation only detects an index that DECLARES a
		// collation other than its column's. Re-typing the COLUMN to a non-deterministic
		// collation leaves every index inheriting it — the two oids still agree — while the
		// equality the uniqueness is built on now treats 'A' and 'a' as the same key. No index
		// is touched, no column name changes, and the rule enforced is a different one.
		name:     "inherited_nondeterministic_collation",
		tuple:    "rollout_id,event_ordinal",
		property: "collisdeterministic",
		mutate: []string{
			`CREATE COLLATION guard_ci (provider = icu, locale = 'und-u-ks-level2', deterministic = false)`,
			`ALTER TABLE ` + gate + ` ALTER COLUMN rollout_id TYPE text COLLATE guard_ci`,
		},
	}, {
		// THE FIFTH VECTOR, and the one the CI spec names as a mandatory negative control
		// (§5.3 point 5): an index that is PRESENT, carries exactly the declared columns and
		// enforces NOTHING AT ALL.
		//
		// indisvalid false is what a CREATE INDEX CONCURRENTLY that never finished leaves behind,
		// and PostgreSQL then ignores the index for queries AND for constraint enforcement. The
		// catalog UPDATE is the deterministic way to produce that state; killing a real CIC is
		// not reproducible. Measured as superuser on 15.18, 16.14, 17.10 and 18.4: the UPDATE is
		// accepted with no allow_system_table_mods, and indisready/indislive stay true — so this
		// vector isolates indisvalid rather than tripping all three at once.
		name:     "unique_not_enforcing",
		tuple:    "rollout_id,event_ordinal",
		property: "indisvalid",
		mutate: []string{
			`DO $mut$ DECLARE ix oid; BEGIN
  SELECT i.indexrelid INTO STRICT ix FROM pg_catalog.pg_index i
   WHERE i.indrelid = '` + gate + `'::regclass AND i.indisunique AND NOT i.indisprimary AND i.indnatts = 2;
  UPDATE pg_catalog.pg_index SET indisvalid = false WHERE indexrelid = ix;
END $mut$`,
		},
	}}

	// THE EXPECTATION IS A LITERAL, NOT A DERIVATION, and round sixteen is why.
	//
	// The previous version built the expected case list FROM `cases`, so truncating `cases`
	// shrank both sides of the comparison and the package passed with 861/0/0. An expectation
	// derived from the thing under test cannot detect that the thing shrank — it is the same
	// defect as the shape table's shared list, which this campaign already paid for once.
	//
	// Written down, so adding a vector is a deliberate edit in TWO places and removing one is
	// impossible to do quietly.
	wantCases := []string{
		"include_payload",
		"nulls_not_distinct",
		"deferrable_uniqueness",
		"inherited_nondeterministic_collation",
		"unique_not_enforcing",
	}
	if len(cases) != len(wantCases) {
		t.Fatalf("the table carries %d vectors and this test expects %d (%v): a vector added without updating the expectation is uncovered, and one removed without updating it is silently gone",
			len(cases), len(wantCases), wantCases)
	}
	for i, tc := range cases {
		if tc.name != wantCases[i] {
			t.Fatalf("vector %d is %q, expected %q: the expectation is a literal so that this mismatch is visible", i, tc.name, wantCases[i])
		}
	}
	coverage.requireCases(wantCases)

	// THE FOUR REFUSALS MUST BE DISTINGUISHABLE FROM EACH OTHER, and that is the property this
	// test could not assert at all before.
	//
	// Asserting per case that the message names its own property does not prove the four are
	// different messages: four vectors could each name a property that some shared preamble
	// mentions. Collecting them and comparing them pairwise does, and it is the criterion an
	// operator actually needs — a refusal is worth something only if it says WHICH of these
	// four happened.
	//
	// Only what was recorded is compared. The count is coverage's job (requireCases above), so
	// this never turns an early failure into a second, derived one.
	var refusalMu sync.Mutex
	refusals := map[int]map[string]string{}
	t.Cleanup(func() {
		refusalMu.Lock()
		defer refusalMu.Unlock()
		majors := make([]int, 0, len(refusals))
		for major := range refusals {
			majors = append(majors, major)
		}
		sort.Ints(majors)
		for _, major := range majors {
			byCase, seen := refusals[major], map[string]string{}
			for _, name := range wantCases {
				text, recorded := byCase[name]
				if !recorded {
					continue
				}
				if other, dup := seen[text]; dup {
					t.Errorf("on major %d the refusals for %q and %q are the SAME text, so an operator reading it cannot tell which catalog property diverged: %s",
						major, other, name, text)
					continue
				}
				seen[text] = name
			}
		}
	})

	for _, m := range matrix {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("major %d/%s", m.Major, tc.name), func(t *testing.T) {
				db := scratchDatabaseOn(t, m, fmt.Sprintf("olv_adv_%d_%s_%s", m.Major, tc.name[:5], matrixScope()))
				if _, err := db.ExecContext(ctx,
					`CREATE FUNCTION `+dialect.BlockMutationFn+`() RETURNS trigger LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION 'immutable'; END $fn$`); err != nil {
					t.Fatalf("create the shared guard function: %v", err)
				}
				dia, ok := dialect.NewForAppRole(store.EnginePostgres, "probe_app")
				if !ok {
					t.Fatal("bind the dialect")
				}
				for i, stmt := range dia.GuardControlPlaneStmts() {
					if _, err := db.ExecContext(ctx, stmt); err != nil {
						t.Fatalf("statement %d of the control plane DDL: %v", i+1, err)
					}
				}
				// The canonical shape must verify FIRST, or a refusal below would prove only that
				// the fixture broke something.
				observed, err := projectGuardControlPlaneShape(ctx, db)
				if err != nil {
					t.Fatalf("project the canonical shape: %v", err)
				}
				for _, want := range dialect.GuardControlPlaneShapePostgres() {
					if diff := guardShapeDifference(want, observed[want.Relation], true); diff != "" {
						t.Fatalf("the canonical DDL does not verify on major %d, so this case cannot measure a divergence: %s", m.Major, diff)
					}
				}

				for _, stmt := range tc.mutate {
					if _, err := db.ExecContext(ctx, stmt); err != nil {
						t.Fatalf("apply the divergence (%s): %v", stmt, err)
					}
				}
				after, err := projectGuardControlPlaneShape(ctx, db)
				if err != nil {
					t.Fatalf("project the divergent shape: %v", err)
				}
				var diff string
				for _, want := range dialect.GuardControlPlaneShapePostgres() {
					if want.Relation != gate {
						continue
					}
					diff = guardShapeDifference(want, after[want.Relation], true)
				}
				// ACCEPTANCE IS CHECKED FIRST. An empty diff cannot contain the tuple either, so
				// the reverse order reported a missing NAME for what is really a missing REFUSAL
				// — two errors, and the loud one second.
				if diff == "" {
					t.Fatalf("on major %d the verifier ACCEPTED a %s uniqueness: the declared columns are there and the rule they enforce is not the declared one",
						m.Major, tc.name)
				}
				if !strings.Contains(diff, tc.tuple) {
					t.Errorf("major %d refused %s with %q, which does not name the tuple %q this vector attacks: a refusal that does not say WHAT diverged cannot tell one vector from another",
						m.Major, tc.name, diff, tc.tuple)
				}
				// AND IT MUST NAME THE CATALOG PROPERTY, which the tuple alone cannot do: all
				// four vectors leave the declared attributes indexed, and what differs is what
				// the index DOES with them. An operator told that a uniquely-indexed tuple is
				// absent goes looking for a missing index — and the index is right there.
				if !strings.Contains(diff, tc.property) {
					t.Errorf("major %d refused %s with %q, which never names %s: that is the pg_catalog column this vector moves, and without it the refusal does not say what to look at",
						m.Major, tc.name, diff, tc.property)
				}
				refusalMu.Lock()
				if refusals[m.Major] == nil {
					refusals[m.Major] = map[string]string{}
				}
				refusals[m.Major][tc.name] = diff
				refusalMu.Unlock()
				t.Logf("GUARD_SHAPE_ADVERSARIAL|major=%d|case=%s|refused=true|property=%s|diff=%s", m.Major, tc.name, tc.property, diff)
				// THE MARK IS THE LAST STATEMENT, so it records work DONE rather than an iteration
				// STARTED. Round fourteen made every measurement body skip and the parents stayed
				// green over certified=[15 16 17 18]: marking at loop entry counts the for, not the
				// test. Anything that empties or skips this body now leaves its pair unmarked.
				coverage.markCase(fmt.Sprintf("%d/%s", m.Major, tc.name))
			})
		}
	}
}

// TestPostgresTheShapeRefusesACheckWeakenedOverTheSameColumns is F-10's other mandatory negative
// control (CI spec §5.3 point 5), and it is the one the DEPARSER can defeat.
//
// A CHECK is compared in three layers (see renderCheck): the columns it constrains, the literals
// its predicate mentions, and the predicate TEXT. Only the third is version-bound, and this
// attacks exactly the case where the first cannot help — replacing
// `gate_condition = ANY (ARRAY['clean','retryable','blocked','verified'])` with
// `gate_condition IS NOT NULL` keeps conkey IDENTICAL while admitting every string the fold has
// no vocabulary for. A comparison that read only the columns called the two equal; that was the
// semantic hole round three found, and it is why the literal multiset exists.
//
// It runs on EVERY certified major because the deparser is the part that moves between majors: a
// comparison that catches this on 15 and not on 18 is not one anybody can rely on. Without a
// negative control the per-major comparison is an assertion that cannot fail.
func TestPostgresTheShapeRefusesACheckWeakenedOverTheSameColumns(t *testing.T) {
	coverage := newMajorCoverage()
	t.Cleanup(func() { coverage.assertCoveredEveryCertifiedMajor(t) })
	matrix := postgresMajorMatrix(t)
	ctx := context.Background()
	gate := dialect.GuardGateEventsTable

	// The constraint is DISCOVERED by what its predicate says, never spelled. PostgreSQL
	// truncates a generated constraint name at NAMEDATALEN-1 = 63 characters, so a fixture that
	// builds one by concatenation fails with 42704 on every major — measured once already, in
	// the adversarial test next door. `retryable` is a member of the declared vocabulary and
	// appears in no other CHECK on this relation, so INTO STRICT is itself the assertion that
	// exactly one was found.
	dropVocabulary := fmt.Sprintf(`DO $mut$ DECLARE n text; BEGIN
  SELECT con.conname INTO STRICT n FROM pg_catalog.pg_constraint con
   WHERE con.conrelid = '%s'::regclass AND con.contype = 'c'
     AND pg_catalog.pg_get_constraintdef(con.oid) LIKE '%%retryable%%';
  EXECUTE 'ALTER TABLE %s DROP CONSTRAINT ' || quote_ident(n);
END $mut$`, gate, gate)

	for _, m := range matrix {
		t.Run(fmt.Sprintf("major %d", m.Major), func(t *testing.T) {
			db := scratchDatabaseOn(t, m, fmt.Sprintf("olv_chk_%d_%s", m.Major, matrixScope()))
			if _, err := db.ExecContext(ctx,
				`CREATE FUNCTION `+dialect.BlockMutationFn+`() RETURNS trigger LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION 'immutable'; END $fn$`); err != nil {
				t.Fatalf("create the shared guard function: %v", err)
			}
			dia, ok := dialect.NewForAppRole(store.EnginePostgres, "probe_app")
			if !ok {
				t.Fatal("bind the dialect")
			}
			for i, stmt := range dia.GuardControlPlaneStmts() {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("statement %d of the control plane DDL: %v", i+1, err)
				}
			}
			// The canonical shape must verify FIRST, or the refusal below would prove only that
			// the fixture broke something.
			observed, err := projectGuardControlPlaneShape(ctx, db)
			if err != nil {
				t.Fatalf("project the canonical shape: %v", err)
			}
			for _, want := range dialect.GuardControlPlaneShapePostgres() {
				if diff := guardShapeDifference(want, observed[want.Relation], true); diff != "" {
					t.Fatalf("the canonical DDL does not verify on major %d, so this case cannot measure a divergence: %s", m.Major, diff)
				}
			}

			for _, stmt := range []string{
				dropVocabulary,
				`ALTER TABLE ` + gate + ` ADD CONSTRAINT gate_condition_weak CHECK (gate_condition IS NOT NULL)`,
			} {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("apply the weakening (%s): %v", stmt, err)
				}
			}
			after, err := projectGuardControlPlaneShape(ctx, db)
			if err != nil {
				t.Fatalf("project the weakened shape: %v", err)
			}
			var diff string
			for _, want := range dialect.GuardControlPlaneShapePostgres() {
				if want.Relation == gate {
					diff = guardShapeDifference(want, after[want.Relation], true)
				}
			}
			if diff == "" {
				t.Fatalf("on major %d the verifier ACCEPTED a CHECK over the same column that admits every string: the fold's whole vocabulary argument rests on that predicate",
					m.Major)
			}
			for _, must := range []string{"CHECK constraint", "gate_condition"} {
				if !strings.Contains(diff, must) {
					t.Errorf("major %d refused with %q, which never says %q: a refusal that does not name the constraint or its column cannot be acted on",
						m.Major, diff, must)
				}
			}
			t.Logf("GUARD_SHAPE_CHECK_NEGATIVE_CONTROL|major=%d|refused=true|diff=%s", m.Major, diff)
			// THE MARK IS THE LAST STATEMENT, so it records work DONE rather than an iteration
			// STARTED.
			coverage.mark(m.Major)
		})
	}
}

// TestPostgresEveryDeclaredCheckIsRefusedWhenWeakenedOverItsOwnColumns is F-10's negative control
// at the size the spec actually asks for: EVERY declared CHECK, on every certified major.
//
// The first version of this control mutated ONE — the gate_condition vocabulary — and the
// correction that claimed §5.3 point 5 closed said "a mutation", singular, quietly lowering a
// condition the same file keeps a few lines later. One CHECK proves sensitivity to ONE shape: a
// string vocabulary over a single column. It says nothing about the numeric bounds, the
// octet_length equalities, the nullable disjunctions or the multi-column relational predicates
// that make up the rest of the declaration.
//
// EACH MUTATION RUNS IN ITS OWN TRANSACTION AND IS ROLLED BACK. PostgreSQL's DDL is
// transactional, so the restore is the rollback rather than a re-ADD that could itself drift, and
// the projection reads through the same Tx — the only way it can see uncommitted DDL.
//
// The weakened predicate is built from the DECLARED column list, so conkey is preserved and the
// comparison cannot pass on the column layer. That is asserted, not assumed: the observed conkey
// of the weakened constraint is read back and compared with the declared columns.
func TestPostgresEveryDeclaredCheckIsRefusedWhenWeakenedOverItsOwnColumns(t *testing.T) {
	coverage := newMajorCoverage()
	t.Cleanup(func() { coverage.assertCoveredEveryCertifiedMajor(t) })
	matrix := postgresMajorMatrix(t)
	ctx := context.Background()

	type target struct {
		relation string
		check    dialect.GuardCheckShape
	}
	var targets []target
	for _, rel := range dialect.GuardControlPlaneShapePostgres() {
		for _, c := range rel.Checks {
			targets = append(targets, target{rel.Relation, c})
		}
	}
	// A LITERAL FLOOR, because an expectation derived from the declaration shrinks with it. This
	// campaign paid for that once already, in the adversarial table next door.
	const declaredChecks = 50
	if len(targets) != declaredChecks {
		t.Fatalf("the declaration carries %d CHECK constraints and this control expects %d: a CHECK added without updating the expectation is uncovered, and one removed without updating it is silently gone",
			len(targets), declaredChecks)
	}

	for _, m := range matrix {
		t.Run(fmt.Sprintf("major %d", m.Major), func(t *testing.T) {
			db := scratchDatabaseOn(t, m, fmt.Sprintf("olv_allchk_%d_%s", m.Major, matrixScope()))
			if _, err := db.ExecContext(ctx,
				`CREATE FUNCTION `+dialect.BlockMutationFn+`() RETURNS trigger LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION 'immutable'; END $fn$`); err != nil {
				t.Fatalf("create the shared guard function: %v", err)
			}
			dia, ok := dialect.NewForAppRole(store.EnginePostgres, "probe_app")
			if !ok {
				t.Fatal("bind the dialect")
			}
			for i, stmt := range dia.GuardControlPlaneStmts() {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("statement %d of the control plane DDL: %v", i+1, err)
				}
			}
			observed, err := projectGuardControlPlaneShape(ctx, db)
			if err != nil {
				t.Fatalf("project the canonical shape: %v", err)
			}
			for _, want := range dialect.GuardControlPlaneShapePostgres() {
				if diff := guardShapeDifference(want, observed[want.Relation], true); diff != "" {
					t.Fatalf("the canonical DDL does not verify on major %d, so nothing below measures a divergence: %s", m.Major, diff)
				}
			}

			refused := 0
			for _, tg := range targets {
				weak := make([]string, 0, 4)
				for _, col := range strings.Split(tg.check.Columns, ",") {
					weak = append(weak, col+" IS NOT NULL")
				}
				weakened := "CHECK (" + strings.Join(weak, " AND ") + ")"
				// A CHECK that ALREADY is exactly this cannot be weakened this way, and passing
				// silently would be a vacuous row. None exist today; if one appears, say so.
				if strings.EqualFold(strings.Join(strings.Fields(tg.check.Definition), " "),
					strings.Join(strings.Fields(weakened), " ")) {
					t.Errorf("%s: the declared CHECK on (%s) IS the weakened form, so this row measures nothing: %s",
						tg.relation, tg.check.Columns, tg.check.Definition)
					continue
				}

				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatalf("begin: %v", err)
				}
				func() {
					defer func() { _ = tx.Rollback() }()
					var conname string
					if err := tx.QueryRowContext(ctx, `SELECT con.conname FROM pg_catalog.pg_constraint con
 WHERE con.conrelid = $1::regclass AND con.contype = 'c'
   AND pg_catalog.pg_get_constraintdef(con.oid) = $2`, tg.relation, tg.check.Definition).Scan(&conname); err != nil {
						t.Errorf("%s: locate the CHECK whose definition is %q: %v", tg.relation, tg.check.Definition, err)
						return
					}
					// #nosec G202 -- conname came from pg_constraint via quote_ident below; the
					// predicate is built from the DECLARED column list, which is a constant of
					// this binary
					if _, err := tx.ExecContext(ctx, `DO $mut$ BEGIN
  EXECUTE 'ALTER TABLE `+tg.relation+` DROP CONSTRAINT ' || quote_ident(`+quoteLiteral(conname)+`);
  EXECUTE 'ALTER TABLE `+tg.relation+` ADD CONSTRAINT ' || quote_ident(`+quoteLiteral(conname)+`) || ' `+weakened+`';
END $mut$`); err != nil {
						t.Errorf("%s/%s: weaken over the same columns: %v", tg.relation, conname, err)
						return
					}
					// SAME COLUMNS, ASSERTED. If conkey moved, a refusal below would be the
					// column layer talking and would say nothing about the predicate layers.
					var cols string
					// CROSS JOIN LATERAL, not a comma-join. A comma between pg_constraint and the
					// unnest binds the explicit JOIN to the unnest alone, so `con` leaves scope
					// and the whole read-back fails with 42P01 — measured, on all four majors.
					if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT string_agg(a.attname, ',' ORDER BY k.ord)
   FROM pg_catalog.pg_constraint con
   CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
   JOIN pg_catalog.pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
  WHERE con.conrelid = $1::regclass AND con.conname = $2), '')`, tg.relation, conname).Scan(&cols); err != nil {
						t.Errorf("%s/%s: read back the weakened conkey: %v", tg.relation, conname, err)
						return
					}
					if cols != tg.check.Columns {
						t.Errorf("%s/%s: the weakened CHECK constrains (%s) where the declared one constrains (%s), so this is not a same-column mutation",
							tg.relation, conname, cols, tg.check.Columns)
						return
					}
					after, err := projectGuardControlPlaneShape(ctx, tx)
					if err != nil {
						t.Errorf("%s: project the weakened shape: %v", tg.relation, err)
						return
					}
					var diff string
					for _, want := range dialect.GuardControlPlaneShapePostgres() {
						if want.Relation == tg.relation {
							diff = guardShapeDifference(want, after[want.Relation], true)
						}
					}
					if diff == "" {
						t.Errorf("on major %d the verifier ACCEPTED %s/%s weakened to %s over the same columns: the declared predicate was %s",
							m.Major, tg.relation, conname, weakened, tg.check.Definition)
						return
					}
					if !strings.Contains(diff, "CHECK constraint") {
						t.Errorf("%s/%s was refused with %q, which never says it is a CHECK constraint", tg.relation, conname, diff)
						return
					}
					refused++
				}()
			}
			if refused != len(targets) {
				t.Errorf("major %d refused %d of %d declared CHECKs weakened over their own columns", m.Major, refused, len(targets))
			}
			t.Logf("GUARD_SHAPE_CHECK_TOTAL_NEGATIVE_CONTROL|major=%d|checks=%d|refused=%d", m.Major, len(targets), refused)
			coverage.mark(m.Major)
		})
	}
}

// quoteLiteral renders a Go string as a PostgreSQL string literal for the DO blocks above. The
// only values that reach it are constraint names read back out of pg_constraint in this same
// transaction, and doubling the quote is what keeps that true of any name PostgreSQL allows.
func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// TestTheMatrixEnvRefusesAPassThatDeclaresAMajorWithoutTheMatrix is the pure half of the rule.
//
// It needs no server, which is the point: the three branches of classifyMatrixEnv decide whether
// three PostgreSQL tests run at all, and a decision that important should not itself be reachable
// only when PostgreSQL is reachable.
//
// MUTATION THAT MUST TURN THIS RED, and MEASURED rather than predicted: returning
// matrixEnvUncovered for the incomplete case reddens the TWO rows that declare a major without a
// matrix — "a pass declares its major but carries no matrix" and its whitespace twin — while the
// other four stay green. (An earlier version of this comment said "exactly one row"; running the
// mutation said two. The number is measured now, which is the only reason it is written down.)
func TestTheMatrixEnvRefusesAPassThatDeclaresAMajorWithoutTheMatrix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		dsns   string
		expect string
		want   matrixEnvVerdict
	}{
		{"the matrix is there and a pass declares its major", "15=a,16=b", "16", matrixEnvRun},
		{"the matrix is there and nobody declares anything", "15=a", "", matrixEnvRun},
		{"nobody claims a major and there is no matrix", "", "", matrixEnvUncovered},
		{"whitespace is not a matrix", "   ", "", matrixEnvUncovered},
		{"a pass declares its major but carries no matrix", "", "16", matrixEnvIncomplete},
		{"whitespace is not a matrix for a declared pass either", " \t ", "18", matrixEnvIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyMatrixEnv(tc.dsns, tc.expect); got != tc.want {
				t.Errorf("classifyMatrixEnv(%q, %q) = %v, want %v — a pass that declares a major and carries no matrix must REFUSE, not skip: the skip is indistinguishable from a matrix that ran",
					tc.dsns, tc.expect, got, tc.want)
			}
		})
	}
}

// TestAnEmptyMatrixIsREFUSED pins the gate that round eleven's mutant walked straight past.
//
// MUTATION THAT MUST TURN THIS RED: delete the len check in refuseAnEmptyMatrix. The nil row
// stops naming the vacuum, and every matrix test goes back to passing over zero servers.
func TestAnEmptyMatrixIsREFUSED(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		out       []majorDSN
		wantFatal int
	}{
		{"a resolved matrix passes through", []majorDSN{{Major: 15, DSN: "a"}}, 0},
		{"nothing resolved is REFUSED", nil, 1},
		{"an empty slice is REFUSED too", []majorDSN{}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var r recordingMatrixReporter
			refuseAnEmptyMatrix(&r, tc.out)
			if r.fatal != tc.wantFatal {
				t.Errorf("refuseAnEmptyMatrix(%v) produced fatal=%d, want %d — a matrix test that iterates zero servers reports success while measuring nothing",
					tc.out, r.fatal, tc.wantFatal)
			}
		})
	}
}

// recordingMatrixReporter records which action a verdict produced, instead of taking it.
type recordingMatrixReporter struct {
	fatal, skip int
	last        string
}

func (r *recordingMatrixReporter) Fatalf(format string, args ...any) {
	r.fatal++
	r.last = fmt.Sprintf(format, args...)
}

func (r *recordingMatrixReporter) Skipf(format string, args ...any) {
	r.skip++
	r.last = fmt.Sprintf(format, args...)
}

// TestTheMatrixEnvVerdictIsOBEYED closes the half the pure table could not reach.
//
// The table above proves classifyMatrixEnv returns the right verdict. This proves the harness
// ACTS on it — a different statement, and the one round nine's contrast broke by mutating
// Fatalf to Skipf and watching every test in this file stay green, matrices 15..18 included.
//
// MUTATIONS THAT MUST TURN THIS RED, and both were executed. Swapping Fatalf for Skipf inside
// applyMatrixEnvVerdict, and — the one round ten used to walk through the previous version —
// degrading the verdict at the CALL SITE, `applyMatrixEnvVerdict(r, matrixEnvUncovered, expect)`.
// The second is why this drives resolveMatrixEnv rather than the two halves separately: a mutant
// does not have to touch either half when it can lie about what passes between them.
func TestTheMatrixEnvVerdictIsOBEYED(t *testing.T) {
	t.Parallel()
	fourDSNs := "15=a,16=b,17=c,18=d"
	for _, tc := range []struct {
		name      string
		raw       string
		expect    string
		wantFatal int
		wantSkip  int
	}{
		{"a declared pass without a matrix is REFUSED", "", "16", 1, 0},
		{"an undeclared run without a matrix is skipped", "", "", 0, 1},
		{"a declared pass WITH the four DSNs is neither", fourDSNs, "16", 0, 0},
		{"an undeclared run with the four DSNs is neither", fourDSNs, "", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var r recordingMatrixReporter
			resolveMatrixEnv(&r, tc.raw, tc.expect)
			if r.fatal != tc.wantFatal || r.skip != tc.wantSkip {
				t.Errorf("resolveMatrixEnv(%q, %q) produced fatal=%d skip=%d, want fatal=%d skip=%d — a declared matrix pass that SKIPS is the false green this refusal exists to stop, and classifying it correctly buys nothing if the path does not act on it",
					tc.raw, tc.expect, r.fatal, r.skip, tc.wantFatal, tc.wantSkip)
			}
			if tc.wantFatal == 1 && !strings.Contains(r.last, envMatrixDSNs) {
				t.Errorf("the refusal does not name %s, so it cannot tell the job what to export: %q", envMatrixDSNs, r.last)
			}
		})
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
)

// THE SUITE MUST LEAVE THE CLUSTER AS IT FOUND IT, and until now nothing checked.
//
// A leaked fixture role is not untidiness. It is what produced the worst measurement error of
// this session: a test left the cluster-wide application role NOINHERIT without restoring it,
// the next runs read that role, and the CONTROL used to exonerate the change read it too — so
// a whole "product defect on PostgreSQL 15" was published, and republished, on the strength of
// an experiment and a control sharing the same dirty environment. A control that shares the
// dirty environment with the experiment is not a control.
//
// Measured by the independent contrast, on a FULLY GREEN suite: eleven roles survived across
// four servers. Two ordering defects produced them, and both are the same class:
//
//   - `defer pool.Close()` alongside a `t.Cleanup` DROP ROLE. Cleanups run LIFO AFTER the
//     function returns; a deferred close runs DURING it. The drop met a closed pool and its
//     error was discarded (guardaclwindow_pg_test.go).
//   - a DROP ROLE cleanup registered after the scratch database's, so LIFO tried the role
//     first, while its grants still existed. The dependency error was discarded
//     (guardeventfence_pg_test.go).
//
// Both are fixed at the source. THIS is the check that keeps them fixed: a snapshot taken
// before the suite and asserted after it, because "I restore it" and "I restore it in fact"
// are different claims and only the second is worth anything.
//
// IT IS DELIBERATELY SCOPED TO WHAT THIS PACKAGE CREATES. Other lanes share these servers, so
// the assertion covers the fixture name prefixes this package uses plus the posture of the
// shared application role. Asserting over EVERY role would fail on a neighbour's work and be
// switched off within a week.

// clusterStateFingerprint is what must be identical before and after.
type clusterStateFingerprint struct {
	// Roles are the roles matching this package's fixture prefixes, sorted.
	Roles []string
	// AppRoleInherits is the posture of the shared application role — the exact attribute
	// this session's own fixture left behind.
	AppRoleInherits bool
	// PublicCanReadPgRoles is the catalog grant a hardened-catalog test revokes.
	PublicCanReadPgRoles bool
	// Databases are the scratch databases this package's fixtures create. A leaked database
	// holds a leaked role's dependencies, so counting only roles reports half the leak.
	Databases []string
}

// fixturePrefixes are the names this package's fixtures create. A role that does not match
// one of these is not this package's to account for.
//
// EXTENDED after the fourth contrast, which named two families the first list missed: the
// `e_*` roles of the escalation fixtures and `olivares_app_mid`, the middle role of the
// NOINHERIT chain. A snapshot that does not cover a name cannot report its leak.
var fixturePrefixes = []string{
	"olv_evfence_", "olv_app_", "olv_to_", "olv_",
	"esc_app_", "esc_tgt_", "legacy_fn_", "e_", "olivares_app_mid",
}

func snapshotClusterState(t *testing.T, db *sql.DB) clusterStateFingerprint {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var fp clusterStateFingerprint
	rows, err := db.QueryContext(ctx, `SELECT rolname FROM pg_roles ORDER BY 1`)
	if err != nil {
		t.Fatalf("snapshot roles: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan role: %v", err)
		}
		// A pgtest-generated object belonging to ANOTHER test process is not this
		// package's to account for: `go test ./a/... ./b/...` runs packages
		// concurrently against one server, so a sibling package's live role would
		// otherwise appear inside this window and be reported as our leak.
		// Names without a process tag stay fully accounted for.
		if pgtest.ForeignFixtureObject(name) {
			continue
		}
		for _, p := range fixturePrefixes {
			if strings.HasPrefix(name, p) {
				fp.Roles = append(fp.Roles, name)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot roles: %v", err)
	}
	sort.Strings(fp.Roles)

	// The shared application role's posture. A missing row is not a failure: not every
	// cluster provisions it under that name.
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT rolinherit FROM pg_roles WHERE rolname = 'olivares_app'), true)`).
		Scan(&fp.AppRoleInherits); err != nil {
		t.Fatalf("snapshot the application role's posture: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT pg_catalog.has_table_privilege('public', 'pg_catalog.pg_roles', 'SELECT')`).
		Scan(&fp.PublicCanReadPgRoles); err != nil {
		t.Fatalf("snapshot the pg_roles grant: %v", err)
	}
	drows, err := db.QueryContext(ctx,
		`SELECT datname FROM pg_database WHERE datname LIKE 'olv\_%' OR datname LIKE 'e2e%' ORDER BY 1`)
	if err != nil {
		t.Fatalf("snapshot databases: %v", err)
	}
	defer drows.Close()
	for drows.Next() {
		var name string
		if err := drows.Scan(&name); err != nil {
			t.Fatalf("scan database: %v", err)
		}
		if pgtest.ForeignFixtureObject(name) {
			continue // another test process's scratch database (see above)
		}
		fp.Databases = append(fp.Databases, name)
	}
	if err := drows.Err(); err != nil {
		t.Fatalf("snapshot databases: %v", err)
	}
	return fp
}

// TestTheSuiteLeavesTheClusterAsItFoundIt is the assertion, and it is written as a test with
// a cleanup rather than as a TestMain on purpose: TestMain would have to own skip handling,
// the matrix environment and every other package concern, and a check nobody can read is a
// check nobody maintains.
//
// It snapshots on entry and asserts in t.Cleanup, which Go runs after this test — not after
// the package. So it catches leaks from every test that ran BEFORE it and from its own
// parallel siblings that finish first. That is a real limitation and it is stated rather than
// implied: it is a net, not a proof. What makes it worth having is that the two leaks it was
// written for are both reproducible in one run of this package.
func TestTheSuiteLeavesTheClusterAsItFoundIt(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to account for cluster-scoped state", pgtest.EnvSuperuserDSN)
	}
	db, err := sql.Open("pgx", os.Getenv(pgtest.EnvSuperuserDSN))
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	// Registered FIRST so LIFO closes it LAST — the very ordering whose absence caused the
	// leaks this test exists to catch.
	t.Cleanup(func() { _ = db.Close() })

	before := snapshotClusterState(t, db)
	t.Logf("CLUSTER_STATE|before|fixture_roles=%d|app_inherits=%v|public_reads_pg_roles=%v",
		len(before.Roles), before.AppRoleInherits, before.PublicCanReadPgRoles)

	t.Cleanup(func() {
		after := snapshotClusterState(t, db)
		if leaked := added(before.Roles, after.Roles); len(leaked) > 0 {
			t.Errorf("the suite LEAKED %d cluster-scoped role(s): %v. Roles are global to the "+
				"server, so a fixture that creates one and does not remove it changes what every "+
				"later test — and every other lane sharing this cluster — measures. That is not "+
				"untidiness: this session published a false product defect because a leaked role "+
				"contaminated both an experiment and the control used to exonerate it",
				len(leaked), leaked)
		}
		if after.AppRoleInherits != before.AppRoleInherits {
			t.Errorf("the shared application role's INHERIT posture changed from %v to %v. This is "+
				"the exact attribute a fixture in this package altered and failed to restore",
				before.AppRoleInherits, after.AppRoleInherits)
		}
		if after.PublicCanReadPgRoles != before.PublicCanReadPgRoles {
			t.Errorf("PUBLIC's SELECT on pg_roles changed from %v to %v; a catalog-hardened fixture "+
				"did not put it back, and every later boot reads a posture it did not choose",
				before.PublicCanReadPgRoles, after.PublicCanReadPgRoles)
		}
		t.Logf("CLUSTER_STATE|after|fixture_roles=%d|app_inherits=%v|public_reads_pg_roles=%v",
			len(after.Roles), after.AppRoleInherits, after.PublicCanReadPgRoles)
	})
}

// added returns the names present in after and absent from before. Both are sorted.
func added(before, after []string) []string {
	had := make(map[string]bool, len(before))
	for _, b := range before {
		had[b] = true
	}
	var out []string
	for _, a := range after {
		if !had[a] {
			out = append(out, a)
		}
	}
	return out
}

// TestMain IS THE ONLY REAL END-OF-SUITE HOOK, and the test above was not one.
//
// The fourth contrast put the limitation precisely: a leak that happens BEFORE that test starts
// is absorbed into its own initial snapshot, and one that happens AFTER occurs when its cleanup
// has already run. Measured there — B-1 left `olivares_app` NOINHERIT again with the guard test
// itself green. A check whose window is a subset of the suite cannot account for the suite.
//
// This runs before the first test and after the last one, which is the whole point. It does not
// t.Fatal — it has no *testing.T — so it reports on stderr and turns a leak into a NON-ZERO EXIT
// even when every test passed. That is the only shape in which "the suite left the cluster as it
// found it" is a gate rather than a hope.
//
// It stays silent when no superuser DSN is configured: this package runs on SQLite alone in that
// case and there is no cluster to account for.
func TestMain(m *testing.M) {
	before, ok := rawClusterSnapshot()
	code := m.Run()
	if !ok {
		os.Exit(code)
	}
	after, ok := rawClusterSnapshot()
	if !ok {
		fmt.Fprintln(os.Stderr, "CLUSTER_STATE|could not re-read the cluster after the suite; a leak "+
			"cannot be ruled out and this is reported rather than assumed clean")
		os.Exit(code)
	}
	var problems []string
	if leaked := added(before.Roles, after.Roles); len(leaked) > 0 {
		problems = append(problems, fmt.Sprintf("LEAKED %d cluster-scoped role(s): %v", len(leaked), leaked))
	}
	if leaked := added(before.Databases, after.Databases); len(leaked) > 0 {
		problems = append(problems, fmt.Sprintf("LEAKED %d scratch database(s): %v", len(leaked), leaked))
	}
	if before.AppRoleInherits != after.AppRoleInherits {
		problems = append(problems, fmt.Sprintf("the shared application role's INHERIT posture changed %v -> %v",
			before.AppRoleInherits, after.AppRoleInherits))
	}
	if before.PublicCanReadPgRoles != after.PublicCanReadPgRoles {
		problems = append(problems, fmt.Sprintf("PUBLIC's SELECT on pg_roles changed %v -> %v",
			before.PublicCanReadPgRoles, after.PublicCanReadPgRoles))
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "\nCLUSTER_STATE|THE SUITE DID NOT LEAVE THE CLUSTER AS IT FOUND IT\n")
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		fmt.Fprintln(os.Stderr, "  Cluster-scoped state is shared by every later test and every other lane "+
			"on this server. This session published a false product defect because a leaked role "+
			"contaminated an experiment AND the control used to exonerate it.")
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// rawClusterSnapshot is snapshotClusterState without a *testing.T, for TestMain. It reports
// false when there is no server to look at, and never kills the run for that.
func rawClusterSnapshot() (clusterStateFingerprint, bool) {
	dsn := strings.TrimSpace(os.Getenv(pgtest.EnvSuperuserDSN))
	if dsn == "" {
		return clusterStateFingerprint{}, false
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return clusterStateFingerprint{}, false
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var fp clusterStateFingerprint
	rows, err := db.QueryContext(ctx, `SELECT rolname FROM pg_roles ORDER BY 1`)
	if err != nil {
		return clusterStateFingerprint{}, false
	}
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			_ = rows.Close()
			return clusterStateFingerprint{}, false
		}
		for _, p := range fixturePrefixes {
			if strings.HasPrefix(name, p) {
				fp.Roles = append(fp.Roles, name)
				break
			}
		}
	}
	_ = rows.Close()
	drows, err := db.QueryContext(ctx,
		`SELECT datname FROM pg_database WHERE datname LIKE 'olv\_%' OR datname LIKE 'e2e%' ORDER BY 1`)
	if err != nil {
		return clusterStateFingerprint{}, false
	}
	for drows.Next() {
		var name string
		if drows.Scan(&name) != nil {
			_ = drows.Close()
			return clusterStateFingerprint{}, false
		}
		fp.Databases = append(fp.Databases, name)
	}
	_ = drows.Close()
	if db.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT rolinherit FROM pg_roles WHERE rolname = 'olivares_app'), true)`).
		Scan(&fp.AppRoleInherits) != nil {
		return clusterStateFingerprint{}, false
	}
	if db.QueryRowContext(ctx,
		`SELECT pg_catalog.has_table_privilege('public', 'pg_catalog.pg_roles', 'SELECT')`).
		Scan(&fp.PublicCanReadPgRoles) != nil {
		return clusterStateFingerprint{}, false
	}
	sort.Strings(fp.Roles)
	sort.Strings(fp.Databases)
	return fp, true
}

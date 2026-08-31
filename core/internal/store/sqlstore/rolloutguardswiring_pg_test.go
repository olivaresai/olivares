// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// THE TWO GAPS THE CONTRAST FOUND IN THIS SESSION'S OWN COVERAGE, closed where they are
// observable: on a server.
//
// M-4. The regressions for the classification receipt open SQLite ONLY. Measured by the
// contrast: omitting the PostgreSQL guard while keeping the SQLite pair left both of them
// green. An engine-specific omission is invisible to a test that never runs that engine, and
// the PostgreSQL half is the one carrying ENABLE ALWAYS and the ACL revoke — the two pieces
// SQLite has no equivalent for at all.
//
// B-1. guardrolefact_test.go builds guardRoles by hand and exercises the classifier and its
// consumer. That pins the DECISION and not the WIRING: measured by the contrast, deleting
// `guardRolesForBoot.OwnerConfigured = true` from the boot path left all five of its tests
// green. Finding an observable for it took two attempts — see the second test below, whose
// first shape was itself measured useless.
func TestPostgresTheRolloutEvidenceRelationsCarryTheirGuard(t *testing.T) {
	t.Parallel()
	dsns := isolatedPG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidgetStaged)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = st.Close()

	raw, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("raw pool: %v", err)
	}
	defer func() { _ = raw.Close() }()

	// control_rollout_state is deliberately ABSENT: it takes UPDATEs in production, so
	// guarding it would refuse the very transition it exists to record. Asserting that
	// absence is as load-bearing as asserting the others' presence — a change that guarded
	// everything uniformly would brick SetRolloutMode.
	for _, tc := range []struct {
		table  string
		guard  bool
		reason string
	}{
		{dialect.ControlRolloutTransitionTable, true, "the append-only history of deliberate decisions"},
		{dialect.ControlRolloutClassificationTable, true, "the durable receipt a lost state row is detected against"},
		{dialect.ControlAppendOnlyScopeTable, true, "the inventory of what must stay guarded"},
		{dialect.ControlRolloutStateTable, false, "mutable by design: a transition rewrites the current mode"},
	} {
		t.Run(tc.table, func(t *testing.T) {
			var enabled string
			err := raw.QueryRowContext(ctx,
				`SELECT t.tgenabled FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
				 JOIN pg_proc p ON p.oid = t.tgfoid
				 WHERE c.relname = $1 AND NOT t.tgisinternal AND p.proname = $2`,
				tc.table, dialect.BlockMutationFn).Scan(&enabled)
			switch {
			case !tc.guard:
				if err == nil {
					t.Fatalf("%s carries an immutability guard (tgenabled=%q) and must not: %s. "+
						"Guarding it would refuse the UPDATE that records a transition", tc.table, enabled, tc.reason)
				}
				return
			case err != nil:
				t.Fatalf("%s has NO immutability guard on PostgreSQL (%s). The SQLite regressions cannot "+
					"see this: the engines emit different statements and only this one carries ENABLE "+
					"ALWAYS and the revoke: %v", tc.table, tc.reason, err)
			}
			// ENABLE ALWAYS, not the ORIGIN default. At 'O' the trigger does not fire for a
			// logical-replication apply, so the guard is absent on exactly the path an operator
			// cannot watch — the repository's own words.
			if enabled != "A" {
				t.Fatalf("%s's guard is at tgenabled=%q, not 'A' (ALWAYS). At 'O' a logical-replication "+
					"apply mutates evidence in silence", tc.table, enabled)
			}
			// And the ACL half, which is the only defense against TRUNCATE — no row trigger can
			// observe it.
			for _, priv := range []string{"UPDATE", "DELETE", "TRUNCATE"} {
				var held bool
				if err := raw.QueryRowContext(ctx,
					"SELECT pg_catalog.has_table_privilege($1, $2)", tc.table, priv).Scan(&held); err != nil {
					t.Fatalf("probe %s: %v", priv, err)
				}
				if held {
					t.Errorf("the application role still holds %s on %s; the trigger cannot see TRUNCATE, "+
						"so the revoke is the only ACL-layer defense", priv, tc.table)
				}
			}
		})
	}
}

// TestPostgresASplitOwnerDeploymentRunsTheEscalationClosure is B-1, and the FIRST shape of it
// was useless — reported rather than replaced silently.
//
// That version asserted the application role holds no write privilege on the three
// control-plane relations. Measured by mutation: it stays green with the OwnerConfigured
// wiring removed, because the v6 creation DDL already emits pgRevokeAllWritesUnlessOwner in
// the transaction that creates those relations. The privileges are absent whatever the boot
// concluded about the topology, so the assertion could not see the difference it claimed to.
//
// What ONLY happens when the boot resolves the topology as SPLIT is the escalation closure:
// two distinct role names say what the operator configured, and whether the application role
// can ASSUME the other one is a question only the server can answer. Under a resolved split
// a membership that lets app become owner is FATAL — the boundary would be verified and
// absent at the same time. Under "single-role" that check never runs at all.
//
// So the fixture grants exactly that membership and the property is the refusal.
func TestPostgresASplitOwnerDeploymentRunsTheEscalationClosure(t *testing.T) {
	t.Parallel()
	dsns := isolatedPGSplit(t)
	ctx := context.Background()

	appRole := roleOf(t, ctx, dsns.App)
	ownerRole := roleOf(t, ctx, dsns.Owner)
	if appRole == ownerRole {
		t.Fatalf("the split fixture handed the same role twice (%q); there is no split to resolve", appRole)
	}

	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4}
	// A clean boot first, so the control plane exists and the SECOND boot fails for the
	// membership and nothing else.
	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("baseline split open: %v", err)
	}
	_ = st.Close()

	super, err := sql.Open("pgx", dsns.Superuser)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	// t.Cleanup, NOT defer: cleanups run LIFO AFTER the function returns, while a deferred
	// Close runs DURING the return — so the restore registered below found `sql: database is
	// closed` and the role stayed contaminated anyway. Registering the close FIRST makes it
	// run LAST.
	t.Cleanup(func() { _ = super.Close() })
	// The application role is cluster-global. Keep the provisioning lock for the whole
	// mutation-and-restore window so parallel isolatedPG fixtures cannot ALTER the same
	// pg_authid tuple while this test has it in the deliberate NOINHERIT posture.
	releaseSharedRole := pgtest.LockSharedRole(t)
	t.Cleanup(releaseSharedRole)
	// NOINHERIT FIRST, and it is the whole reason this fixture discriminates. A plain
	// membership also hands app the owner's privileges automatically, and then the ORDINARY
	// append-only verification refuses the boot through has_table_privilege — measured: the
	// test stayed green with the wiring removed, catching the deployment by a different
	// defense than the one it claims to pin. NOINHERIT is the exact shape the escalation
	// closure was written against and which its own doc records measuring:
	//
	//	has_table_privilege('ledger','INSERT') = false   <- the ordinary verifier passes here
	//	pg_has_role('app','owner','MEMBER')     = true
	//	SET ROLE owner; INSERT INTO ledger ...  = INSERT 0 1
	//
	// With it, the ONLY check that can refuse this boot is the closure, and the closure only
	// runs when the topology resolved to SPLIT.
	//
	// AND IT IS RESTORED, which the first version of this fixture did not do. isolatedPGSplit
	// deliberately reuses the CLUSTER-WIDE application role and does not tear it down, so this
	// ALTER leaked NOINHERIT into every other test and every later run against the same
	// server. It was not theoretical: the contrast measured all four servers left at
	// rolinherit=false, and — worse — a mutation elsewhere in this session only reddened
	// BECAUSE of that residue. A parallel test that mutates shared cluster state and does not
	// put it back turns other tests' verdicts into functions of execution order.
	if _, err := super.ExecContext(ctx, `ALTER ROLE `+pgQuoteIdent(appRole)+` NOINHERIT`); err != nil {
		t.Fatalf("make the membership non-inheriting: %v", err)
	}
	t.Cleanup(func() {
		if _, err := super.ExecContext(context.Background(),
			`ALTER ROLE `+pgQuoteIdent(appRole)+` INHERIT`); err != nil {
			t.Errorf("FAILED TO RESTORE %s to INHERIT: this role is shared by the whole cluster and "+
				"every later test now runs against a posture this one left behind: %v", appRole, err)
		}
	})
	// The membership the closure exists to catch: app can SET ROLE to owner, so revoking
	// privileges from app buys nothing — it changes role first and writes as the owner.
	if _, err := super.ExecContext(ctx,
		`GRANT `+pgQuoteIdent(ownerRole)+` TO `+pgQuoteIdent(appRole)); err != nil {
		t.Fatalf("grant the membership: %v", err)
	}
	t.Cleanup(func() {
		if _, err := super.ExecContext(context.Background(),
			`REVOKE `+pgQuoteIdent(ownerRole)+` FROM `+pgQuoteIdent(appRole)); err != nil {
			t.Errorf("FAILED TO REVOKE %s FROM %s: this membership is cluster-global and "+
				"every later test can otherwise assume the owner role: %v", ownerRole, appRole, err)
		}
	})
	// The precondition, asserted: the ordinary verifier must NOT be able to see this.
	app, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	defer func() { _ = app.Close() }()
	var inherited bool
	if err := app.QueryRowContext(ctx,
		"SELECT pg_catalog.has_table_privilege($1, 'INSERT')", dialect.GuardControlPlaneTables()[0]).
		Scan(&inherited); err != nil {
		t.Fatalf("probe the inherited privilege: %v", err)
	}
	if inherited {
		t.Fatalf("the membership is still INHERITING, so the ordinary append-only verification would " +
			"refuse this boot and the escalation closure would not be the thing under test")
	}

	_, err = Open(ctx, cfg, registerWidget)
	if err == nil {
		t.Fatalf("boot was GREEN with %q a member of %q on a deployment that configures a SEPARATE "+
			"owner role. The escalation closure only runs when the topology resolves to SPLIT, so a "+
			"green boot here means this boot called itself single-role — which is exactly what losing "+
			"the OwnerConfigured wiring produces, and what no unit test over a hand-built guardRoles "+
			"can observe", appRole, ownerRole)
	}
	t.Logf("ESCALATION_CLOSURE|refused=%v", err)
}

// roleOf reports the role a DSN authenticates as.
func roleOf(t *testing.T, ctx context.Context, dsn string) string {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open %q: %v", dsn, err)
	}
	defer func() { _ = db.Close() }()
	var role string
	if err := db.QueryRowContext(ctx, "SELECT current_user").Scan(&role); err != nil {
		t.Fatalf("read current_user: %v", err)
	}
	return role
}

// pgQuoteIdent quotes an identifier for a statement this test renders itself. The roles come
// from the fixture, not from user input, but a bare name would still break on anything the
// provisioner might legitimately choose.
func pgQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

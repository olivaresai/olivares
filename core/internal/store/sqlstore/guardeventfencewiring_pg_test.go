// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardeventfencewiring_pg_test.go proves the event-fence check is CALLED BY PRODUCTION, not
// merely callable.
//
// The distinction is the one this whole campaign is about. A helper with its own unit tests is
// a library; what makes it a control is a caller on the boot path, and the only proof of that
// is a mutation that removes the caller and turns a test red. Every assertion below drives the
// real `Open`, never runGuardEventFenceCheck directly.
//
// MUTATIONS THAT MUST TURN THIS RED:
//
//  1. Delete the `runGuardEventFenceCheck` call from store.go's pre-serve window. Red in
//     `TestPostgresADivergentEventFenceRefusesTheBoot`: Open hands out a usable store over a
//     database whose fence was installed and then neutralized.
//  2. Make the `required` policy accept an absent fence. Red in
//     `TestPostgresARequiredEventFenceRefusesABootWithoutIt`.
//  3. Make GuardEventFencePolicy.Valid accept anything. Red in
//     `TestPostgresAnUnknownEventFencePolicyIsRefusedRatherThanResolved` — a typo that read as
//     "verify" would be a required fence quietly downgraded.

// installEventFenceThroughSuperuser applies this build's own fence DDL to an isolated database
// using the maintenance role, which is the only role that can: CREATE EVENT TRIGGER is
// superuser-only and every role this product uses is NOSUPERUSER.
func installEventFenceThroughSuperuser(t *testing.T, superuserDSN string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", superuserDSN)
	if err != nil {
		t.Fatalf("open the maintenance pool: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, s := range dialect.GuardEventFenceStmts() {
		if _, err := db.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("apply the fence DDL: %v\nstatement: %s", err, s)
		}
	}
	var legs int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pg_catalog.pg_event_trigger WHERE evtname IN ($1, $2)`,
		dialect.GuardEventFenceDropTrigger, dialect.GuardEventFenceEndTrigger).Scan(&legs); err != nil {
		t.Fatalf("count the fence's legs: %v", err)
	}
	if legs != 2 {
		t.Fatalf("the fence fixture installed %d leg(s) and this test needs 2: everything below would be measuring a database that is not the one it describes", legs)
	}
	return db
}

// TestPostgresADivergentEventFenceRefusesTheBoot is the wiring proof.
//
// The fence is installed BEFORE the first boot and neutralized afterwards, which is the shape
// the check exists for: absent is not divergent, and only a database that HAD the fence can
// have lost it. The neutralization is the measured one — a rewritten handler, after which
// pg_event_trigger reads exactly as it did while the fence refuses nothing.
func TestPostgresADivergentEventFenceRefusesTheBoot(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	super := installEventFenceThroughSuperuser(t, dsns.Superuser)

	// A boot over an INSTALLED fence must succeed: without this leg the test could pass
	// because the fence broke the boot for some unrelated reason.
	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("a boot over an installed, canonical fence must succeed: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close the first store: %v", cerr)
	}

	if _, err := super.ExecContext(ctx,
		`CREATE OR REPLACE FUNCTION `+dialect.EngineSchema+`.`+dialect.GuardEventFenceHandlerFn+
			`() RETURNS event_trigger LANGUAGE plpgsql AS $x$ BEGIN END $x$`); err != nil {
		t.Fatalf("neutralize the fence: %v", err)
	}

	st2, err := Open(ctx, cfg, registerWidget)
	if st2 != nil {
		_ = st2.Close()
	}
	if err == nil {
		t.Fatal("a boot over a fence that was installed and then rewritten into a no-op handed out a store; nothing now refuses the DROP TRIGGER this fence exists to refuse, and the catalog rows still say it is there")
	}
	if !strings.Contains(err.Error(), "installed and then changed") {
		t.Errorf("the refusal is not the fence's, so this test may be passing for another reason: %v", err)
	}
	t.Logf("GUARD_EVENT_FENCE_WIRED|installed_boot=ok|divergent_boot=refused")
}

// TestPostgresARequiredEventFenceRefusesABootWithoutIt pins the one thing the policy adds.
//
// Under the default posture an absent fence is loud and not fatal — no deployment can have
// installed it before this edition existed. Under `required` it is a refusal, because a
// control an operator declared and that is not there is not a control.
func TestPostgresARequiredEventFenceRefusesABootWithoutIt(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	// Default posture: absent, and the boot proceeds.
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("the default posture must not turn an absent fence into an outage: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close the default-posture store: %v", cerr)
	}

	// Required posture, same database: refused, and the message says how to fix it.
	st2, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4,
		GuardEventFence: store.GuardEventFenceRequired,
	}, registerWidget)
	if st2 != nil {
		_ = st2.Close()
	}
	if err == nil {
		t.Fatal("a deployment that declared the fence required booted without it")
	}
	if !strings.Contains(err.Error(), "required by configuration and is not installed") {
		t.Errorf("the refusal is not the required-posture one: %v", err)
	}

	// And with the fence applied, the required posture boots.
	installEventFenceThroughSuperuser(t, dsns.Superuser)
	st3, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4,
		GuardEventFence: store.GuardEventFenceRequired,
	}, registerWidget)
	if err != nil {
		t.Fatalf("the required posture must boot once the fence is there: %v", err)
	}
	if cerr := st3.Close(); cerr != nil {
		t.Fatalf("close the required-posture store: %v", cerr)
	}
	t.Logf("GUARD_EVENT_FENCE_POLICY|absent_default=ok|absent_required=refused|installed_required=ok")
}

// TestPostgresAnUnknownEventFencePolicyIsRefusedRatherThanResolved: an unrecognized value must
// not fall back to the default. A typo that read as "verify" would be a required fence
// silently downgraded, which is the failure mode a default is supposed to prevent, inverted.
func TestPostgresAnUnknownEventFencePolicyIsRefusedRatherThanResolved(t *testing.T) {
	dsns := isolatedPG(t)
	st, err := Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4,
		GuardEventFence: store.GuardEventFencePolicy("requried"), // the plausible typo
	}, registerWidget)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("a misspelled policy booted; whatever the operator meant, it was not silently 'verify'")
	}
	if !strings.Contains(err.Error(), "is not a guard fence policy this build understands") {
		t.Errorf("the refusal does not name the policy vocabulary: %v", err)
	}
}

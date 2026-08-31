// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package eventing

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Unit H, commit 3 — the fence ENFORCED, on real PostgreSQL, in the real deployment shape.
//
// SQLite cannot prove any of this. The properties that matter here are properties of an engine that
// has row-level security, a deliberately unprivileged application role, and transaction semantics a
// pure-Go single-writer file does not model:
//
//   - a NOBYPASSRLS application role, so one tenant's proof is genuinely invisible to another's
//     mutation rather than merely unused;
//   - a separate OWNER for DDL, so "the app role cannot disable the trigger" is a measured
//     privilege result and not an assumption;
//   - real concurrency, so the arming race has a boundary that can be observed instead of reasoned
//     about. The measurements that preceded this unit proved sequential statements; that was the gap
//     an adversarial review of the design named, and this file is where it is closed.
//
// The database and roles are provisioned per test from the superuser DSN, so nothing here touches a
// shared database. It SKIPS when no superuser DSN is configured, and says so — a fence whose only
// verification is skipped silently would be worse than no verification.
//
// MUTATION-TESTED BY HAND, 2026-07-30 — read this as a RECORD OF A RUN, not as a standing property.
// Each guarantee below was verified to fail for ITS OWN reason by breaking the mechanism it rests on,
// watching the named test go red, and restoring it. There is no mutation runner in this repository,
// so nothing re-checks the table: an edit that breaks one of these pairings will not turn anything
// red. The pairings are the evidence that the tests measure their mechanism; the DATE is the limit of
// that evidence.
//
//	mutation of the migration                            test that went red
//	---------------------------------------------------  -----------------------------------------
//	drop FOR SHARE on the control row                     TestAnInFlightWriteHoldsTheArming
//	stop comparing the observed generation                TestAStaleGenerationIsRefused
//	check the proof without CONSUMING it                  TestAProofIsSpentOnce
//	fence every update, not only a moved destination      TestANonMovingUpdateNeedsNoProofEvenWhenArmed
//	remove the live-sink DELETE trigger                   TestDeletingALiveSinkProfileIsRefusedButCleanupIsNot
//	drop `tenant_id = NEW.tenant_id` from the proof lookup  TestABypassRLSConnectionStillCannotBorrowAnotherTenantsProof
//
// That matters more than the count of passing tests: a test that stays green when its mechanism is
// removed is measuring nothing, and this campaign has already found three of those.
//
// The last row was run BOTH ways, and the asymmetry is the honest labeling rather than a gap:
// with the tenant predicate removed, TestOneTenantsProofCannotAuthorizeAnothersMutation STAYS GREEN
// (measured) because row-level security is still stopping it there, while the BYPASSRLS test goes
// red. Two independent defenses, each measured by the test that isolates it — and neither claimed to
// be doing the other's work.

// pgFence is an isolated PostgreSQL database with the owner/app role split the deployment
// documentation recommends.
type pgFence struct {
	App   string
	Owner string
	Admin string
	// AdminRole is the BYPASSRLS role's name, so a test can grant it write privileges on purpose
	// and measure what the fence does when row-level security is NOT the thing stopping a borrowed
	// proof.
	AdminRole string
	name      string
}

// newPGFence provisions an isolated database and the three roles. It is deliberately explicit
// rather than reusing the engine's own test helper: that helper lives under core/internal, which a
// module cannot import, and pretending otherwise would have meant testing the fence on SQLite only.
func newPGFence(t *testing.T) pgFence {
	t.Helper()
	super := strings.TrimSpace(os.Getenv("OLIVARES_TEST_POSTGRES_SUPERUSER_DSN"))
	if super == "" {
		t.Skip("OLIVARES_TEST_POSTGRES_SUPERUSER_DSN is not set: the writer fence's PostgreSQL enforcement is NOT verified in this run")
	}
	db, err := sql.Open("pgx", super)
	if err != nil {
		t.Fatalf("open superuser: %v", err)
	}
	defer db.Close()

	suffix := strings.ReplaceAll(strings.ToLower(model.NewID().String()), "-", "")
	if len(suffix) > 16 {
		suffix = suffix[:16]
	}
	name := "olv_s517_" + suffix
	appRole, ownerRole, adminRole := name+"_app", name+"_own", name+"_adm"

	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("provision %q: %v", q, err)
		}
	}
	// Exactly the documented posture: owner owns the schema and runs DDL; app is NOSUPERUSER
	// NOBYPASSRLS; admin is the BYPASSRLS cross-tenant reader.
	exec(fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'p' NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB", ownerRole))
	exec(fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'p' NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB", appRole))
	exec(fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'p' NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB", adminRole))
	exec(fmt.Sprintf("CREATE DATABASE %s OWNER %s", name, ownerRole))

	host := hostOf(super)
	f := pgFence{
		name:      name,
		AdminRole: adminRole,
		Owner:     fmt.Sprintf("postgres://%s:p@%s/%s?sslmode=disable", ownerRole, host, name),
		App:       fmt.Sprintf("postgres://%s:p@%s/%s?sslmode=disable", appRole, host, name),
		Admin:     fmt.Sprintf("postgres://%s:p@%s/%s?sslmode=disable", adminRole, host, name),
	}
	// The owner grants the app and admin roles what the deployment grants them: USAGE plus DML, and
	// deliberately NOT schema CREATE — which is exactly the privilege whose absence broke unit G's
	// ceremony until it learned to take an owner DSN.
	ownerDB, err := sql.Open("pgx", f.Owner)
	if err != nil {
		t.Fatalf("open owner: %v", err)
	}
	defer ownerDB.Close()
	for _, q := range []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s, %s", appRole, adminRole),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", ownerRole, appRole),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT ON TABLES TO %s", ownerRole, adminRole),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %s", ownerRole, appRole),
	} {
		if _, err := ownerDB.Exec(q); err != nil {
			t.Fatalf("grant %q: %v", q, err)
		}
	}
	t.Cleanup(func() {
		cdb, err := sql.Open("pgx", super)
		if err != nil {
			return
		}
		defer cdb.Close()
		_, _ = cdb.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
		for _, r := range []string{appRole, adminRole, ownerRole} {
			_, _ = cdb.Exec("DROP ROLE IF EXISTS " + r)
		}
	})
	return f
}

// hostOf pulls host:port out of a DSN without a URL parse, which would fail on the credential forms
// this repository's env DSNs use.
func hostOf(dsn string) string {
	s := dsn
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	return s
}

// openPreFence opens the split-role database as a binary from the era BEFORE the fence: the module's
// tables, the destination control, and neither the fence's control nor its migrations.
func openPreFence(t *testing.T, f pgFence) store.Store {
	t.Helper()
	m := New()
	st, err := engine.Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: f.App, OwnerDSN: f.Owner, AdminDSN: f.Admin, MaxConns: 6,
	}, func(reg store.ExtensionRegistry) error {
		return m.RegisterSchema(preFenceRegistry{reg})
	})
	if err != nil {
		t.Fatalf("open postgres as a pre-fence binary: %v", err)
	}
	return st
}

// openFenced opens the module's real schema against the split-role database.
func openFenced(t *testing.T, f pgFence) store.Store {
	t.Helper()
	m := New()
	st, err := engine.Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: f.App, OwnerDSN: f.Owner, AdminDSN: f.Admin, MaxConns: 6,
	}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open postgres with the module schema: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func provisionFenceTenant(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(context.Background(), model.Org{Slug: slug, Name: slug})
		if err != nil {
			return err
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	return tenant
}

// armFence makes the deployment demand a capability, the way the operator's ceremony will.
func armFence(t *testing.T, st store.Store) int64 {
	t.Helper()
	rs := st.(store.RolloutStater)
	cur, err := rs.RolloutState(context.Background(), EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read fence state: %v", err)
	}
	if cur.CurrentMode == store.RolloutEnforced {
		return cur.Generation
	}
	next, err := rs.SetRolloutMode(context.Background(), store.RolloutTransition{
		Key: EgressWriterFenceControlKey, Mode: store.RolloutEnforced,
		Actor: "test", Reason: "arm the fence", ExpectGeneration: cur.Generation,
	})
	if err != nil {
		t.Fatalf("arm the fence: %v", err)
	}
	return next.Generation
}

// writeSubscription writes a subscription row the way a binary of the given era would: `attest`
// true is a binary that carries the gate, false is one that does not.
func writeSubscription(ctx context.Context, st store.Store, tenant model.TenantID, name, endpoint string, attest bool, generation int64) error {
	return st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colSubName: name, colSubEnabled: true, colSubTypes: "finding.reported",
			colSubEndpoint: endpoint, colSubSecret: "sealed:x", colSubSecretHint: "x",
			colSubRole: "viewer", colSubOwnerActor: "t", colSubOwnerActorK: "user",
			colSubAuthType: authTypeNone,
		}
		if attest {
			if err := StampWriterProof(ctx, sc, rec, generation); err != nil {
				return err
			}
		}
		_, err = repo.Create(ctx, rec)
		return err
	})
}

// TestTheFenceRefusesAWriterThatDoesNotProveItsCapability is the property the whole unit exists for,
// measured through the engine's own write path on the real role split.
func TestTheFenceRefusesAWriterThatDoesNotProveItsCapability(t *testing.T) {
	f := newPGFence(t)
	st := openFenced(t, f)
	tenant := provisionFenceTenant(t, st, "acme")
	ctx := context.Background()

	// A FRESH database arms the fence by classification: nothing that predates the fence ever wrote
	// here, so there is no rolling update to protect and no reason to leave it open.
	rs := st.(store.RolloutStater)
	cur, err0 := rs.RolloutState(ctx, EgressWriterFenceControlKey)
	if err0 != nil {
		t.Fatalf("read fence state: %v", err0)
	}
	if cur.CurrentMode != store.RolloutEnforced {
		t.Fatalf("a fresh database classified the fence %q, want %q", cur.CurrentMode, store.RolloutEnforced)
	}
	gen := cur.Generation

	// An OLD binary: no proof.
	err := writeSubscription(ctx, st, tenant, "old-writer", "https://b.example.com/h", false, 0)
	if err == nil {
		t.Fatal("an armed fence accepted a write with no capability attestation")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("the refusal is not diagnosable: %v", err)
	}
	// The error must NAME the fence and the remedy. An old binary cannot translate it, which is why
	// the raw text has to carry the meaning.
	if !strings.Contains(err.Error(), "capability") {
		t.Fatalf("the refusal does not say what is missing: %v", err)
	}

	// A NEW binary: proof in the same transaction.
	if err := writeSubscription(ctx, st, tenant, "new-writer", "https://c.example.com/h", true, gen); err != nil {
		t.Fatalf("a writer carrying the gate was refused: %v", err)
	}
}

// TestAStaleGenerationIsRefused. Without this, the proof would mean only "code able to write an
// attestation ran". With it, it means the writer read the CURRENT disposition — a node holding a
// cached read attests an old generation, is refused, and retries.
func TestAStaleGenerationIsRefused(t *testing.T) {
	f := newPGFence(t)
	st := openFenced(t, f)
	tenant := provisionFenceTenant(t, st, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	err := writeSubscription(ctx, st, tenant, "stale", "https://d.example.com/h", true, gen-1)
	if err == nil {
		t.Fatal("a proof made against an older fence generation was accepted: a node with a stale cached read would author under a disposition that has since moved")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestOneTenantsProofCannotAuthorizeAnothersMutation. Row-level security is what makes this true,
// and only a real engine with a NOBYPASSRLS role can demonstrate it.
func TestOneTenantsProofCannotAuthorizeAnothersMutation(t *testing.T) {
	f := newPGFence(t)
	st := openFenced(t, f)
	alpha := provisionFenceTenant(t, st, "alpha")
	bravo := provisionFenceTenant(t, st, "bravo")
	ctx := context.Background()
	gen := armFence(t, st)

	// Alpha writes a proof and does NOT spend it (the mutation fails on a bad column), leaving a
	// live attestation in alpha's scope.
	var alphaNonce string
	if err := st.Mutate(ctx, alpha, func(sc store.Scope) error {
		n, err := WriterProof{Capability: EgressWriterCapability, Generation: gen}.Stamp(ctx, sc)
		alphaNonce = n
		return err
	}); err != nil {
		t.Fatalf("alpha stamps a proof: %v", err)
	}
	if alphaNonce == "" {
		t.Fatal("no nonce")
	}

	// Bravo tries to use alpha's nonce.
	err := st.Mutate(ctx, bravo, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			colSubName: "borrowed", colSubEnabled: true, colSubTypes: "finding.reported",
			colSubEndpoint: "https://e.example.com/h", colSubSecret: "sealed:x", colSubSecretHint: "x",
			colSubRole: "viewer", colSubOwnerActor: "t", colSubOwnerActorK: "user",
			colSubAuthType: authTypeNone, colWriterNonce: alphaNonce,
		})
		return err
	})
	if err == nil {
		t.Fatal("one tenant's capability proof authorized another tenant's mutation")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestABypassRLSConnectionStillCannotBorrowAnotherTenantsProof measures the tenant predicate the
// fence function carries, INDEPENDENTLY of row-level security.
//
// TestOneTenantsProofCannotAuthorizeAnothersMutation above is defended twice over — by RLS and by
// the predicate — so it cannot tell which one did the work, and a mutation of the predicate leaves
// it green. This one removes RLS from the picture entirely: it writes through the BYPASSRLS role,
// deliberately granted the write privilege it does not have in the documented posture. That models
// the misconfiguration the deployment docs warn against, and the point is that the fence must not
// DEPEND on the warning being obeyed. It is also the property SQLite has to rely on outright, having
// no row-level security at all.
func TestABypassRLSConnectionStillCannotBorrowAnotherTenantsProof(t *testing.T) {
	f := newPGFence(t)
	st := openFenced(t, f)
	alpha := provisionFenceTenant(t, st, "alpha")
	bravo := provisionFenceTenant(t, st, "bravo")
	ctx := context.Background()
	gen := armFence(t, st)

	var alphaNonce string
	if err := st.Mutate(ctx, alpha, func(sc store.Scope) error {
		n, err := WriterProof{Capability: EgressWriterCapability, Generation: gen}.Stamp(ctx, sc)
		alphaNonce = n
		return err
	}); err != nil {
		t.Fatalf("alpha stamps a proof: %v", err)
	}

	owner, err := sql.Open("pgx", f.Owner)
	if err != nil {
		t.Fatalf("open owner: %v", err)
	}
	defer owner.Close()
	for _, q := range []string{
		"GRANT INSERT, SELECT ON eventing_subscription TO " + f.AdminRole,
		"GRANT SELECT, DELETE ON eventing_writer_attest TO " + f.AdminRole,
		// The trigger function is SECURITY INVOKER, so it reads the control row as whoever is
		// writing — and it reads it FOR SHARE, which PostgreSQL charges UPDATE privilege for, not
		// SELECT. Measured the hard way: with SELECT alone this write failed with SQLSTATE 42501
		// (permission denied for table control_rollout_state) while has_table_privilege reported
		// SELECT = true.
		//
		// That is a real coupling worth stating rather than discovering: any role that writes a
		// governed table must hold SELECT AND UPDATE on control_rollout_state. The documented
		// posture already gives the application role both (the owner grants it SELECT, INSERT,
		// UPDATE, DELETE); a narrower hand-built grant would fail closed, which is the right
		// direction but an opaque error.
		"GRANT SELECT, UPDATE ON control_rollout_state TO " + f.AdminRole,
	} {
		if _, err := owner.Exec(q); err != nil {
			t.Fatalf("grant %q: %v", q, err)
		}
	}
	admin, err := sql.Open("pgx", f.Admin)
	if err != nil {
		t.Fatalf("open the BYPASSRLS role: %v", err)
	}
	defer admin.Close()
	// It can SEE alpha's proof — that is what BYPASSRLS means, and it is the premise of the test.
	var visible int
	if err := admin.QueryRow("SELECT COUNT(*) FROM eventing_writer_attest WHERE nonce = $1", alphaNonce).Scan(&visible); err != nil {
		t.Fatalf("read attestations as the BYPASSRLS role: %v", err)
	}
	if visible != 1 {
		t.Fatalf("the BYPASSRLS role sees %d rows for alpha's nonce, want 1: without visibility this test proves nothing", visible)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = admin.Exec(`INSERT INTO eventing_subscription
		(id, tenant_id, created_at, updated_at, version, name, enabled, event_types, endpoint,
		 secret_sealed, secret_hint, role, owner_actor, owner_actor_kind, writer_nonce)
		VALUES ($1, $2, $3, $3, 1, 'borrowed', true, 'finding.reported', 'https://n.example.com/h',
		 'sealed:x', 'x', 'viewer', 't', 'user', $4)`,
		model.NewID().String(), bravo.String(), now, alphaNonce)
	if err == nil {
		t.Fatal("a BYPASSRLS connection used one tenant's capability proof to authorize another tenant's mutation: the isolation of a proof must be a property of the rule, not of how the roles were configured")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestAProofIsSpentOnce. A proof that survives its mutation is a proof a second writer can use.
func TestAProofIsSpentOnce(t *testing.T) {
	f := newPGFence(t)
	st := openFenced(t, f)
	tenant := provisionFenceTenant(t, st, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	var nonce string
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		n, err := WriterProof{Capability: EgressWriterCapability, Generation: gen}.Stamp(ctx, sc)
		nonce = n
		return err
	}); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	write := func(name string) error {
		return st.Mutate(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(subscriptionKind)
			if err != nil {
				return err
			}
			_, err = repo.Create(ctx, model.Record{
				colSubName: name, colSubEnabled: true, colSubTypes: "finding.reported",
				colSubEndpoint: "https://f.example.com/h", colSubSecret: "sealed:x", colSubSecretHint: "x",
				colSubRole: "viewer", colSubOwnerActor: "t", colSubOwnerActorK: "user",
				colSubAuthType: authTypeNone, colWriterNonce: nonce,
			})
			return err
		})
	}
	if err := write("first"); err != nil {
		t.Fatalf("the first use of a live proof was refused: %v", err)
	}
	if err := write("second"); err == nil {
		t.Fatal("the same proof authorized a SECOND mutation: an orphaned proof would be reusable forever")
	}
}

// TestAnOldBinarysUpdatePreservingEveryColumnIsRefused is the hole the design contrast named
// explicitly: a persistent column alone is not enough, because an old binary preserves the stored
// value while changing the endpoint. It fails because the proof that created that value was spent.
func TestAnOldBinarysUpdatePreservingEveryColumnIsRefused(t *testing.T) {
	f := newPGFence(t)
	st := openFenced(t, f)
	tenant := provisionFenceTenant(t, st, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	if err := writeSubscription(ctx, st, tenant, "sub", "https://g.example.com/h", true, gen); err != nil {
		t.Fatalf("seed a governed row: %v", err)
	}
	// An old binary re-points it, carrying every column it read — including the spent nonce.
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		rec := rows[0]
		rec[colSubEndpoint] = "https://evil.example.com/h"
		_, err = repo.Update(ctx, rec)
		return err
	})
	if err == nil {
		t.Fatal("an update that re-pointed the destination while preserving the stored nonce was accepted")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestANonMovingUpdateNeedsNoProofEvenWhenArmed pins the narrow scope as a PROPERTY rather than a
// comment. Unit G preserves that a pre-existing subscription stays editable — including to disable
// it, which is what an operator does in an incident — so an armed fence must not take that away.
func TestANonMovingUpdateNeedsNoProofEvenWhenArmed(t *testing.T) {
	f := newPGFence(t)
	st := openFenced(t, f)
	tenant := provisionFenceTenant(t, st, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	if err := writeSubscription(ctx, st, tenant, "sub", "https://h.example.com/h", true, gen); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// An OLD binary disables it: every column preserved, the endpoint unchanged.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		rec := rows[0]
		rec[colSubEnabled] = false
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("an armed fence blocked DISABLING a subscription from a writer that carries no proof: %v — that is the action an operator takes in an incident, and unit G preserves it on purpose", err)
	}
}

// TestReactivatingADormantDestinationIsGovernedOnPostgres mirrors the SQLite property on the engine
// that ships: disabling is free, reactivating carries a proof.
func TestReactivatingADormantDestinationIsGovernedOnPostgres(t *testing.T) {
	f := newPGFence(t)
	st := openFenced(t, f)
	tenant := provisionFenceTenant(t, st, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	if err := writeSubscription(ctx, st, tenant, "sub", "https://s2.example.com/h", true, gen); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// An OLD binary disables it: free.
	flip := func(to bool) error {
		return st.Mutate(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(subscriptionKind)
			if err != nil {
				return err
			}
			rows, _, err := repo.List(ctx, model.Query{Limit: 1})
			if err != nil {
				return err
			}
			rec := rows[0]
			rec[colSubEnabled] = to
			_, err = repo.Update(ctx, rec)
			return err
		})
	}
	if err := flip(false); err != nil {
		t.Fatalf("an armed fence blocked DISABLING a subscription: %v — the fence must never block turning egress off", err)
	}
	if err := flip(true); err == nil {
		t.Fatal("an old binary reactivated a dormant destination with no proof: reactivation resumes delivery to it")
	} else if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestTheApplicationRoleCannotDisableOrDropTheFence. A fence the writer can turn off is not a fence.
// Measured, because it is a privilege result and not a design intention.
func TestTheApplicationRoleCannotDisableOrDropTheFence(t *testing.T) {
	f := newPGFence(t)
	_ = openFenced(t, f)

	app, err := sql.Open("pgx", f.App)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	defer app.Close()
	// The SQLSTATE is asserted, not merely the presence of an error. An earlier version accepted
	// ANY failure, and `DROP FUNCTION` fails with 2BP01 (dependent objects) for a role that DOES own
	// the function — so the strongest of the three statements could have been refused for a reason
	// that says nothing about privilege, and this test would still have been green. 42501 is
	// insufficient_privilege: the only answer that means what the test claims.
	for _, q := range []string{
		"ALTER TABLE eventing_subscription DISABLE TRIGGER eventing_subscription_writer_fence_ins",
		"DROP TRIGGER eventing_subscription_writer_fence_ins ON eventing_subscription",
		"DROP FUNCTION olivares_eventing_writer_fence()",
	} {
		_, err := app.Exec(q)
		if err == nil {
			t.Fatalf("the application role was allowed to %q — in the split-role deployment it must not own the table", q)
		}
		if got := pgSQLState(err); got != "42501" {
			t.Fatalf("%q was refused with SQLSTATE %q, want 42501 (insufficient_privilege): a refusal for any other reason does not show the application role LACKS the privilege — it may simply have hit a dependency. err=%v", q, got, err)
		}
	}
}

// pgSQLState extracts the five-character SQLSTATE from a driver error, or "" when the error carries
// none. Written against the message because the stdlib driver hands back a wrapped error and this
// package deliberately depends on database/sql rather than on pgx's typed errors.
func pgSQLState(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	const marker = "SQLSTATE "
	i := strings.Index(msg, marker)
	if i < 0 || len(msg) < i+len(marker)+5 {
		return ""
	}
	return msg[i+len(marker) : i+len(marker)+5]
}

// TestAnInFlightWriteHoldsTheArming is the concurrent boundary. Without the shared lock on the
// control row, a writer whose transaction started while the fence was dormant can commit AFTER the
// arming commits — its trigger read the old requirement, so the arming returns while a pre-arm write
// is still in flight. The measurements that preceded this unit proved sequential statements only;
// this is the gap the design contrast named.
func TestAnInFlightWriteHoldsTheArming(t *testing.T) {
	f := newPGFence(t)
	ctx := context.Background()

	// The race only exists on an UPGRADE: a fresh database is armed by classification, so there is
	// no pre-arm state to be in flight during. Era 1 is a binary that predates the fence; era 2
	// installs it DORMANT, which is the window the arming has to close safely.
	st1 := openPreFence(t, f)
	tenant := provisionFenceTenant(t, st1, "acme")
	_ = st1.Close()
	st := openFenced(t, f)
	if got, err := st.(store.RolloutStater).RolloutState(ctx, EgressWriterFenceControlKey); err != nil {
		t.Fatalf("read fence state: %v", err)
	} else if got.CurrentMode != store.RolloutLegacyCompat {
		t.Fatalf("precondition: the fence is %q, want %q", got.CurrentMode, store.RolloutLegacyCompat)
	}

	// A writer opens a transaction while the fence is DORMANT and holds it.
	release := make(chan struct{})
	inFlight := make(chan struct{})   // the writer's governed statement has run, so FOR SHARE is held
	armStarted := make(chan struct{}) // the armer goroutine has begun its attempt
	writeDone := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		writeDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(subscriptionKind)
			if err != nil {
				return err
			}
			if _, err := repo.Create(ctx, model.Record{
				colSubName: "in-flight", colSubEnabled: true, colSubTypes: "finding.reported",
				colSubEndpoint: "https://i.example.com/h", colSubSecret: "sealed:x", colSubSecretHint: "x",
				colSubRole: "viewer", colSubOwnerActor: "t", colSubOwnerActorK: "user",
				colSubAuthType: authTypeNone,
			}); err != nil {
				return err
			}
			// The trigger has taken its shared lock on the control row by now. Hold the
			// transaction open so the arming has to wait for it.
			close(inFlight)
			<-release
			return nil
		})
	}()

	// SIGNALS, NOT SLEEPS. This slept 400ms hoping the writer had reached the lock and then read
	// "still blocked" off a 1200ms timeout — so on a loaded runner it could have passed with the
	// writer not yet at its statement, or with the arming goroutine never scheduled at all, which
	// measures the Go runtime instead of the row lock. The writer now announces that its statement
	// has run (so the trigger has taken FOR SHARE), the armer announces that it has begun, and the
	// timeout is left to measure only what cannot be signaled: that a started attempt does not
	// finish. Unlike SQLite, PostgreSQL lets the armer READ while the write is in flight — it blocks
	// on the row lock at the transition — so the signal can sit before the read on both engines and
	// mean the same thing.
	armed := make(chan error, 1)
	go func() {
		<-inFlight
		close(armStarted)
		rs := st.(store.RolloutStater)
		cur, err := rs.RolloutState(ctx, EgressWriterFenceControlKey)
		if err != nil {
			armed <- err
			return
		}
		_, err = rs.SetRolloutMode(ctx, store.RolloutTransition{
			Key: EgressWriterFenceControlKey, Mode: store.RolloutEnforced,
			Actor: "test", Reason: "arm while a write is in flight", ExpectGeneration: cur.Generation,
		})
		armed <- err
	}()

	// The arming must NOT complete while the writer holds its lock.
	select {
	case <-armStarted:
	case <-time.After(10 * time.Second):
		close(release)
		wg.Wait()
		t.Fatal("the arming goroutine never began its attempt: the case below would have read a scheduling gap as a lock being held")
	}

	select {
	case err := <-armed:
		t.Fatalf("the arming completed while a pre-arm write was still in flight (err=%v): after it returns, no un-proved write may still land", err)
	case <-time.After(1200 * time.Millisecond):
		// Still blocked, which is the property.
	}
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("the in-flight pre-arm write failed: %v", err)
	}
	wg.Wait()
	if err := <-armed; err != nil {
		t.Fatalf("the arming failed once the writer finished: %v", err)
	}
	// And from here, a writer with no proof is refused.
	if err := writeSubscription(ctx, st, tenant, "after-arming", "https://j.example.com/h", false, 0); err == nil {
		t.Fatal("a write with no proof was accepted after the arming returned")
	}
}

// TestDeletingALiveSinkProfileIsRefusedButCleanupIsNot covers the DELETE half — the one mutation
// that re-pointed a live destination past any INSERT/UPDATE fence.
func TestDeletingALiveSinkProfileIsRefusedButCleanupIsNot(t *testing.T) {
	f := newPGFence(t)
	st := openFenced(t, f)
	tenant := provisionFenceTenant(t, st, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	var subID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colSubName: "sub", colSubEnabled: true, colSubTypes: "finding.reported",
			colSubEndpoint: "https://k.example.com/h", colSubSecret: "sealed:x", colSubSecretHint: "x",
			colSubRole: "viewer", colSubOwnerActor: "t", colSubOwnerActorK: "user",
			colSubAuthType: authTypeNone,
		}
		if err := StampWriterProof(ctx, sc, rec, gen); err != nil {
			return err
		}
		created, err := repo.Create(ctx, rec)
		if err != nil {
			return err
		}
		subID = model.ID(created.String(model.ColID))
		sinks, err := sc.Ext(subscriptionSinkKind)
		if err != nil {
			return err
		}
		srec := model.Record{
			colSinkSubRef: subID.String(), colSinkKind: "splunk_hec",
			colSinkFormat: "", colSinkCred: "sealed:t", colSinkOpts: "", colSinkHint: "t",
		}
		if err := StampWriterProof(ctx, sc, srec, gen); err != nil {
			return err
		}
		_, err = sinks.Create(ctx, srec)
		return err
	}); err != nil {
		t.Fatalf("seed a subscription with a sink profile: %v", err)
	}

	// Deleting the profile while the subscription LIVES re-points the destination: refused.
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		return m0(t).deleteSinkRowWithSubscription(ctx, sc, subID)
	})
	if err == nil {
		t.Fatal("deleting the sink profile of a LIVE subscription was accepted: it moves the destination to the base endpoint, silently")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Deleting the subscription and then its profile is cleanup, not a re-point: allowed.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		if err := repo.Delete(ctx, subID); err != nil {
			return err
		}
		return m0(t).deleteSinkRowWithSubscription(ctx, sc, subID)
	}); err != nil {
		t.Fatalf("deleting a subscription and its profile together was refused: %v — that is cleanup, and there is no destination left to move", err)
	}
}

// m0 is a bare module used only to reach the two sink helpers; it needs no seams because those
// helpers take the Scope they write through.
func m0(t *testing.T) *Module {
	t.Helper()
	return New()
}

// TestInstallingTheFenceOnAnUPGRADEChangesNothingUntilArmed is the operational promise, and the one
// that decides whether this unit is safe to ship: on a deployment whose fleet predates the fence,
// the migration lands and every existing writer keeps working. If this were false, every estate
// would break the moment the first new pod took the migration lock — during a rolling update that is
// supposed to be invisible.
func TestInstallingTheFenceOnAnUPGRADEChangesNothingUntilArmed(t *testing.T) {
	f := newPGFence(t)
	ctx := context.Background()

	// Era 1: a binary with neither the fence's control nor its migrations.
	st1 := openPreFence(t, f)
	tenant := provisionFenceTenant(t, st1, "acme")
	if err := writeSubscription(ctx, st1, tenant, "era1", "https://p1.example.com/h", false, 0); err != nil {
		t.Fatalf("era 1 write: %v", err)
	}
	_ = st1.Close()

	// Era 2: the fence arrives. The witness table exists, so it is classified DORMANT — and an
	// un-upgraded writer must still be able to author.
	st2 := openFenced(t, f)
	got, err := st2.(store.RolloutStater).RolloutState(ctx, EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read fence state: %v", err)
	}
	if got.CurrentMode != store.RolloutLegacyCompat {
		t.Fatalf("an upgraded deployment classified the fence %q, want %q", got.CurrentMode, store.RolloutLegacyCompat)
	}
	if err := writeSubscription(ctx, st2, tenant, "era2-unproved", "https://p2.example.com/h", false, 0); err != nil {
		t.Fatalf("a writer with no proof was refused on a DORMANT fence: %v — installing the fence would break every rolling update", err)
	}
	// And a writer that DOES carry the gate works too, so the two eras coexist.
	if err := writeSubscription(ctx, st2, tenant, "era2-proved", "https://p3.example.com/h", true, got.Generation); err != nil {
		t.Fatalf("a writer carrying the gate was refused on a dormant fence: %v", err)
	}
	// Only the deliberate arming closes it.
	gen := armFence(t, st2)
	if err := writeSubscription(ctx, st2, tenant, "after-arm", "https://p4.example.com/h", false, 0); err == nil {
		t.Fatal("after arming, a writer with no proof was still accepted")
	}
	if err := writeSubscription(ctx, st2, tenant, "after-arm-ok", "https://p5.example.com/h", true, gen); err != nil {
		t.Fatalf("after arming, a writer carrying the gate was refused: %v", err)
	}
}

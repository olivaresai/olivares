// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

// The attempt lifecycle, first internal cut — durability and failure, on a real SQLite FILE database
// and, when a server is configured, on a real isolated PostgreSQL. An absent
// Postgres server SKIPS loudly: not run is not a pass.
//
// What each case here is careful NOT to claim:
//
//   - No exclusion is claimed, so no barrier expects two tenant callbacks to be
//     open at once. The current store serializes a tenant's Mutate, so such a
//     barrier would deadlock rather than measure anything; the lock ORDER inside
//     one transaction is asserted in attempt_upgrade_test.go instead.
//   - The three failure shapes are staged apart on purpose, because they are
//     different facts: a failure BEFORE the commit (nothing persists), a REAL
//     unique-index conflict from the engine (refused, never absorbed as a
//     successful replay), and an acknowledgement LOST AFTER an actual commit
//     (write_outcome_unknown, resolved by identity, never by importing again).

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// eachAttemptBackend runs fn against a real SQLite FILE database (so a case can
// close and reopen it) and, when a server is configured, a private PostgreSQL
// database of its own.
func eachAttemptBackend(t *testing.T, fn func(t *testing.T, cfg store.Config)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		fn(t, store.Config{
			Engine: store.EngineSQLite,
			DSN:    filepath.Join(t.TempDir(), "finops-attempt.db"),
			Debug:  true,
		})
	})
	t.Run("postgres", func(t *testing.T) {
		if !enginetest.PostgresAvailable(t) {
			t.Skipf("%s unset: the PostgreSQL leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
		}
		fn(t, store.Config{
			Engine:   store.EnginePostgres,
			DSN:      enginetest.IsolatedPostgres(t).App,
			MaxConns: 4,
		})
	})
}

// registerHistoricalFinOpsSchema declares the reservation ledger EXACTLY as it was
// before this cut: no lifecycle linkage columns, and no attempt or lifecycle-scope
// tables at all. A database opened with it is a genuine pre-lifecycle database, not a
// current one with some rows removed.
func registerHistoricalFinOpsSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  budgetReservationKind,
		Table: budgetReservationTable,
		Fields: []model.FieldSpec{
			{Name: colResvPolicyRef, Kind: model.KindUUID, Indexed: true},
			{Name: colResvPolicyKind, Kind: model.KindText},
			{Name: colResvDimension, Kind: model.KindText, Nullable: true},
			{Name: colResvScopeKey, Kind: model.KindText},
			{Name: colResvPeriod, Kind: model.KindText},
			{Name: colResvPeriodStart, Kind: model.KindTimestamp, Indexed: true},
			{Name: colResvSeq, Kind: model.KindInt},
			{Name: colResvAmount, Kind: model.KindInt},
			{Name: colResvActual, Kind: model.KindInt},
			{Name: colResvState, Kind: model.KindText, Indexed: true},
			{Name: colResvHandle, Kind: model.KindUUID, Indexed: true},
			{Name: colResvExpiresAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colResvSettledAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "finops_budget_reservation_seq_uniq",
			Columns: []string{model.ColTenantID, colResvPolicyRef, colResvPeriodStart, colResvScopeKey, colResvSeq},
			Unique:  true,
		}},
	})
}

// openAttemptStore opens a store with the given registrar and closes it when the
// case ends. It deliberately does NOT use t.Cleanup for the close in the reopen
// cases: those close explicitly, because reopening a database still held open is
// not the upgrade being measured.
func openAttemptStore(t testing.TB, cfg store.Config, register func(store.ExtensionRegistry) error) store.Store {
	t.Helper()
	st, err := engine.Open(context.Background(), cfg, register)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

// finModuleOn wires a module with the lab verifier to an already-open store.
func finModuleOn(t testing.TB, st store.Store, tenant model.TenantID) (*Module, *labVerifier) {
	t.Helper()
	v := newLabVerifier(tenant)
	m := New(WithAttemptEvidenceVerifier(v))
	m.host = &fakeHost{}
	m.clock = &fakeClock{t: baseTime}
	m.UseData(finopsTestData{ModuleData: api.NewModuleData(st), st: st})
	return m, v
}

// TestAHistoricalDatabaseIsExpandedAndReopenedRepeatedly is the migration causal.
//
// A pre-lifecycle database is created and POPULATED through the old descriptor; it is
// then opened with the current one (which adds two nullable columns and creates
// two tables), and reopened twice more. Across all of it: the original rows keep
// their ids, keep NULL in both linkage columns, and are still read as legacy; a
// frontier and an import survive; and the second and third opens change nothing.
func TestAHistoricalDatabaseIsExpandedAndReopenedRepeatedly(t *testing.T) {
	eachAttemptBackend(t, func(t *testing.T, cfg store.Config) {
		ctx := context.Background()

		// --- the historical database -----------------------------------------
		old := openAttemptStore(t, cfg, registerHistoricalFinOpsSchema)
		slug := "hist-" + uniqueSlugSuffix(t)
		var tenant model.TenantID
		if err := old.System(ctx, func(sys store.SystemScope) error {
			if _, e := sys.EnsureSystemTenant(ctx); e != nil {
				return e
			}
			org, e := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
			tenant = org.TenantID
			return e
		}); err != nil {
			t.Fatalf("provision tenant: %v", err)
		}
		policy := model.NewID()
		handle := model.NewID()
		seeded := seedGroup(t, old, tenant,
			legacyChild(policy, handle, "", "monthly", 1, 5*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive),
			legacyChild(policy, handle, "seat-a", "monthly", 2, 2*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateExpired))
		// A THIRD row that shares (policy, dim_key, period_start) with the first and
		// differs only in seq — the ordinary shape of two concurrent reservers on one
		// budget. It is the migration hazard of this cut: the new UNIQUE index is
		// (tenant, attempt_ref, policy_ref, dim_key, period_start), and it is only safe
		// on a populated table because BOTH engines treat NULLs as distinct in a unique
		// index. If they did not, creating that index over a real ledger would fail —
		// or, worse, the expansion would succeed on one engine and not the other.
		seeded = append(seeded, seedGroup(t, old, tenant,
			legacyChild(policy, model.NewID(), "", "monthly", 3, 1*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))...)
		originalIDs := []string{
			seeded[0].String(model.ColID), seeded[1].String(model.ColID), seeded[2].String(model.ColID),
		}
		if err := old.Close(); err != nil {
			t.Fatalf("close the historical store: %v", err)
		}

		// --- the expansion ----------------------------------------------------
		m0 := New()
		st := openAttemptStore(t, cfg, m0.RegisterSchema)
		rows := countReservations(t, st, tenant)
		if len(rows) != 3 {
			t.Fatalf("%d rows survived the expansion, want 3 — including the pair that shares every column of the new unique index but seq", len(rows))
		}
		for _, r := range rows {
			if !r.IsNull(colResvAttemptRef) || !r.IsNull(colResvLifecycleVersion) {
				t.Fatalf("row %s acquired a lifecycle linkage from the migration", r.String(model.ColID))
			}
			if link, _ := linkageOf(r); link != linkageLegacy {
				t.Fatalf("row %s is no longer read as legacy after the expansion", r.String(model.ColID))
			}
		}
		if got := idsOf(rows); !sameSet(got, originalIDs) {
			t.Fatalf("row ids changed across the expansion: %v -> %v", originalIDs, got)
		}

		m, _ := finModuleOn(t, st, tenant)
		view, err := m.BeginLifecycleActivation(ctx, tenant, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence")},
		})
		if err != nil {
			t.Fatalf("Begin on the expanded database: %v", err)
		}
		imported, err := m.ImportLegacyHold(ctx, tenant, importFor(t, handle, currentGroupRows(t, st, tenant, handle)))
		if err != nil {
			t.Fatalf("import on the expanded database: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close the expanded store: %v", err)
		}

		// --- reopen, twice ----------------------------------------------------
		for round := 1; round <= 2; round++ {
			m2 := New()
			st2 := openAttemptStore(t, cfg, m2.RegisterSchema)
			mod, _ := finModuleOn(t, st2, tenant)

			scope, found, serr := readScopeThrough(t, st2, tenant)
			if serr != nil || !found {
				t.Fatalf("reopen %d: the frontier did not survive (found=%v err=%v)", round, found, serr)
			}
			if scope.FrontierDigest != view.FrontierDigest || scope.State != lifecycleQuiescing {
				t.Fatalf("reopen %d: the frontier changed: %+v", round, scope)
			}
			got, gerr := mod.GetAttempt(ctx, tenant, imported.AttemptRef)
			if gerr != nil {
				t.Fatalf("reopen %d: the import did not survive: %v", round, gerr)
			}
			if got.Phase != phaseOutcomeUnknown || got.AccountingAt != nil ||
				got.AccountingBasis.Kind != basisLegacyUnknown {
				t.Fatalf("reopen %d: the imported attempt came back changed: %+v", round, got)
			}
			if len(got.LegacyImport.OriginalChildren) != 2 {
				t.Fatalf("reopen %d: the immutable original lost rows", round)
			}
			// The NULL columns of the later phases are still NULL: the expansion
			// declared them, and nothing in this cut fills them.
			row := attemptRows(t, st2, tenant)[0]
			for _, col := range []string{
				colAttemptEffectDigest, colAttemptDispatchRef, colAttemptAccountingAt,
				colAttemptDispatchMarked, colAttemptSettledAt, colAttemptOutcome,
				colAttemptOutcomeDigest, colAttemptSettlementDgst, colAttemptSampleKey,
				colAttemptSampleID, colAttemptCostRecordID, colAttemptResolutionEvid,
			} {
				if !row.IsNull(col) {
					t.Fatalf("reopen %d: %s is not NULL on an imported attempt", round, col)
				}
			}
			// And an exact replay of the original request still resolves.
			replay, rerr := mod.ImportLegacyHold(ctx, tenant, imported.LegacyImport.OriginalRequest)
			if rerr != nil {
				t.Fatalf("reopen %d: replay: %v", round, rerr)
			}
			if replay.AttemptRef != imported.AttemptRef {
				t.Fatalf("reopen %d: the replay produced another identity", round)
			}
			if n := len(attemptRows(t, st2, tenant)); n != 1 {
				t.Fatalf("reopen %d: %d attempt rows", round, n)
			}
			if err := st2.Close(); err != nil {
				t.Fatalf("reopen %d: close: %v", round, err)
			}
		}
	})
}

func readScopeThrough(t testing.TB, st store.Store, tenant model.TenantID) (LifecycleScopeView, bool, error) {
	t.Helper()
	var (
		view  LifecycleScopeView
		found bool
		out   error
	)
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		view, found, out = readLifecycleScope(context.Background(), sc)
		return nil
	}); err != nil {
		return LifecycleScopeView{}, false, err
	}
	return view, found, out
}

func idsOf(rows []model.Record) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.String(model.ColID))
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// TestAFailureBeforeTheCommitLeavesTheWholeGroupUntouched: the injected failure
// lands after the FIRST child was already updated inside the transaction. Nothing
// may survive it — not the parent, not the first child, not the audit — and the
// same import repeated afterwards must simply work.
func TestAFailureBeforeTheCommitLeavesTheWholeGroupUntouched(t *testing.T) {
	eachAttemptBackend(t, func(t *testing.T, cfg store.Config) {
		ctx := context.Background()
		m, st, tenant, _ := openLifecycleFinCfg(t, cfg)
		policy := model.NewID()
		handle := model.NewID()
		rows := seedGroup(t, st, tenant,
			legacyChild(policy, handle, "", "monthly", 1, 5*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive),
			legacyChild(policy, handle, "seat-a", "monthly", 2, 2*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
		req := importFor(t, handle, rows)
		auditBefore := len(auditActions(t, st, tenant))

		real := m.data
		forceUpdateFailureAfter(m, 1) // the SECOND child's update fails
		_, err := m.ImportLegacyHold(ctx, tenant, req)
		if err == nil {
			t.Fatalf("the injected failure was not reported")
		}
		var ae *AttemptError
		if errors.As(err, &ae) && ae.MayHaveCommitted {
			t.Fatalf("a failure the callback itself returned was reported as possibly committed: %+v", ae)
		}
		m.UseData(real)

		if n := len(attemptRows(t, st, tenant)); n != 0 {
			t.Fatalf("%d attempt parents survived a rolled-back import", n)
		}
		for _, r := range countReservations(t, st, tenant) {
			if link, _ := linkageOf(r); link != linkageLegacy {
				t.Fatalf("child %s kept a linkage from a rolled-back import", r.String(model.ColID))
			}
		}
		if got := len(auditActions(t, st, tenant)); got != auditBefore {
			t.Fatalf("a rolled-back import left %d audit events behind", got-auditBefore)
		}

		// Explicitly repeated, it works — and produces ONE parent.
		view, err := m.ImportLegacyHold(ctx, tenant, importFor(t, handle, countReservations(t, st, tenant)))
		if err != nil {
			t.Fatalf("the repeated import: %v", err)
		}
		if view.Phase != phaseOutcomeUnknown {
			t.Fatalf("phase = %q", view.Phase)
		}
		if n := len(attemptRows(t, st, tenant)); n != 1 {
			t.Fatalf("%d attempt parents after the repeat", n)
		}
	})
}

// TestARealUniqueViolationIsRefusedAndNeverAbsorbed: a parent already owns this
// handle under a DIFFERENT identity, so the insert hits the (tenant, handle)
// unique index. On PostgreSQL that aborts the transaction, which is exactly why it
// must not be reported as a successful de-duplication.
func TestARealUniqueViolationIsRefusedAndNeverAbsorbed(t *testing.T) {
	eachAttemptBackend(t, func(t *testing.T, cfg store.Config) {
		ctx := context.Background()
		m, st, tenant, _ := openLifecycleFinCfg(t, cfg)
		policy := model.NewID()
		handle := model.NewID()
		rows := seedGroup(t, st, tenant,
			legacyChild(policy, handle, "", "monthly", 1, 5*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))

		// A squatter parent: same handle, another identity. Only a direct write can
		// produce it, which is the point — it is the shape a collision would take.
		squatter := legacyImportRef(tenant, model.NewID())
		seedAttemptRow(t, st, tenant, squatter, handle)

		_, err := m.ImportLegacyHold(ctx, tenant, importFor(t, handle, rows))
		if err == nil {
			t.Fatalf("the unique violation was absorbed as a successful import")
		}
		if code := attemptCode(err); code != errCodeStaleAttempt {
			t.Fatalf("code = %q, want stale_attempt (err=%v)", code, err)
		}
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("the store conflict lost its identity: errors.Is(store.ErrConflict) is false")
		}
		if n := len(attemptRows(t, st, tenant)); n != 1 {
			t.Fatalf("%d attempt rows, want only the squatter", n)
		}
		for _, r := range countReservations(t, st, tenant) {
			if link, _ := linkageOf(r); link != linkageLegacy {
				t.Fatalf("a child was linked by an import that failed")
			}
		}
	})
}

// seedAttemptRow writes a minimal valid parent row directly.
func seedAttemptRow(t testing.TB, st store.Store, tenant model.TenantID, ref AttemptRef, handle model.ID) {
	t.Helper()
	binding := legacyUnboundBinding(ref)
	bindingBody, err := marshalBounded(encodeBinding(binding), maxAttemptPayloadBytes, "binding")
	if err != nil {
		t.Fatalf("encode binding: %v", err)
	}
	targetsBody, err := marshalBounded(encodeTargets(nil), maxAttemptPayloadBytes, "targets")
	if err != nil {
		t.Fatalf("encode targets: %v", err)
	}
	basisBody, err := marshalBounded(encodeAccountingBasis(AccountingBasis{Kind: basisLegacyUnknown}), maxAttemptPayloadBytes, "basis")
	if err != nil {
		t.Fatalf("encode basis: %v", err)
	}
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, rerr := sc.Ext(attemptKind)
		if rerr != nil {
			return rerr
		}
		_, rerr = repo.Create(context.Background(), model.Record{
			colAttemptContractVersion: attemptContractVersion,
			colAttemptRef:             string(ref),
			colAttemptRequestRef:      string(ref),
			colAttemptHandle:          handle.String(),
			colAttemptPhase:           string(phaseOutcomeUnknown),
			colAttemptBindingDigest:   string(bindingDigestOf(tenant, ref, binding)),
			colAttemptBinding:         bindingBody,
			colAttemptTargets:         targetsBody,
			colAttemptResvDigest:      string(bindingDigestOf(tenant, ref, binding)),
			colAttemptReviewAfter:     model.NewTimestamp(baseTime).String(),
			colAttemptOwnerRef:        "squatter",
			colAttemptOwnerEpoch:      int64(1),
			colAttemptPublication:     publicationNone,
			colAttemptAccountingBasis: basisBody,
		})
		return rerr
	}); err != nil {
		t.Fatalf("seed attempt row: %v", err)
	}
}

// lostAckData commits for real and then loses the acknowledgement, which is the
// one shape in which the caller genuinely cannot know. It is NOT a rollback and
// must never be reported as one.
type lostAckData struct {
	api.ModuleData
	err  error
	once bool
}

func (d *lostAckData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if err := d.ModuleData.Mutate(ctx, tenant, fn); err != nil {
		return err
	}
	if !d.once {
		d.once = true
		return d.err
	}
	return nil
}

// TestALostAcknowledgementAfterAnActualCommitIsResolvedByIdentity: the transaction
// COMMITTED and the acknowledgement did not arrive. The caller is told
// write_outcome_unknown with MayHaveCommitted, and the resolution is a lookup by
// the deterministic identity plus an exact replay of the original request — never a
// second import, and never a second parent.
func TestALostAcknowledgementAfterAnActualCommitIsResolvedByIdentity(t *testing.T) {
	eachAttemptBackend(t, func(t *testing.T, cfg store.Config) {
		ctx := context.Background()
		m, st, tenant, _ := openLifecycleFinCfg(t, cfg)
		policy := model.NewID()
		handle := model.NewID()
		rows := seedGroup(t, st, tenant,
			legacyChild(policy, handle, "", "monthly", 1, 5*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
		req := importFor(t, handle, rows)

		real := m.data
		m.UseData(&lostAckData{ModuleData: real, err: fmt.Errorf("finops-test: the commit acknowledgement was lost")})
		_, err := m.ImportLegacyHold(ctx, tenant, req)
		m.UseData(real)

		var ae *AttemptError
		if !errors.As(err, &ae) {
			t.Fatalf("err = %v, want a typed attempt error", err)
		}
		if ae.Code != errCodeWriteOutcomeUnknown || !ae.MayHaveCommitted {
			t.Fatalf("code=%q may_have_committed=%v, want write_outcome_unknown and true", ae.Code, ae.MayHaveCommitted)
		}
		if ae.Retry != retryReadByIdentity {
			t.Fatalf("retry = %q, want read_by_identity", ae.Retry)
		}
		if ae.AttemptRef == nil || *ae.AttemptRef != legacyImportRef(tenant, handle) {
			t.Fatalf("the uncertain refusal did not name the identity to read")
		}

		// THE EVIDENCE IS THERE: the write did commit.
		got, gerr := m.GetAttempt(ctx, tenant, *ae.AttemptRef)
		if gerr != nil {
			t.Fatalf("reading the uncertain import by identity: %v", gerr)
		}
		if got.Handle != handle || got.Phase != phaseOutcomeUnknown {
			t.Fatalf("the durable state is not the import: %+v", got)
		}
		// And the original request resolves it, without duplicating anything.
		replay, rerr := m.ImportLegacyHold(ctx, tenant, req)
		if rerr != nil {
			t.Fatalf("resolving by the original request: %v", rerr)
		}
		if replay.AttemptRef != got.AttemptRef || replay.Version != got.Version {
			t.Fatalf("the resolution produced a different attempt: %+v vs %+v", replay, got)
		}
		if n := len(attemptRows(t, st, tenant)); n != 1 {
			t.Fatalf("%d attempt rows after an uncertain import and its resolution", n)
		}
		if n := countAuditAction(t, st, tenant, auditActionImport); n != 1 {
			t.Fatalf("%d import audit events, want the one the committed transaction wrote", n)
		}
	})
}

// TestTheRequiredCapabilitiesAreNotOptional: a scope that hides the writer lock,
// and one that hides the database clock, each stop the lifecycle operations before
// they decide anything. Neither is skipped, and neither degrades to the
// application clock.
func TestTheRequiredCapabilitiesAreNotOptional(t *testing.T) {
	ctx := context.Background()
	t.Run("a scope with no writer lock", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		m.UseData(hidingData{ModuleData: m.data, hideLock: true})
		_, err := m.BeginLifecycleActivation(ctx, tenant, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence")},
		})
		if attemptCode(err) != errCodeCapabilityUnavailable {
			t.Fatalf("code = %q, want capability_unavailable", attemptCode(err))
		}
		if n := len(scopeRows(t, st, tenant)); n != 0 {
			t.Fatalf("%d frontier rows were written without the writer lock", n)
		}
	})
	t.Run("a scope with no database clock", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		m.UseData(hidingData{ModuleData: m.data, hideClock: true})
		_, err := m.BeginLifecycleActivation(ctx, tenant, LifecycleActivationRequest{
			Evidence: []EvidenceRef{labEvidence("quiescence")},
		})
		if attemptCode(err) != errCodeCapabilityUnavailable {
			t.Fatalf("code = %q, want capability_unavailable", attemptCode(err))
		}
		if n := len(scopeRows(t, st, tenant)); n != 0 {
			t.Fatalf("%d frontier rows were written without the database clock", n)
		}
	})
}

// hidingData drops one optional capability, which is what a decorator written
// without thinking about optional methods produces.
type hidingData struct {
	api.ModuleData
	hideLock  bool
	hideClock bool
}

func (d hidingData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(d.wrap(sc))
	})
}

func (d hidingData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(d.wrap(sc))
	})
}

func (d hidingData) wrap(sc store.Scope) store.Scope {
	if d.hideLock {
		return noLockScope{Scope: sc}
	}
	return clocklessScope{Scope: sc}
}

// clocklessScope forwards the lock and hides the clock.
type clocklessScope struct{ store.Scope }

func (s clocklessScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

// openLifecycleFinCfg is newLifecycleFin parametrized by store config.
func openLifecycleFinCfg(t testing.TB, cfg store.Config) (*Module, store.Store, model.TenantID, *labVerifier) {
	t.Helper()
	m, st, tenant, _ := openFinCfg(t, cfg)
	v := newLabVerifier(tenant)
	WithAttemptEvidenceVerifier(v)(m)
	m.clock = &fakeClock{t: baseTime}
	return m, st, tenant, v
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

// The attempt lifecycle, first internal cut — enablement/authority, import identity and replay, and
// the monetary behavior of an imported hold.
//
// What the fixture verifier in this file DOES and DOES NOT prove, said once: it
// proves the DEPENDENCY CONTRACT — that the module asks a configured component,
// per call, before it looks anything up, with the exact closed check shape, and
// that a component whose access is withdrawn stops being able to read or replay.
// It is NOT authentication and it is NOT evidence that any deployed authority
// exists. No adapter in New() or in boot supplies one.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// The laboratory verifier
// -----------------------------------------------------------------------------

// labVerifier answers the module's evidence checks from an explicit table of the
// tenants the configured component currently has recovery access to.
//
// It reads that access through the SCOPE it is handed (its tenant), never from the
// request: no OwnerRef, no EvidenceRef and no historical subject reaches it as
// authority, and it returns a constant Actor only for the audit attribution the
// contract asks it to supply. Withdrawing a tenant from the table models the
// component losing access, which is the condition the cut requires the next read
// and the next replay to fail on.
type labVerifier struct {
	mu      sync.Mutex
	granted map[model.TenantID]bool
	actor   VerifiedAttemptActor
	checks  []EvidenceCheck
	scopes  []model.TenantID
	// failOp, when set, refuses exactly one operation kind, so a case can separate
	// "authorized to read" from "authorized to cross a boundary".
	failOp string
}

func newLabVerifier(granted ...model.TenantID) *labVerifier {
	v := &labVerifier{
		granted: map[model.TenantID]bool{},
		actor:   VerifiedAttemptActor{Actor: "lab-reconciler", ActorKind: model.ActorSystem},
	}
	for _, t := range granted {
		v.granted[t] = true
	}
	return v
}

func (v *labVerifier) VerifyAttemptEvidence(_ context.Context, sc store.Scope, c EvidenceCheck) (VerifiedAttemptActor, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.checks = append(v.checks, c)
	v.scopes = append(v.scopes, sc.Tenant())
	if sc.Tenant().IsZero() {
		return VerifiedAttemptActor{}, errors.New("lab: no tenant on the scope")
	}
	if v.failOp != "" && c.Operation == v.failOp {
		return VerifiedAttemptActor{}, errors.New("lab: this component may not perform that operation")
	}
	if !v.granted[sc.Tenant()] {
		return VerifiedAttemptActor{}, errors.New("lab: the component has no current recovery access to this tenant")
	}
	return v.actor, nil
}

func (v *labVerifier) revoke(t model.TenantID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.granted, t)
}

func (v *labVerifier) grant(t model.TenantID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.granted[t] = true
}

func (v *labVerifier) seen() []EvidenceCheck {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]EvidenceCheck, len(v.checks))
	copy(out, v.checks)
	return out
}

// -----------------------------------------------------------------------------
// Fixtures
// -----------------------------------------------------------------------------

// newLifecycleFin opens the SQLite fixture and installs the verifier THROUGH THE
// PUBLIC OPTION, so the wiring the composition would use is the wiring under test.
func newLifecycleFin(t testing.TB) (*Module, store.Store, model.TenantID, *labVerifier) {
	t.Helper()
	m, st, tenant, _ := newFin(t)
	v := newLabVerifier(tenant)
	WithAttemptEvidenceVerifier(v)(m)
	m.clock = &fakeClock{t: baseTime}
	return m, st, tenant, v
}

// legacyChild builds one LEGACY reservation row: both lifecycle linkage columns
// absent, which is what makes it legacy. `at` is an instant INSIDE the period the
// row belongs to; the stored period_start is the bucket the ledger would have
// written, so a fixture cannot accidentally fall outside the query it is meant to
// be found by.
func legacyChild(policy model.ID, handle model.ID, scopeKey, period string, seq, amount int64, at, expires time.Time, state string) model.Record {
	pStart, _ := periodStart(period, at)
	return model.Record{
		colResvPolicyRef:   policy.String(),
		colResvPolicyKind:  policyKindBudget,
		colResvDimension:   "global",
		colResvScopeKey:    scopeKey,
		colResvPeriod:      period,
		colResvPeriodStart: model.NewTimestamp(pStart).String(),
		colResvSeq:         seq,
		colResvAmount:      amount,
		colResvActual:      int64(0),
		colResvState:       state,
		colResvHandle:      handle.String(),
		colResvExpiresAt:   model.NewTimestamp(expires).String(),
	}
}

// seedGroup writes a legacy group and returns the created rows in order.
func seedGroup(t testing.TB, st store.Store, tenant model.TenantID, recs ...model.Record) []model.Record {
	t.Helper()
	out := make([]model.Record, 0, len(recs))
	for _, r := range recs {
		out = append(out, seedReservation(t, st, tenant, r))
	}
	return out
}

// importFor builds the import request naming exactly the rows given, at their
// current versions.
func importFor(t testing.TB, handle model.ID, rows []model.Record, evidence ...EvidenceRef) ImportLegacyHoldRequest {
	t.Helper()
	req := ImportLegacyHoldRequest{
		Handle:      handle,
		OwnerRef:    "lab-reconciler",
		ReviewAfter: model.NewTimestamp(baseTime.Add(24 * time.Hour)),
		Evidence:    evidence,
	}
	if len(evidence) == 0 {
		req.Evidence = []EvidenceRef{labEvidence("recovery-authorization")}
	}
	for _, r := range rows {
		id, err := model.ParseID(r.String(model.ColID))
		if err != nil {
			t.Fatalf("child id: %v", err)
		}
		version, ok := int64Cell(r, model.ColVersion)
		if !ok {
			t.Fatalf("child %s carries no version", id)
		}
		req.Children = append(req.Children, LegacyChildVersion{ID: id, Version: version})
	}
	return req
}

// labEvidence builds a shape-valid evidence reference. Shape-valid is ALL it is:
// the digest points at nothing, which is exactly why the module never treats an
// evidence field as authority.
func labEvidence(ref string) EvidenceRef {
	return EvidenceRef{
		Kind:   evidenceStoreRow,
		Ref:    ref,
		Digest: canonDigest("olivares.finops.lab-evidence", func(w *canonWriter) { w.str(ref) }),
	}
}

// attemptRows returns every attempt parent row of the tenant.
func attemptRows(t testing.TB, st store.Store, tenant model.TenantID) []model.Record {
	t.Helper()
	return extRows(t, st, tenant, attemptKind)
}

// scopeRows returns every lifecycle-scope row of the tenant.
func scopeRows(t testing.TB, st store.Store, tenant model.TenantID) []model.Record {
	t.Helper()
	return extRows(t, st, tenant, lifecycleScopeKind)
}

func extRows(t testing.TB, st store.Store, tenant model.TenantID, kind model.Kind) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		out, _, err = repo.List(context.Background(), model.Query{Limit: listCap})
		return err
	}); err != nil {
		t.Fatalf("list %s: %v", kind, err)
	}
	return out
}

// reservationByID reads one reservation row back.
func reservationByID(t testing.TB, st store.Store, tenant model.TenantID, id model.ID) model.Record {
	t.Helper()
	for _, r := range countReservations(t, st, tenant) {
		if r.String(model.ColID) == id.String() {
			return r
		}
	}
	t.Fatalf("reservation %s is gone", id)
	return nil
}

// -----------------------------------------------------------------------------
// Group 1 — no enablement, no authority
// -----------------------------------------------------------------------------

// TestWithoutAVerifierEveryLifecycleOperationRefusesAndWritesNothing is the
// enablement control: New() installs no verifier, and the cut must be unreachable
// rather than permissive. The three operations refuse with capability_unavailable
// BEFORE any read, and the ledger is untouched — no parent, no frontier, no audit.
func TestWithoutAVerifierEveryLifecycleOperationRefusesAndWritesNothing(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	if m.attemptVerifier != nil {
		t.Fatalf("New() installed an attempt verifier; nothing in boot may fabricate one")
	}
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant, legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))

	auditBefore := len(auditActions(t, st, tenant))

	if _, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
		Evidence: []EvidenceRef{labEvidence("quiescence")},
	}); attemptCode(err) != errCodeCapabilityUnavailable {
		t.Fatalf("Begin without a verifier: code = %q, want capability_unavailable (err=%v)", attemptCode(err), err)
	}
	if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); attemptCode(err) != errCodeCapabilityUnavailable {
		t.Fatalf("Import without a verifier: code = %q, want capability_unavailable (err=%v)", attemptCode(err), err)
	}
	if _, err := m.GetAttempt(context.Background(), tenant, legacyImportRef(tenant, handle)); attemptCode(err) != errCodeCapabilityUnavailable {
		t.Fatalf("GetAttempt without a verifier: code = %q, want capability_unavailable (err=%v)", attemptCode(err), err)
	}

	if n := len(attemptRows(t, st, tenant)); n != 0 {
		t.Errorf("%d attempt rows were written without a verifier", n)
	}
	if n := len(scopeRows(t, st, tenant)); n != 0 {
		t.Errorf("%d lifecycle scope rows were written without a verifier", n)
	}
	if got := len(auditActions(t, st, tenant)); got != auditBefore {
		t.Errorf("audit chain grew by %d events for operations that refused", got-auditBefore)
	}
	// The legacy row is untouched: still legacy, still active, still 7.
	child := reservationByID(t, st, tenant, mustID(t, rows[0].String(model.ColID)))
	if link, _ := linkageOf(child); link != linkageLegacy {
		t.Errorf("the legacy child acquired a lifecycle linkage from a refused import")
	}
}

func mustID(t testing.TB, s string) model.ID {
	t.Helper()
	id, err := model.ParseID(s)
	if err != nil {
		t.Fatalf("parse id %q: %v", s, err)
	}
	return id
}

// TestNeitherOwnerRefNorEvidenceNorAnotherTenantIsAuthority stages the three things
// a caller CAN put in a request and shows none of them is authority: a request
// naming any owner it likes, carrying shape-valid evidence, for a tenant the
// configured component has no access to.
func TestNeitherOwnerRefNorEvidenceNorAnotherTenantIsAuthority(t *testing.T) {
	m, st, tenant, v := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant, legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))

	v.revoke(tenant)
	req := importFor(t, handle, rows)
	req.OwnerRef = "finops-root"
	req.Evidence = []EvidenceRef{labEvidence("an-authorization-i-wrote-myself")}

	if _, err := m.ImportLegacyHold(context.Background(), tenant, req); attemptCode(err) != errCodeOwnerMismatch {
		t.Fatalf("import by an unauthorized component: code = %q, want owner_mismatch (err=%v)", attemptCode(err), err)
	}
	if n := len(attemptRows(t, st, tenant)); n != 0 {
		t.Fatalf("%d attempt rows were written for an unauthorized import", n)
	}
	// The verifier saw the QUERY first and never got as far as the import check:
	// authority is asked before the lookup, so nothing about the ledger leaked.
	checks := v.seen()
	if len(checks) != 1 || checks[0].Operation != opQuery {
		t.Fatalf("checks = %+v, want exactly one query check before the refusal", checks)
	}
}

// TestTheQueryCheckIsTheClosedEmptyForm pins the shape the addendum fixes: the
// operation, the attempt reference for a single-attempt lookup (empty for the
// tenant's scope row), and EVERY other field empty. The absences are deliberate,
// and filling one with a zero digest or a historical owner would make a read look
// like a transition.
func TestTheQueryCheckIsTheClosedEmptyForm(t *testing.T) {
	m, st, tenant, v := newLifecycleFin(t)
	ref := legacyImportRef(tenant, model.NewID())
	_, _ = m.GetAttempt(context.Background(), tenant, ref)
	_, _ = m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
		Evidence: []EvidenceRef{labEvidence("quiescence")},
	})
	_ = st

	var sawAttempt, sawScope bool
	for _, c := range v.seen() {
		if c.Operation != opQuery {
			continue
		}
		switch c.AttemptRef {
		case ref:
			sawAttempt = true
		case "":
			sawScope = true
		default:
			t.Fatalf("a query check named an unexpected attempt %q", c.AttemptRef)
		}
		if c.LegacyImportDigest != nil || c.ScopeFrontierRef != nil || c.EffectDigest != nil ||
			c.ProposedBinding != nil || c.ProposedOutcome != nil || len(c.Evidence) != 0 ||
			c.BindingDigest != "" || c.ReservationDigest != "" || c.ProposedDigest != "" ||
			c.OwnerRef != "" || c.ReasonCode != "" {
			t.Fatalf("a query check carried a non-empty field: %+v", c)
		}
	}
	if !sawAttempt || !sawScope {
		t.Fatalf("query checks: attempt-form=%v scope-form=%v, want both", sawAttempt, sawScope)
	}
	// And the validator refuses the form itself if a field is filled in.
	if err := validateEvidenceCheck(EvidenceCheck{Operation: opQuery, AttemptRef: ref, OwnerRef: "someone"}); err == nil {
		t.Fatalf("a query check carrying an owner was accepted")
	}
}

// TestBeginProducesQuiescingAndNothingElse is the "no activation exists" control,
// asserted two ways: on the durable row, and on the package's own surface. The
// second half is the one that matters — a case can only assert about methods that
// exist, so the absence of the rest is checked by reflection over the module type
// and by reading this package's sources for the one value that would mean active.
func TestBeginProducesQuiescingAndNothingElse(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	seedGroup(t, st, tenant, legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))

	view, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{
		Evidence: []EvidenceRef{labEvidence("quiescence-and-caller-readiness")},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if view.State != lifecycleQuiescing || view.ActivatedAt != nil {
		t.Fatalf("Begin produced state=%q activated_at=%v, want quiescing and nil", view.State, view.ActivatedAt)
	}
	if view.PendingGroupCount != 1 {
		t.Fatalf("pending group count = %d, want 1", view.PendingGroupCount)
	}
	rows := scopeRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("%d lifecycle scope rows, want exactly 1", len(rows))
	}
	if _, activated := textCell(rows[0], colScopeActivated); activated {
		t.Fatalf("Begin stamped activated_at")
	}
	if n := countCosts(t, st, tenant); n != 0 {
		t.Fatalf("Begin produced %d cost records", n)
	}
	if n := len(costSampleRows(t, st, tenant)); n != 0 {
		t.Fatalf("Begin produced %d cost samples", n)
	}
	if got := countAuditAction(t, st, tenant, auditActionBeginActivation); got != 1 {
		t.Fatalf("begin audit events = %d, want exactly 1", got)
	}

	// No method of this module can activate, prepare, mark, settle, release or
	// publish an attempt: those are the operations this cut deliberately does not
	// implement, and a stub returning success would be worse than their absence.
	typ := reflect.TypeOf(m)
	for _, absent := range []string{
		"ActivateLifecycleScope", "PrepareAttempt", "MarkDispatchPossible",
		"RecordAttemptOutcome", "SettleAttempt", "ReleaseAttempt",
		"PublishAttemptCost", "BindImportedAttempt", "ListAttemptsDue",
		"MarkReconciliationDue", "TransferAttemptOwner",
	} {
		if _, ok := typ.MethodByName(absent); ok {
			t.Errorf("the module exposes %s: this cut must not ship an operation it has not built", absent)
		}
	}
	// And no writer in this package can put the active state or an activation
	// instant into the frontier row.
	for _, file := range packageSources(t) {
		body := readFile(t, file)
		for _, forbidden := range []string{
			"colScopeState:      lifecycleActive",
			"colScopeState: lifecycleActive",
			"colScopeActivated:",
			"colScopeActivation:",
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s writes %q: no operation in this cut may activate a scope", filepath.Base(file), forbidden)
			}
		}
	}
}

// packageSources lists the non-test Go sources of this package.
func packageSources(t testing.TB) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatalf("no package sources found; the source control would pass vacuously")
	}
	return out
}

func readFile(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test reading its own package's sources
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// -----------------------------------------------------------------------------
// Current authority is per call: revocation blocks the next read AND the replay
// -----------------------------------------------------------------------------

// TestWithdrawnAccessBlocksTheNextReadAndTheSameExactReplay is the addendum's
// condition. Both calls SUCCEEDED before the withdrawal; neither may succeed after
// it. A replay is not exempt: an exact repeat of an already-applied import is
// still a read of this tenant's ledger, and the previous authorization is not a
// stored permission.
func TestWithdrawnAccessBlocksTheNextReadAndTheSameExactReplay(t *testing.T) {
	m, st, tenant, v := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant, legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	req := importFor(t, handle, rows)

	first, err := m.ImportLegacyHold(context.Background(), tenant, req)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := m.GetAttempt(context.Background(), tenant, first.AttemptRef); err != nil {
		t.Fatalf("read before withdrawal: %v", err)
	}
	if _, err := m.ImportLegacyHold(context.Background(), tenant, req); err != nil {
		t.Fatalf("replay before withdrawal: %v", err)
	}

	v.revoke(tenant)

	if _, err := m.GetAttempt(context.Background(), tenant, first.AttemptRef); attemptCode(err) != errCodeOwnerMismatch {
		t.Fatalf("read after withdrawal: code = %q, want owner_mismatch (err=%v)", attemptCode(err), err)
	}
	if _, err := m.ImportLegacyHold(context.Background(), tenant, req); attemptCode(err) != errCodeOwnerMismatch {
		t.Fatalf("exact replay after withdrawal: code = %q, want owner_mismatch (err=%v)", attemptCode(err), err)
	}
	if n := len(attemptRows(t, st, tenant)); n != 1 {
		t.Fatalf("%d attempt rows after the refusals, want the one original import", n)
	}
}

// TestAReadProducesNoAuditAndNoGrant: a lookup and a replay leave the audit chain
// exactly as they found it. The one import that DID transition wrote one event.
func TestAReadProducesNoAuditAndNoGrant(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant, legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	req := importFor(t, handle, rows)

	view, err := m.ImportLegacyHold(context.Background(), tenant, req)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	after := len(auditActions(t, st, tenant))

	for i := 0; i < 3; i++ {
		if _, err := m.GetAttempt(context.Background(), tenant, view.AttemptRef); err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
		if _, err := m.ImportLegacyHold(context.Background(), tenant, req); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	if got := len(auditActions(t, st, tenant)); got != after {
		t.Fatalf("audit chain grew by %d across three lookups and three replays", got-after)
	}
	if got := countAuditAction(t, st, tenant, auditActionImport); got != 1 {
		t.Fatalf("import audit events = %d, want exactly 1 for one import", got)
	}
}

// -----------------------------------------------------------------------------
// Group 3 — import, identity and replay before the OCC
// -----------------------------------------------------------------------------

// TestImportPreservesTheGroupAndReHoldsAnExpiredChild is the shape of one import:
// two children of one handle, one of them already EXPIRED, become one parent in
// phase outcome_unknown with both children held. Amounts, periods and sequences
// are the originals; the expired child's previous state survives only in the
// immutable snapshot; and no marker, effect or cost is produced.
func TestImportPreservesTheGroupAndReHoldsAnExpiredChild(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	lastMonth := baseTime.AddDate(0, -1, 0)
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 4*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive),
		legacyChild(bid, handle, "seat-a", "monthly", 2, 3*oneUSD, lastMonth, lastMonth.Add(time.Hour), resvStateExpired),
	)

	view, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if view.Phase != phaseOutcomeUnknown {
		t.Fatalf("phase = %q, want outcome_unknown", view.Phase)
	}
	if view.EffectDigest != nil || view.OutcomeDigest != nil || view.SettlementDigest != nil ||
		view.SampleID != nil || view.CostRecordID != nil || view.PublicationState != publicationNone {
		t.Fatalf("the import produced a marker, a settlement link or a publication: %+v", view)
	}
	if view.AccountingAt != nil || view.AccountingBasis.Kind != basisLegacyUnknown {
		t.Fatalf("accounting = %v/%q, want NULL and legacy_unknown", view.AccountingAt, view.AccountingBasis.Kind)
	}
	if view.Binding.Status != bindingLegacyUnbound {
		t.Fatalf("binding status = %q, want legacy_unbound", view.Binding.Status)
	}
	for _, dim := range attributionDimensions {
		if got := view.Binding.Attribution[dim].State; got != factMissing {
			t.Fatalf("attribution %q = %q, want an explicit missing", dim, got)
		}
	}
	if n := countCosts(t, st, tenant); n != 0 {
		t.Fatalf("the import produced %d cost records", n)
	}

	// Both children are now v1, ACTIVE, and carry their original amounts, periods
	// and sequences.
	live := countReservations(t, st, tenant)
	if len(live) != 2 {
		t.Fatalf("%d reservation rows, want the original 2", len(live))
	}
	total := int64(0)
	for _, r := range live {
		link, ref := linkageOf(r)
		if link != linkageV1 || ref != view.AttemptRef {
			t.Fatalf("child %s linkage = %v ref=%q, want v1 under %q", r.String(model.ColID), link, ref, view.AttemptRef)
		}
		if r.String(colResvState) != resvStateActive {
			t.Fatalf("child %s state = %q, want an explicit re-hold as active", r.String(model.ColID), r.String(colResvState))
		}
		if !r.IsNull(colResvSettledAt) {
			t.Fatalf("child %s kept a settled_at after being re-held", r.String(model.ColID))
		}
		amount, _ := int64Cell(r, colResvAmount)
		total += amount
	}
	if total != 7*oneUSD {
		t.Fatalf("held total = %d, want the original 7 USD", total)
	}
	// The ORIGINAL states survive in the immutable snapshot, so the expired child's
	// history is not rewritten by having been re-held.
	if view.LegacyImport == nil || len(view.LegacyImport.OriginalChildren) != 2 {
		t.Fatalf("the import snapshot did not preserve both original rows")
	}
	states := map[string]bool{}
	for _, o := range view.LegacyImport.OriginalChildren {
		states[o.String(colResvState)] = true
	}
	if !states[resvStateActive] || !states[resvStateExpired] {
		t.Fatalf("original states = %v, want the active and the expired one preserved", states)
	}
}

// TestReplayReturnsTheCurrentViewAndRestoresNothing repeats the ORIGINAL request
// after its own import moved every child version, and after the parent's owner was
// changed underneath it. It must return the CURRENT view — not the original owner,
// not a second parent — and it must not need the stale child versions to be true
// again, which is why the lookup precedes them.
func TestReplayReturnsTheCurrentViewAndRestoresNothing(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 4*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	req := importFor(t, handle, rows)

	first, err := m.ImportLegacyHold(context.Background(), tenant, req)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// The child versions the request names are now stale — this import moved them.
	live := countReservations(t, st, tenant)
	if v, _ := int64Cell(live[0], model.ColVersion); v == req.Children[0].Version {
		t.Fatalf("the import did not move the child version; the case would prove nothing")
	}
	// And an operator changes the parent's owner, as a transfer later would.
	mutateAttemptRow(t, st, tenant, first.AttemptRef, func(r model.Record) {
		r[colAttemptOwnerRef] = "another-component"
	})

	replay, err := m.ImportLegacyHold(context.Background(), tenant, req)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if replay.OwnerRef != "another-component" {
		t.Fatalf("replay owner = %q, want the CURRENT owner: a replay restores nothing", replay.OwnerRef)
	}
	if replay.AttemptRef != first.AttemptRef {
		t.Fatalf("replay produced another identity: %q vs %q", replay.AttemptRef, first.AttemptRef)
	}
	if n := len(attemptRows(t, st, tenant)); n != 1 {
		t.Fatalf("%d attempt rows after a replay, want 1", n)
	}
}

// mutateAttemptRow edits a parent row directly, to stage a change no API of this
// cut can make.
func mutateAttemptRow(t testing.TB, st store.Store, tenant model.TenantID, ref AttemptRef, edit func(model.Record)) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(attemptKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{eq(colAttemptRef, string(ref))}, Limit: 2,
		})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("attempt %q: %d rows", ref, len(rows))
		}
		edit(rows[0])
		_, err = repo.Update(context.Background(), rows[0])
		return err
	}); err != nil {
		t.Fatalf("mutate attempt row: %v", err)
	}
}

// TestAnAlteredImportRequestConflictsAndWritesNothing walks the four ways a second
// request can differ from the original. Each one is a DIFFERENT request about the
// same identity, so each conflicts; and a changed child VERSION is told apart from
// a changed child SET, because only one of the two is resolved by reading again.
func TestAnAlteredImportRequestConflictsAndWritesNothing(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 4*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive),
		legacyChild(bid, handle, "seat-a", "monthly", 2, 3*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	original := importFor(t, handle, rows)
	if _, err := m.ImportLegacyHold(context.Background(), tenant, original); err != nil {
		t.Fatalf("import: %v", err)
	}
	auditAfter := len(auditActions(t, st, tenant))

	when := model.NewTimestamp(baseTime.Add(-72 * time.Hour))
	for _, tc := range []struct {
		name string
		want string
		edit func(r *ImportLegacyHoldRequest)
	}{
		{"a dropped child", errCodeImportConflict, func(r *ImportLegacyHoldRequest) { r.Children = r.Children[:1] }},
		{"a moved child version", errCodeImportConflict, func(r *ImportLegacyHoldRequest) { r.Children[0].Version++ }},
		{"other evidence", errCodeImportConflict, func(r *ImportLegacyHoldRequest) {
			r.Evidence = []EvidenceRef{labEvidence("a-different-authorization")}
		}},
		{"a claimed accounting instant", errCodeImportConflict, func(r *ImportLegacyHoldRequest) {
			r.AccountingAt = &when
			r.AccountingEvidence = []EvidenceRef{labEvidence("an-invoice")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			altered := original
			altered.Children = append([]LegacyChildVersion(nil), original.Children...)
			tc.edit(&altered)
			_, err := m.ImportLegacyHold(context.Background(), tenant, altered)
			if got := attemptCode(err); got != tc.want {
				t.Fatalf("code = %q, want %q (err=%v)", got, tc.want, err)
			}
			if n := len(attemptRows(t, st, tenant)); n != 1 {
				t.Fatalf("%d attempt rows after the conflict, want the one original", n)
			}
			if got := len(auditActions(t, st, tenant)); got != auditAfter {
				t.Fatalf("the conflict wrote %d audit events", got-auditAfter)
			}
		})
	}
}

// TestAStaleChildVersionOnAFirstImportIsNotAConflict separates the two: with no
// parent yet, a version that has moved is a stale READ of the right group and is
// resolved by reading it again, not by declaring the request wrong forever.
func TestAStaleChildVersionOnAFirstImportIsNotAConflict(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 4*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	req := importFor(t, handle, rows)
	req.Children[0].Version += 5

	_, err := m.ImportLegacyHold(context.Background(), tenant, req)
	if got := attemptCode(err); got != errCodeStaleAttempt {
		t.Fatalf("code = %q, want stale_attempt (err=%v)", got, err)
	}
	var ae *AttemptError
	if !errors.As(err, &ae) || ae.Retry != retryReadByIdentity {
		t.Fatalf("retry class = %q, want read_by_identity", ae.Retry)
	}
	if n := len(attemptRows(t, st, tenant)); n != 0 {
		t.Fatalf("%d attempt rows after a stale first import", n)
	}
}

// TestTheSameHandleInAnotherTenantIsAnotherIdentity: the import reference is
// derived from tenant AND handle, so the same handle in two tenants cannot collide,
// and a reference that exists only in the other tenant is not found here — without
// that tenant ever being consulted.
func TestTheSameHandleInAnotherTenantIsAnotherIdentity(t *testing.T) {
	m, st, tenantA, v := newLifecycleFin(t)
	tenantB := provisionSecondTenant(t, st)
	v.grant(tenantB)

	handle := model.NewID()
	refA := legacyImportRef(tenantA, handle)
	refB := legacyImportRef(tenantB, handle)
	if refA == refB {
		t.Fatalf("one handle produced one identity in two tenants: %q", refA)
	}

	bid := createBudget(t, st, tenantA, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	rows := seedGroup(t, st, tenantA,
		legacyChild(bid, handle, "", "monthly", 1, 4*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	if _, err := m.ImportLegacyHold(context.Background(), tenantA, importFor(t, handle, rows)); err != nil {
		t.Fatalf("import in A: %v", err)
	}
	if _, err := m.GetAttempt(context.Background(), tenantB, refA); attemptCode(err) != errCodeAttemptNotFound {
		t.Fatalf("reading A's reference from B: code = %q, want attempt_not_found", attemptCode(err))
	}
	if n := len(attemptRows(t, st, tenantB)); n != 0 {
		t.Fatalf("%d attempt rows in the untouched tenant", n)
	}
}

// provisionSecondTenant creates another business tenant on the same store.
func provisionSecondTenant(t testing.TB, st store.Store) model.TenantID {
	t.Helper()
	slug := "beta-" + uniqueSlugSuffix(t)
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision second tenant: %v", err)
	}
	return tenant
}

// TestImportRefusesAGroupItCannotEstablish covers the three ways the group itself
// is not importable: it does not exist, it is already linked, and the request names
// a child that is not in it.
func TestImportRefusesAGroupItCannotEstablish(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})

	absent := model.NewID()
	req := ImportLegacyHoldRequest{
		Handle: absent, OwnerRef: "lab", ReviewAfter: model.NewTimestamp(baseTime),
		Children: []LegacyChildVersion{{ID: model.NewID(), Version: 1}},
		Evidence: []EvidenceRef{labEvidence("recovery")},
	}
	if _, err := m.ImportLegacyHold(context.Background(), tenant, req); attemptCode(err) != errCodeLegacyUnresolved {
		t.Fatalf("absent handle: code = %q, want legacy_unresolved", attemptCode(err))
	}

	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 4*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
		t.Fatalf("first import: %v", err)
	}
	// A second, DIFFERENT request for the now-linked group: the identity exists, so
	// this is an import conflict rather than an unresolved group.
	other := importFor(t, handle, rows)
	other.OwnerRef = "someone-else"
	if _, err := m.ImportLegacyHold(context.Background(), tenant, other); attemptCode(err) != errCodeImportConflict {
		t.Fatalf("already-linked group: code = %q, want import_conflict", attemptCode(err))
	}
}

// -----------------------------------------------------------------------------
// Group 5 — retention, window and monetary integrity
// -----------------------------------------------------------------------------

// TestAnImportedHoldSurvivesTheTTLAndTheRollover is the causal the whole reader
// exists for. The hold is 7 USD, its legacy TTL EXPIRED long ago and its original
// period bucket is a previous month; the query is the CURRENT month. The legacy
// branch, correctly, sees nothing. The v1 branch sees the obligation, and with
// 2 USD of recorded cost against a 10 USD limit exactly 1 USD of headroom is left:
// reserving 1 is admitted and reserving 2 is refused.
func TestAnImportedHoldSurvivesTheTTLAndTheRollover(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "monthly-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	lastMonth := baseTime.AddDate(0, -1, 0)
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, lastMonth, lastMonth.Add(time.Hour), resvStateActive))

	// Before the import the legacy reader already ignores it: expired, and in
	// another bucket. This is the control that the 7 counted below is the V1 branch.
	if got := reservedFor(t, m, st, tenant, bid, ""); got.MicroUSD != 0 {
		t.Fatalf("the legacy branch counted %d for an expired row in a previous bucket", got.MicroUSD)
	}

	if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
		t.Fatalf("import: %v", err)
	}
	got := reservedFor(t, m, st, tenant, bid, "")
	if !got.established() || got.MicroUSD != 7*oneUSD {
		t.Fatalf("held reserve = %+v, want an established 7 USD after the TTL and the rollover", got)
	}

	m.ingest(t, tenant, mkCost("openai", "gpt", "s1", 1, 1, 2*oneUSD, baseTime))
	if res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, 1*oneUSD); err != nil || !res.Allowed {
		t.Fatalf("reserving the last 1 USD of headroom: allowed=%v err=%v reason=%q", res.Allowed, err, res.Reason)
	}
	// Release it again so the next assertion measures the same ledger.
	releaseAll(t, m, tenant, st)
	if res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, 2*oneUSD); err != nil || res.Allowed {
		t.Fatalf("reserving 2 USD against 1 USD of headroom: allowed=%v err=%v", res.Allowed, err)
	}
}

// reservedFor reads the held reserve for a policy through the module's own reader.
func reservedFor(t testing.TB, m *Module, st store.Store, tenant model.TenantID, policy model.ID, scopeKey string) reservedTotal {
	t.Helper()
	now := m.clock.Now().Time()
	pStart, hasLower := periodStart("monthly", now)
	pEnd := periodEnd("monthly", pStart)
	var out reservedTotal
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var rerr error
		out, rerr = heldReservedForWindow(context.Background(), sc, policy, scopeKey, pStart, pEnd, hasLower, now)
		return rerr
	}); err != nil {
		t.Fatalf("heldReservedForWindow: %v", err)
	}
	return out
}

// releaseAll releases every reservation handle written by a Reserve call, so a
// case can measure two admissions against the same ledger state.
func releaseAll(t testing.TB, m *Module, tenant model.TenantID, st store.Store) {
	t.Helper()
	handles := map[string]bool{}
	for _, r := range countReservations(t, st, tenant) {
		if link, _ := linkageOf(r); link != linkageLegacy {
			continue
		}
		handles[r.String(colResvHandle)] = true
	}
	for h := range handles {
		if err := m.ReleaseReservation(context.Background(), tenant, h); err != nil {
			t.Fatalf("release %s: %v", h, err)
		}
	}
}

// TestAnUnknownAccountingInstantIsRetainedAndNeverDated: an import with no
// established historical instant holds its money for CURRENT and FUTURE admission
// and is not excluded by a temporal filter, and it does not acquire a date from
// the import, the row's creation or the clock.
func TestAnUnknownAccountingInstantIsRetainedAndNeverDated(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	view, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if view.AccountingAt != nil {
		t.Fatalf("an unknown accounting instant was given the value %v", view.AccountingAt)
	}
	stored := attemptRows(t, st, tenant)[0]
	if _, present := textCell(stored, colAttemptAccountingAt); present {
		t.Fatalf("the stored row carries an accounting instant that history does not establish")
	}

	// CORRECTED BY R1-R8 (return of 2026-09-08). This loop used to require an
	// established 7 in a window that CLOSED IN THE PAST, and that expectation was the
	// defect, not the code: an obligation whose historical accounting instant was
	// never established cannot be allocated to a specific past month, and publishing
	// it there as exact is how a real caller — ingestion evaluating a late cost
	// sample — produced an exact crossing nothing proves. What survives unchanged is
	// the conservative half: for admission now and in the future the same 7 is still
	// held in full, with no invented date and no zero.
	for _, w := range []struct {
		name  string
		start time.Time
		exact bool
	}{
		{"a window that closed in the past", baseTime.AddDate(-1, 0, 0), false},
		{"the current window", baseTime, true},
		{"a future window", baseTime.AddDate(0, 6, 0), true},
	} {
		pStart, hasLower := periodStart("monthly", w.start)
		pEnd := periodEnd("monthly", pStart)
		var got reservedTotal
		if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
			var rerr error
			got, rerr = heldReservedForWindow(context.Background(), sc, bid, "", pStart, pEnd, hasLower, m.clock.Now().Time())
			return rerr
		}); err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}
		if w.exact {
			if !got.established() || got.MicroUSD != 7*oneUSD {
				t.Fatalf("%s: held = %+v, want the undated obligation retained in full for admission", w.name, got)
			}
			continue
		}
		if got.established() {
			t.Fatalf("%s: an undated obligation was allocated to a closed window as an exact %d", w.name, got.MicroUSD)
		}
		if got.State != reservedNonNegativeUnknown {
			t.Fatalf("%s: state = %q, want nonnegative_unknown: the obligation is real, its allocation is not established", w.name, got.State)
		}
		if got.MicroUSD != 0 {
			t.Fatalf("%s: a figure was published for an unestablished allocation: %d", w.name, got.MicroUSD)
		}
	}
}

// TestAKnownAccountingInstantIsCarriedByTheWindow is the other half: an evidenced
// historical instant IS temporal, so a window that closes before it does not carry
// the hold, and one that closes after it does. Neither answer is produced by a TTL.
func TestAKnownAccountingInstantIsCarriedByTheWindow(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	when := model.NewTimestamp(baseTime)
	req := importFor(t, handle, rows)
	req.AccountingAt = &when
	req.AccountingEvidence = []EvidenceRef{labEvidence("a-historical-invoice")}

	view, err := m.ImportLegacyHold(context.Background(), tenant, req)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if view.AccountingBasis.Kind != basisLegacyEvidenced || view.AccountingAt == nil {
		t.Fatalf("basis = %+v, want legacy_evidenced with the instant", view.AccountingBasis)
	}

	for _, tc := range []struct {
		name string
		at   time.Time
		want int64
	}{
		{"a window that closes before the instant", baseTime.AddDate(0, -2, 0), 0},
		{"the window containing it", baseTime, 7 * oneUSD},
		{"a later window", baseTime.AddDate(0, 3, 0), 7 * oneUSD},
	} {
		pStart, hasLower := periodStart("monthly", tc.at)
		pEnd := periodEnd("monthly", pStart)
		var got reservedTotal
		if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
			var rerr error
			got, rerr = heldReservedForWindow(context.Background(), sc, bid, "", pStart, pEnd, hasLower, m.clock.Now().Time())
			return rerr
		}); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !got.established() || got.MicroUSD != tc.want {
			t.Fatalf("%s: held = %+v, want %d", tc.name, got, tc.want)
		}
	}
}

// TestDeletingTheOnlyChildIsNotAZeroHold is the integrity direction that a reader
// starting from the child rows cannot have: with the child gone, the parent still
// says it exists, so the answer is indeterminate — never a hold of zero.
func TestDeletingTheOnlyChildIsNotAZeroHold(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := reservedFor(t, m, st, tenant, bid, ""); got.MicroUSD != 7*oneUSD {
		t.Fatalf("held before the deletion = %+v", got)
	}

	deleteReservation(t, st, tenant, mustID(t, rows[0].String(model.ColID)))

	got := reservedFor(t, m, st, tenant, bid, "")
	if got.established() {
		t.Fatalf("the hold read as an established %d after its only child was deleted", got.MicroUSD)
	}
	if got.State != reservedIndeterminate {
		t.Fatalf("state = %q, want indeterminate: a missing expected child contradicts the ledger", got.State)
	}
	// And the admission path refuses rather than admitting on the fabricated zero.
	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, 1*oneUSD)
	if err != nil || res.Allowed {
		t.Fatalf("admission on an indeterminate hold: allowed=%v err=%v", res.Allowed, err)
	}
}

func deleteReservation(t testing.TB, st store.Store, tenant model.TenantID, id model.ID) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		return repo.Delete(context.Background(), id)
	}); err != nil {
		t.Fatalf("delete reservation: %v", err)
	}
}

// TestAnOrphanV1ChildAndAMutatedAmountAreRejected: a live v1 child whose parent
// does not exist is refused on its own, and a child whose live amount no longer
// matches the immutable original is refused too. Both would otherwise be summed
// as though they were the obligation the ledger recorded.
func TestAnOrphanV1ChildAndAMutatedAmountAreRejected(t *testing.T) {
	t.Run("orphan", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
		orphan := legacyChild(bid, model.NewID(), "", "monthly", 1, 3*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
		orphan[colResvAttemptRef] = string(legacyImportRef(tenant, model.NewID()))
		orphan[colResvLifecycleVersion] = lifecycleLinkageVersion
		seedGroup(t, st, tenant, orphan)

		got := reservedFor(t, m, st, tenant, bid, "")
		if got.established() || got.State != reservedIndeterminate {
			t.Fatalf("an orphan v1 child produced %+v, want indeterminate", got)
		}
	})

	t.Run("a mutated live amount", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
		handle := model.NewID()
		rows := seedGroup(t, st, tenant,
			legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
		if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
			t.Fatalf("import: %v", err)
		}
		mutateReservation(t, st, tenant, mustID(t, rows[0].String(model.ColID)), func(r model.Record) {
			r[colResvAmount] = int64(1)
		})
		got := reservedFor(t, m, st, tenant, bid, "")
		if got.established() || got.State != reservedIndeterminate {
			t.Fatalf("a mutated amount produced %+v, want indeterminate", got)
		}
	})

	t.Run("a partial linkage", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
		partial := legacyChild(bid, model.NewID(), "", "monthly", 1, 3*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
		partial[colResvLifecycleVersion] = lifecycleLinkageVersion // a version with no reference
		seedGroup(t, st, tenant, partial)

		got := reservedFor(t, m, st, tenant, bid, "")
		if got.established() || got.State != reservedIndeterminate {
			t.Fatalf("a partial linkage produced %+v, want indeterminate — it must not read as legacy", got)
		}
	})
}

func mutateReservation(t testing.TB, st store.Store, tenant model.TenantID, id model.ID, edit func(model.Record)) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), id)
		if err != nil {
			return err
		}
		edit(rec)
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatalf("mutate reservation: %v", err)
	}
}

// TestExactLargeAmountsAndOverflowInTheHoldReader: a value above 2^53 — the point
// where a float64 stops being exact — survives the import and the read unchanged,
// and two obligations whose true sum leaves the int64 range produce an
// indeterminate total rather than a wrapped (negative) one that would admit
// everything.
func TestExactLargeAmountsAndOverflowInTheHoldReader(t *testing.T) {
	const beyondFloat64 = int64(1) << 60 // 1152921504606846976, not representable exactly as a float64

	t.Run("an exact large amount", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
		handle := model.NewID()
		rows := seedGroup(t, st, tenant,
			legacyChild(bid, handle, "", "monthly", 1, beyondFloat64+1, baseTime, baseTime.Add(time.Hour), resvStateActive))
		view, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows))
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		// Through the durable snapshot, which is where a JSON number would have
		// rounded it.
		stored, err := m.GetAttempt(context.Background(), tenant, view.AttemptRef)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		amount, ok := int64Cell(stored.LegacyImport.OriginalChildren[0], colResvAmount)
		if !ok || amount != beyondFloat64+1 {
			t.Fatalf("the original amount came back as %d, want %d exactly", amount, beyondFloat64+1)
		}
		if got := reservedFor(t, m, st, tenant, bid, ""); !got.established() || got.MicroUSD != beyondFloat64+1 {
			t.Fatalf("held = %+v, want the exact %d", got, beyondFloat64+1)
		}
	})

	t.Run("a sum outside int64", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})
		handle := model.NewID()
		rows := seedGroup(t, st, tenant,
			legacyChild(bid, handle, "", "monthly", 1, 1<<62, baseTime, baseTime.Add(time.Hour), resvStateActive),
			legacyChild(bid, handle, "", "monthly", 2, 1<<62, baseTime.AddDate(0, -1, 0), baseTime.Add(time.Hour), resvStateActive))
		// The two children sit in DIFFERENT period buckets of the same policy and
		// scope, which is what the v1 target uniqueness allows and what a real group
		// spanning a rollover looks like. Both are held; their true sum is 2^63,
		// which is outside int64.
		if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
			t.Fatalf("import: %v", err)
		}
		got := reservedFor(t, m, st, tenant, bid, "")
		if got.established() {
			t.Fatalf("a sum outside int64 was published as %d", got.MicroUSD)
		}
		if got.Fault != resvFaultSumUnrepresentable {
			t.Fatalf("fault = %q, want the existing sum-not-representable cause", got.Fault)
		}
	})
}

// TestTheModuleDataSeamIsUnchangedForAnInactiveTenant is the compatibility control
// for a deployment that configures NO verifier at all: with no frontier row, every
// legacy reservation wrapper behaves exactly as it did before this cut.
func TestTheModuleDataSeamIsUnchangedForAnInactiveTenant(t *testing.T) {
	m, st, tenant, _ := newFin(t) // deliberately no verifier
	m.clock = &fakeClock{t: baseTime}
	createBudget(t, st, tenant, "b", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"})

	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, 1*oneUSD)
	if err != nil || !res.Allowed || res.Handle == "" {
		t.Fatalf("reserve on a confirmed-inactive tenant with no verifier: %+v err=%v", res, err)
	}
	if err := m.CommitReservation(context.Background(), tenant, res.Handle, 1*oneUSD); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := m.SweepExpiredReservations(context.Background(), tenant); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	res2, err := m.ReserveSpendLimit(context.Background(), tenant, "actor", nil, 1*oneUSD)
	if err != nil || !res2.Allowed {
		t.Fatalf("reserve spend limit: %+v err=%v", res2, err)
	}
	_ = api.ModuleData(nil)
}

// =============================================================================
// CORRECTION R4a/R4c — the stored documents must prove the data the reader uses
// =============================================================================

// TestAMutatedTargetScopeKeyIsNotATemporalExclusion is the return's precise
// counterexample. Import a hold of 7 for policy P and scope key K; change ONLY
// `targets[0].scope_key` to Q, leaving the child row, the immutable snapshot and
// every stored digest untouched. The parent still parses.
//
// The first cut then read P/K as an established ZERO: the target no longer matched
// the query, so the child was not expected — and because its id was still claimed by
// the parent, the reader treated the disagreement as if the obligation simply fell
// outside the requested window. An attribution that contradicts the ledger is not a
// temporal exclusion, and zero is the one answer that admits everything.
func TestAMutatedTargetScopeKeyIsNotATemporalExclusion(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	view, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := reservedFor(t, m, st, tenant, bid, ""); !got.established() || got.MicroUSD != 7*oneUSD {
		t.Fatalf("held before the mutation = %+v", got)
	}

	mutateAttemptTargets(t, st, tenant, view.AttemptRef, func(targets []jsonTarget) {
		targets[0].ScopeKey = "another-scope"
	})

	got := reservedFor(t, m, st, tenant, bid, "")
	if got.established() {
		t.Fatalf("a target whose attribution contradicts its own child read as an established %d", got.MicroUSD)
	}
	if got.State != reservedIndeterminate {
		t.Fatalf("state = %q, want indeterminate", got.State)
	}
	// The admission path must refuse rather than admit on the fabricated zero.
	res, rerr := m.ReserveBudget(context.Background(), tenant, SpendDims{}, 1*oneUSD)
	if rerr != nil || res.Allowed {
		t.Fatalf("admission on a contradictory manifest: allowed=%v err=%v", res.Allowed, rerr)
	}
	// And the parent itself is no longer readable as a coherent attempt.
	if _, gerr := m.GetAttempt(context.Background(), tenant, view.AttemptRef); attemptCode(gerr) != errCodeLedgerIndeterminate {
		t.Fatalf("GetAttempt: code = %q, want ledger_indeterminate", attemptCode(gerr))
	}
}

// TestAStoredDigestThatNoLongerCommitsToItsDataIsRefused walks the other stored
// commitments this phase CAN recompute. Each edit keeps the row parseable and
// changes data the digest is supposed to cover.
func TestAStoredDigestThatNoLongerCommitsToItsDataIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(t testing.TB, st store.Store, tenant model.TenantID, ref AttemptRef)
	}{
		{"the binding digest no longer covers the binding", func(t testing.TB, st store.Store, tenant model.TenantID, ref AttemptRef) {
			mutateAttemptRow(t, st, tenant, ref, func(r model.Record) {
				var b jsonBinding
				if err := strictUnmarshal(r.String(colAttemptBinding), &b); err != nil {
					t.Fatalf("decode binding: %v", err)
				}
				b.Status = bindingResolved
				body, err := marshalBounded(b, maxAttemptPayloadBytes, "binding")
				if err != nil {
					t.Fatalf("encode binding: %v", err)
				}
				r[colAttemptBinding] = body
			})
		}},
		{"an original child amount was edited under its group digest", func(t testing.TB, st store.Store, tenant model.TenantID, ref AttemptRef) {
			mutateAttemptRow(t, st, tenant, ref, func(r model.Record) {
				var snaps []jsonImportSnapshot
				if err := strictUnmarshal(r.String(colAttemptLegacyHandles), &snaps); err != nil {
					t.Fatalf("decode snapshot: %v", err)
				}
				snaps[0].OriginalChildren[0].Amount = jsonInt(1)
				body, err := marshalBounded(snaps, maxAttemptPayloadBytes, "snapshot")
				if err != nil {
					t.Fatalf("encode snapshot: %v", err)
				}
				r[colAttemptLegacyHandles] = body
			})
		}},
		{"the target set no longer names the original children", func(t testing.TB, st store.Store, tenant model.TenantID, ref AttemptRef) {
			mutateAttemptTargets(t, st, tenant, ref, func(targets []jsonTarget) {
				targets[0].ChildID = model.NewID().String()
			})
		}},
		{"the owner epoch was rewound below its floor", func(t testing.TB, st store.Store, tenant model.TenantID, ref AttemptRef) {
			mutateAttemptRow(t, st, tenant, ref, func(r model.Record) { r[colAttemptOwnerEpoch] = int64(0) })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newLifecycleFin(t)
			bid := createBudget(t, st, tenant, "b", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			handle := model.NewID()
			rows := seedGroup(t, st, tenant,
				legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
			view, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows))
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			tc.edit(t, st, tenant, view.AttemptRef)

			if _, gerr := m.GetAttempt(context.Background(), tenant, view.AttemptRef); attemptCode(gerr) != errCodeLedgerIndeterminate {
				t.Fatalf("GetAttempt: code = %q, want ledger_indeterminate", attemptCode(gerr))
			}
			if got := reservedFor(t, m, st, tenant, bid, ""); got.established() {
				t.Fatalf("the hold reader published an established %d over an unproven document", got.MicroUSD)
			}
		})
	}
}

// TestAPhaseThisCutCannotInterpretIsRefusedRatherThanTrusted: knowing a phase's NAME
// is not the same as being able to read the data that phase implies. A parent in
// cost_ready carries an outcome and a price this binary has no decoder for, so
// treating it as "held, trust its manifest" would rest a hold on fields nobody
// validated. It is an integrity error until the phase's own reader ships.
func TestAPhaseThisCutCannotInterpretIsRefusedRatherThanTrusted(t *testing.T) {
	for _, phase := range []AttemptPhase{phaseDispatchPossible, phaseUsageKnown, phaseCostReady, phaseSettled} {
		t.Run(string(phase), func(t *testing.T) {
			m, st, tenant, _ := newLifecycleFin(t)
			bid := createBudget(t, st, tenant, "b", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			handle := model.NewID()
			rows := seedGroup(t, st, tenant,
				legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
			view, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows))
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			mutateAttemptRow(t, st, tenant, view.AttemptRef, func(r model.Record) {
				r[colAttemptPhase] = string(phase)
			})
			if _, gerr := m.GetAttempt(context.Background(), tenant, view.AttemptRef); attemptCode(gerr) != errCodeLedgerIndeterminate {
				t.Fatalf("GetAttempt: code = %q, want ledger_indeterminate", attemptCode(gerr))
			}
			if got := reservedFor(t, m, st, tenant, bid, ""); got.established() {
				t.Fatalf("the reader published an established %d for a phase it cannot interpret", got.MicroUSD)
			}
		})
	}
}

// mutateAttemptTargets edits the stored target manifest in place.
func mutateAttemptTargets(t testing.TB, st store.Store, tenant model.TenantID, ref AttemptRef, edit func([]jsonTarget)) {
	t.Helper()
	mutateAttemptRow(t, st, tenant, ref, func(r model.Record) {
		var targets []jsonTarget
		if err := strictUnmarshal(r.String(colAttemptTargets), &targets); err != nil {
			t.Fatalf("decode targets: %v", err)
		}
		edit(targets)
		body, err := marshalBounded(targets, maxAttemptPayloadBytes, "targets")
		if err != nil {
			t.Fatalf("encode targets: %v", err)
		}
		r[colAttemptTargets] = body
	})
}

// =============================================================================
// CORRECTION R6 — contradictions are inspected BEFORE the non-negative class
// =============================================================================

// TestAContradictoryV1PrefixNeverEarnsTheNonNegativeClass. A4.2 established the
// distinction this restores: a paging enumeration whose observed rows are all clean
// carries the non-negative invariant forward and a lower bound may rest on it,
// while an OBSERVED contradiction proves nothing. The first cut returned the clean
// class as soon as either scan was incomplete — before it had looked at a single
// row — so a prefix containing a foreign-tenant child, a negative amount or an
// unreadable parent was indistinguishable from a clean one.
func TestAContradictoryV1PrefixNeverEarnsTheNonNegativeClass(t *testing.T) {
	seedImported := func(t *testing.T) (*Module, store.Store, model.TenantID, model.ID, []model.Record) {
		t.Helper()
		m, st, tenant, _ := newLifecycleFin(t)
		bid := createBudget(t, st, tenant, "b", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		handle := model.NewID()
		rows := seedGroup(t, st, tenant,
			legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
		if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
			t.Fatalf("import: %v", err)
		}
		return m, st, tenant, bid, rows
	}

	// extraPageRows lets a case put a row on the observed PAGE that the tenant's own
	// repository would never return. It is reset per case.
	var extraPageRows []model.Record

	for _, tc := range []struct {
		name  string
		stage func(t *testing.T, m *Module, st store.Store, tenant model.TenantID, bid model.ID, rows []model.Record)
	}{
		{"a negative amount in the observed prefix", func(t *testing.T, m *Module, st store.Store, tenant model.TenantID, bid model.ID, rows []model.Record) {
			mutateReservation(t, st, tenant, mustID(t, rows[0].String(model.ColID)), func(r model.Record) {
				r[colResvAmount] = int64(-1)
			})
		}},
		{"a foreign tenant in the observed prefix", func(t *testing.T, m *Module, st store.Store, tenant model.TenantID, bid model.ID, rows []model.Record) {
			// Injected through the page rather than written: the repository is
			// tenant-pinned, so a row carrying another tenant's id is a shape a STORE
			// hands back, not one this tenant's ledger can contain. The reader must
			// still refuse to rest a bound on a page that contains it.
			foreign := legacyChild(bid, model.NewID(), "", "monthly", 9, 3*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
			foreign[model.ColID] = model.NewID().String()
			foreign[model.ColVersion] = int64(1)
			foreign[model.ColTenantID] = model.NewID().String()
			foreign[colResvAttemptRef] = string(legacyImportRef(tenant, model.NewID()))
			foreign[colResvLifecycleVersion] = lifecycleLinkageVersion
			extraPageRows = []model.Record{foreign}
		}},
		{"a malformed linkage in the observed prefix", func(t *testing.T, m *Module, st store.Store, tenant model.TenantID, bid model.ID, rows []model.Record) {
			mutateReservation(t, st, tenant, mustID(t, rows[0].String(model.ColID)), func(r model.Record) {
				r[colResvLifecycleVersion] = int64(99)
			})
		}},
		{"an unreadable parent in the observed prefix", func(t *testing.T, m *Module, st store.Store, tenant model.TenantID, bid model.ID, rows []model.Record) {
			for _, row := range attemptRows(t, st, tenant) {
				mutateAttemptRow(t, st, tenant, AttemptRef(row.String(colAttemptRef)), func(r model.Record) {
					r[colAttemptOwnerRef] = ""
				})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, bid, rows := seedImported(t)
			extraPageRows = nil
			tc.stage(t, m, st, tenant, bid, rows)
			// The enumeration is ALSO cut short, which is what used to short-circuit
			// the inspection.
			forcePager(m, alwaysMorePagerOver(t, st, tenant, extraPageRows...))

			got := reservedThroughModule(t, m, tenant, bid, "")
			if got.established() {
				t.Fatalf("an established %d was published from a contradictory prefix", got.MicroUSD)
			}
			if got.State != reservedIndeterminate {
				t.Fatalf("state = %q, want indeterminate: a contradiction was observed, so no bound may rest on it", got.State)
			}
		})
	}

	// The positive control the correction must preserve: a CLEAN prefix cut short by
	// paging still earns the non-negative class, because nothing observed
	// contradicts the invariant.
	t.Run("a clean incomplete prefix keeps the non-negative class", func(t *testing.T) {
		m, st, tenant, bid, _ := seedImported(t)
		forcePager(m, alwaysMorePagerOver(t, st, tenant))
		got := reservedThroughModule(t, m, tenant, bid, "")
		if got.established() {
			t.Fatalf("an incomplete enumeration published an established total: %+v", got)
		}
		if got.State != reservedNonNegativeUnknown {
			t.Fatalf("state = %q, want nonnegative_unknown: nothing observed contradicts the invariant", got.State)
		}
		if got.MicroUSD != 0 {
			t.Fatalf("a prefix sum was published as a total: %d", got.MicroUSD)
		}
	})
}

// reservedThroughModule reads the held reserve through the MODULE's own data
// handle, so a scope decorator a case installed (a paging fixture, a failing read)
// is actually in the path. reservedFor talks to the store directly and would
// silently bypass it.
func reservedThroughModule(t testing.TB, m *Module, tenant model.TenantID, policy model.ID, scopeKey string) reservedTotal {
	t.Helper()
	now := m.clock.Now().Time()
	pStart, hasLower := periodStart("monthly", now)
	pEnd := periodEnd("monthly", pStart)
	var out reservedTotal
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		var rerr error
		out, rerr = heldReservedForWindow(context.Background(), sc, policy, scopeKey, pStart, pEnd, hasLower, now)
		return rerr
	}); err != nil {
		t.Fatalf("heldReservedForWindow: %v", err)
	}
	return out
}

// alwaysMorePagerOver replays the tenant's REAL reservation rows on every page and
// never stops, so the enumeration ends at the page cap with rows still promised —
// the clean-prefix shape — while the rows it observes are the real ones the case
// staged.
func alwaysMorePagerOver(t testing.TB, st store.Store, tenant model.TenantID, extra ...model.Record) func(int, model.Query) ([]model.Record, model.Page) {
	t.Helper()
	rows := append(countReservations(t, st, tenant), extra...)
	return func(call int, _ model.Query) ([]model.Record, model.Page) {
		return rows, model.Page{Cursor: "cursor-" + strconv.Itoa(call+1), HasMore: true}
	}
}

// =============================================================================
// CORRECTION R7 — the child set is canonicalized, so a permutation is a replay
// =============================================================================

// TestAPermutedChildSetIsTheSameImportRequest: the contract fixes a complete
// SORTED set of child ids and versions. The first cut framed the caller's order, so
// repeating the very same intention with the two children swapped produced different
// bytes, a different request digest and an import_conflict — a recovery caller that
// rebuilt its set from a map could not replay its own import.
func TestAPermutedChildSetIsTheSameImportRequest(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 4*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive),
		legacyChild(bid, handle, "seat-a", "monthly", 2, 3*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	req := importFor(t, handle, rows)
	if len(req.Children) != 2 {
		t.Fatalf("the fixture must name two children")
	}
	first, err := m.ImportLegacyHold(context.Background(), tenant, req)
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	permuted := req
	permuted.Children = []LegacyChildVersion{req.Children[1], req.Children[0]}
	replay, err := m.ImportLegacyHold(context.Background(), tenant, permuted)
	if err != nil {
		t.Fatalf("the same set in another order was refused: %v", err)
	}
	if replay.AttemptRef != first.AttemptRef {
		t.Fatalf("the permuted replay produced another identity: %q vs %q", replay.AttemptRef, first.AttemptRef)
	}
	if n := len(attemptRows(t, st, tenant)); n != 1 {
		t.Fatalf("%d attempt rows after a permuted replay", n)
	}

	// A DIFFERENT set still conflicts, and a duplicated id is still invalid: the
	// canonical order is not a license to accept a different request.
	dropped := req
	dropped.Children = req.Children[:1]
	if _, err := m.ImportLegacyHold(context.Background(), tenant, dropped); attemptCode(err) != errCodeImportConflict {
		t.Fatalf("a dropped child: code = %q, want import_conflict", attemptCode(err))
	}
	duplicated := req
	duplicated.Children = []LegacyChildVersion{req.Children[0], req.Children[0]}
	if _, err := m.ImportLegacyHold(context.Background(), tenant, duplicated); attemptCode(err) != errCodeInvalidAttempt {
		t.Fatalf("a duplicated child: code = %q, want invalid_attempt", attemptCode(err))
	}
}

// =============================================================================
// CORRECTION R8 — a nullable absence survives, and the codec is closed
// =============================================================================

// TestANullDimensionIsNotAnEmptyDimension. The historical reservation schema allows
// a NULL dimension. The first cut stored the zero string for both, so an absence and
// a present empty value produced the SAME snapshot and the same group digest — the
// import claimed to preserve history it had silently completed.
func TestANullDimensionIsNotAnEmptyDimension(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})

	nullHandle, emptyHandle := model.NewID(), model.NewID()
	withNull := legacyChild(bid, nullHandle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
	delete(withNull, colResvDimension) // the column is nullable and this row leaves it unset
	withEmpty := legacyChild(bid, emptyHandle, "", "monthly", 2, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
	withEmpty[colResvDimension] = ""

	nullRows := seedGroup(t, st, tenant, withNull)
	emptyRows := seedGroup(t, st, tenant, withEmpty)
	if !nullRows[0].IsNull(colResvDimension) {
		t.Fatalf("the fixture did not produce a NULL dimension; the case would be vacuous")
	}
	if v, ok := textCell(emptyRows[0], colResvDimension); !ok || v != "" {
		t.Fatalf("the fixture did not produce a present empty dimension; the case would be vacuous")
	}

	nullView, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, nullHandle, nullRows))
	if err != nil {
		t.Fatalf("import the NULL-dimension group: %v", err)
	}
	if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, emptyHandle, emptyRows)); err != nil {
		t.Fatalf("import the empty-dimension group: %v", err)
	}

	// The absence survives the round trip.
	stored, err := m.GetAttempt(context.Background(), tenant, nullView.AttemptRef)
	if err != nil {
		t.Fatalf("read back the NULL-dimension import: %v", err)
	}
	if !stored.LegacyImport.OriginalChildren[0].IsNull(colResvDimension) {
		t.Fatalf("a NULL dimension came back as %#v; the import completed history it did not know",
			stored.LegacyImport.OriginalChildren[0][colResvDimension])
	}
	// AND THE DIGEST COMPARISON ISOLATES THE DIMENSION. The two imported groups
	// above differ in handle, id and sequence as well, so comparing THEIR digests
	// would prove only that two different groups hash differently. The isolated fact
	// is this: one original row, copied, with the dimension absent in one copy and
	// present-and-empty in the other, and nothing else changed at all.
	base, err := childRowSnapshot(nullRows[0])
	if err != nil {
		t.Fatalf("snapshot the NULL row: %v", err)
	}
	if base.Dimension != nil {
		t.Fatalf("the snapshot did not preserve the absence; the isolation would be vacuous")
	}
	withEmptyDim := base
	empty := ""
	withEmptyDim.Dimension = &empty
	if base.ID != withEmptyDim.ID || base.Seq != withEmptyDim.Seq || base.Handle != withEmptyDim.Handle {
		t.Fatalf("the two copies are not the same row; the comparison would not isolate the dimension")
	}
	nullDigest := groupDigest(tenant, nullHandle, []jsonChildRow{base})
	emptyDigest := groupDigest(tenant, nullHandle, []jsonChildRow{withEmptyDim})
	if nullDigest == emptyDigest {
		t.Fatalf("one row hashed identically with the dimension absent and with it present-and-empty")
	}
	// The same two copies also frame differently, which is where the digest gets the
	// difference from.
	if string(canonBytes("finops.lab", base.canon)) == string(canonBytes("finops.lab", withEmptyDim.canon)) {
		t.Fatalf("presence is not framed: the two copies produced identical canonical bytes")
	}

	// A cell of the WRONG TYPE is neither: the snapshot refuses it rather than
	// guessing, so a row this codec cannot read exactly never becomes history.
	badHandle := model.NewID()
	badRow := legacyChild(bid, badHandle, "", "monthly", 3, oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive)
	badRow[colResvDimension] = int64(7)
	if _, err := childRowSnapshot(badRow); err == nil {
		t.Fatalf("a non-text dimension cell was accepted into the immutable original")
	}
}

// TestTheDurableCodecIsClosed: the JSON helper must require an actual end of input
// and refuse a duplicated key. `Decoder.More()` asks whether another ELEMENT
// follows inside an array or object — it is false at top level whatever trails the
// document — so a stray closing brace used to pass, and a repeated key silently
// took its last value.
func TestTheDurableCodecIsClosed(t *testing.T) {
	// `inner` is a DECLARED object. That matters for the nested-duplicate case: the
	// previous version nested the duplicate under an UNKNOWN key, which
	// DisallowUnknownFields rejects on its own, so the case could not tell the token
	// walk's duplicate detection from the decoder's unknown-field refusal. With a
	// declared nested object the only reason left to refuse is the duplicate.
	type inner struct {
		Y jsonInt `json:"y"`
	}
	type doc struct {
		Kind  string  `json:"kind"`
		N     jsonInt `json:"n"`
		Inner inner   `json:"inner"`
	}
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"a clean document", `{"kind":"a","n":"1"}`, true},
		{"a clean document with a declared nested object", `{"kind":"a","n":"1","inner":{"y":"1"}}`, true},
		{"a trailing closing brace", `{"kind":"a","n":"1"}}`, false},
		{"a trailing closing bracket", `{"kind":"a","n":"1"}]`, false},
		{"a second document", `{"kind":"a","n":"1"}{"kind":"b","n":"2"}`, false},
		{"trailing rubbish", `{"kind":"a","n":"1"} not json`, false},
		{"a duplicated key", `{"kind":"a","kind":"b","n":"1"}`, false},
		{"a duplicated key in a DECLARED nested object", `{"kind":"a","n":"1","inner":{"y":"1","y":"2"}}`, false},
		{"an unknown field", `{"kind":"a","n":"1","z":"3"}`, false},
		{"an integer sent as a JSON number", `{"kind":"a","n":1}`, false},
		{"a non-canonical integer string", `{"kind":"a","n":"01"}`, false},
		{"a null where a string is declared", `{"kind":null,"n":"1"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out doc
			err := strictUnmarshal(tc.body, &out)
			if tc.ok && err != nil {
				t.Fatalf("a valid document was refused: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("the codec accepted %q", tc.body)
			}
		})
	}
}

// =============================================================================
// RESIDUAL R6 — a complete child census proves an absence a partial parent scan
// cannot undo
// =============================================================================

// truncatedParentData truncates the ATTEMPT repository and only that one. The
// existing R6 fixture truncates RESERVATIONS through forcePager, so its greens say
// nothing about a partial PARENT enumeration; this is the independent pager the
// residual return asks for.
//
// It hands back the real rows ONCE and then empty pages, always promising more
// with an advancing cursor, so the parent scan ends at the page cap with rows still
// promised while every row it observed is a genuine one, observed once. (Replaying
// the same page would stage a duplicated parent instead, which is a different
// defect and one the reader already refuses.) The reservation repository is
// untouched, so the child census is COMPLETE.
type truncatedParentData struct {
	api.ModuleData
	rows []model.Record
}

func (d truncatedParentData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(truncatedParentScope{Scope: sc, rows: d.rows})
	})
}

func (d truncatedParentData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(truncatedParentScope{Scope: sc, rows: d.rows})
	})
}

type truncatedParentScope struct {
	store.Scope
	rows []model.Record
}

func (s truncatedParentScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

func (s truncatedParentScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	clock, ok := s.Scope.(store.TransactionClock)
	if !ok {
		return model.Timestamp{}, errors.New("finops-test: wrapped scope provides no transaction clock")
	}
	return clock.TransactionNow(ctx)
}

func (s truncatedParentScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != attemptKind {
		return repo, err
	}
	return &truncatedParentRepo{GenericRepo: repo, rows: s.rows}, nil
}

type truncatedParentRepo struct {
	store.GenericRepo
	rows []model.Record
	call int
}

func (r *truncatedParentRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	if len(q.Sort) > 0 || q.Limit == 2 {
		// The exact-lookup queries (by ref, by tenant) stay on the real repository:
		// this fixture truncates the CENSUS scan, not identity lookups.
		return r.GenericRepo.List(ctx, q)
	}
	r.call++
	if r.call > 1 {
		return nil, model.Page{Cursor: "parent-page-" + strconv.Itoa(r.call), HasMore: true}, nil
	}
	return r.rows, model.Page{Cursor: "parent-page-1", HasMore: true}, nil
}

// TestAPartialParentScanCannotHideAnAbsenceTheChildCensusProves. The reader
// returned the non-negative class the moment the PARENT enumeration was incomplete,
// before it checked for expected children that were never observed. But the two
// enumerations are independent: if the child census for this policy and scope
// COMPLETED and does not contain child C, then C does not exist — and a parent the
// scan has not reached yet cannot put it back into a census that finished.
//
// The observed parent P claims C, the complete child census lacks it, and the
// contradiction is therefore already established. Granting the non-negative class
// lets A4.2's algebra rest a published lower bound on it.
func TestAPartialParentScanCannotHideAnAbsenceTheChildCensusProves(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	bid := createBudget(t, st, tenant, "b", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	handle := model.NewID()
	rows := seedGroup(t, st, tenant,
		legacyChild(bid, handle, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
		t.Fatalf("import: %v", err)
	}
	parents := attemptRows(t, st, tenant)
	if len(parents) != 1 {
		t.Fatalf("%d parent rows, want 1", len(parents))
	}

	// The only child of the observed parent is deleted, so the COMPLETE child census
	// for this policy and scope no longer contains it.
	deleteReservation(t, st, tenant, mustID(t, rows[0].String(model.ColID)))
	m.UseData(truncatedParentData{ModuleData: m.data, rows: parents})

	got := reservedThroughModule(t, m, tenant, bid, "")
	if got.established() {
		t.Fatalf("an established %d was published while a claimed child is provably absent", got.MicroUSD)
	}
	if got.State != reservedIndeterminate {
		t.Fatalf("state = %q, want indeterminate: the complete child census already proves the absence, "+
			"and the parents still unread cannot create a child in a census that finished", got.State)
	}

	// THE CLEAN POSITIVE CONTROL: the same partial parent enumeration with every
	// expected child present is still merely unobserved, and keeps its bound. A
	// partial input is not indeterminate by itself.
	m2, st2, tenant2, _ := newLifecycleFin(t)
	bid2 := createBudget(t, st2, tenant2, "b", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	handle2 := model.NewID()
	rows2 := seedGroup(t, st2, tenant2,
		legacyChild(bid2, handle2, "", "monthly", 1, 7*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
	if _, err := m2.ImportLegacyHold(context.Background(), tenant2, importFor(t, handle2, rows2)); err != nil {
		t.Fatalf("import: %v", err)
	}
	m2.UseData(truncatedParentData{ModuleData: m2.data, rows: attemptRows(t, st2, tenant2)})
	clean := reservedThroughModule(t, m2, tenant2, bid2, "")
	if clean.established() {
		t.Fatalf("an incomplete parent enumeration published an established total: %+v", clean)
	}
	if clean.State != reservedNonNegativeUnknown {
		t.Fatalf("state = %q, want nonnegative_unknown: nothing observed contradicts the invariant", clean.State)
	}
	if clean.MicroUSD != 0 {
		t.Fatalf("a prefix sum was published as a total: %d", clean.MicroUSD)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

// The KNOWN-EMPTY census: a tenant whose complete enumeration finds zero
// pending legacy groups.
//
// The producer and the reader disagreed about how that fact is spelled.
// censusLegacyGroups appends nothing, so it hands back a NIL slice; json.Marshal
// writes nil as `null`; and this module's codec refuses a null anywhere on the way
// back in (attempt_schema.go, scanStrictJSON). Begin therefore committed a durable
// boundary and answered from an in-memory view, and every later reader of that same
// row — the replay, the covered legacy wrappers, the import — got
// ledger_indeterminate forever. Nothing detected it, because the ONLY oracle any
// case had used was Begin's own return value.
//
// So the oracle here is never the return of Begin. It is the row read back through
// the real internal reader, the guard the product actually calls, and the bytes in
// the cell.
//
// What these cases deliberately do NOT do:
//
//   - they do not teach the reader to accept a null. A stored null stays refused,
//     and TestAStoredNullPendingSetIsStillRefused is the case that says so;
//   - they do not stage the fixed shape by hand. No case writes `[]` into a stored
//     document: every readable frontier below was produced by BeginLifecycleActivation.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// The three shapes of an empty census
// -----------------------------------------------------------------------------

// emptyCensusCase seeds one tenant whose complete enumeration yields NO pending
// legacy group, by one of the three routes that can produce that state.
type emptyCensusCase struct {
	name string
	// seed prepares the tenant and returns how many reservation rows it left, so a
	// case can tell "nothing to census" apart from "rows that census to nothing".
	seed func(t *testing.T, m *Module, st store.Store, tenant model.TenantID) int
}

var emptyCensusCases = []emptyCensusCase{{
	name: "a tenant with no reservation rows at all",
	seed: func(*testing.T, *Module, store.Store, model.TenantID) int { return 0 },
}, {
	name: "a tenant whose only legacy history is terminal",
	seed: func(t *testing.T, _ *Module, st store.Store, tenant model.TenantID) int {
		bid := createBudget(t, st, tenant, "b", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		last := baseTime.AddDate(0, -1, 0)
		seedGroup(t, st, tenant,
			legacyChild(bid, model.NewID(), "", "monthly", 1, 9*oneUSD, last, last.Add(time.Hour), resvStateCommitted))
		seedGroup(t, st, tenant,
			legacyChild(bid, model.NewID(), "", "monthly", 2, 4*oneUSD, last, last.Add(time.Hour), resvStateReleased))
		return 2
	},
}, {
	name: "a tenant whose only group was already imported to v1",
	seed: func(t *testing.T, m *Module, st store.Store, tenant model.TenantID) int {
		bid := createBudget(t, st, tenant, "b", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		handle := model.NewID()
		rows := seedGroup(t, st, tenant,
			legacyChild(bid, handle, "", "monthly", 1, 3*oneUSD, baseTime, baseTime.Add(time.Hour), resvStateActive))
		// The group leaves the legacy ledger through the real import, not by hand: a
		// hand-linked row would prove the census can skip a shape, not that a genuine
		// v1 group censuses to nothing pending.
		if _, err := m.ImportLegacyHold(context.Background(), tenant, importFor(t, handle, rows)); err != nil {
			t.Fatalf("import before Begin: %v", err)
		}
		return 1
	},
}}

// TestAnEmptyCensusIsDurableAndReplays is the causal case.
//
// Before the correction every subtest here failed at the FIRST read-back, on both
// backends, with ledger_indeterminate: the row said `"pending_groups":null` and the
// strict codec refused it. Begin's own return said nothing was wrong.
//
// It runs on a real SQLite FILE database and, when one is configured, on a real
// isolated PostgreSQL. The durable shape is what is under test, so the engine that
// stores and returns the cell is part of the claim.
func TestAnEmptyCensusIsDurableAndReplays(t *testing.T) {
	eachAttemptBackend(t, func(t *testing.T, cfg store.Config) {
		for _, tc := range emptyCensusCases {
			t.Run(tc.name, func(t *testing.T) {
				m, st, tenant, _ := openLifecycleFinCfg(t, cfg)
				seeded := tc.seed(t, m, st, tenant)
				if got := len(countReservations(t, st, tenant)); got != seeded {
					t.Fatalf("the fixture left %d reservation rows, want %d", got, seeded)
				}

				req := LifecycleActivationRequest{
					Evidence: []EvidenceRef{labEvidence("quiescence"), labEvidence("caller-readiness")},
				}
				begun, err := m.BeginLifecycleActivation(context.Background(), tenant, req)
				if err != nil {
					t.Fatalf("Begin over an empty census: %v", err)
				}
				if begun.PendingGroupCount != 0 {
					t.Fatalf("Begin reported %d pending groups, want the empty census", begun.PendingGroupCount)
				}

				// ORACLE 1 — the bytes, checked FIRST because the defect is a
				// serialization and this is the only oracle that names it directly. Read
				// before the correction, this line reports the whole failure in one
				// message: `"pending_groups":null` in the committed cell.
				body := storedFrontierBody(t, st, tenant)
				if strings.Contains(body, `"pending_groups":null`) {
					t.Fatalf("the frontier was committed with a null pending set: %s", body)
				}
				if !strings.Contains(body, `"pending_groups":[]`) {
					t.Fatalf("the frontier does not carry the canonical empty array: %s", body)
				}
				// The committed document is the artifact under test, so it is logged
				// rather than only asserted about: a run of this case is a receipt of the
				// exact bytes a tenant with an empty census now carries.
				t.Logf("committed census document: %s", body)

				// ORACLE 2 — the row read back through the module's own reader. This is
				// what Begin's return value never proved.
				view, found, rerr := readScopeThrough(t, st, tenant)
				if rerr != nil {
					t.Fatalf("reading the committed boundary back: %v (code %q)", rerr, attemptCode(rerr))
				}
				if !found {
					t.Fatalf("Begin committed a boundary the reader cannot find")
				}
				if view.ID != begun.ID || view.Version != begun.Version {
					t.Fatalf("the durable row is another boundary: %+v vs %+v", view, begun)
				}
				if view.FrontierDigest != begun.FrontierDigest ||
					view.PendingGroupDigest != begun.PendingGroupDigest ||
					view.HistoricalTerminalDigest != begun.HistoricalTerminalDigest {
					t.Fatalf("the durable digests differ from the ones Begin answered: %+v vs %+v", view, begun)
				}
				if view.PendingGroupCount != 0 || view.State != lifecycleQuiescing {
					t.Fatalf("durable view = %+v, want a quiescing boundary over zero pending groups", view)
				}

				// ORACLE 3 — the census document itself: present, decodable, and EMPTY,
				// which is a different fact from absent.
				doc := frontierDocOf(t, st, tenant)
				if doc.PendingGroups == nil {
					t.Fatalf("the stored pending set decoded to a nil slice, not a known-empty collection")
				}
				if len(doc.PendingGroups) != 0 || doc.PendingGroupCount != 0 {
					t.Fatalf("pending groups = %+v (count %d), want the empty census", doc.PendingGroups, doc.PendingGroupCount)
				}

				// ORACLE 4 — the boundary does the one thing it exists to do. A readable
				// frontier makes the covered legacy wrappers refuse with
				// lifecycle_api_required; an UNREADABLE one refuses with
				// ledger_indeterminate, which is the pre-correction answer and is not a
				// working boundary.
				res, gerr := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
				if res.Allowed {
					t.Fatalf("a covered wrapper admitted across the boundary: %+v", res)
				}
				if attemptCode(gerr) != errCodeLifecycleAPIRequired {
					t.Fatalf("guard: code = %q, want lifecycle_api_required (err=%v)", attemptCode(gerr), gerr)
				}

				// ORACLE 5 — the replay is stable: same boundary, no second census, no
				// second audit, still exactly one row.
				auditBefore := countAuditAction(t, st, tenant, auditActionBeginActivation)
				replay, err := m.BeginLifecycleActivation(context.Background(), tenant, req)
				if err != nil {
					t.Fatalf("replay of the same Begin: %v (code %q)", err, attemptCode(err))
				}
				if replay.ID != begun.ID || replay.Version != begun.Version ||
					replay.FrontierDigest != begun.FrontierDigest {
					t.Fatalf("the replay produced a different boundary: %+v vs %+v", replay, begun)
				}
				if got := countAuditAction(t, st, tenant, auditActionBeginActivation); got != auditBefore {
					t.Fatalf("the replay appended %d begin audit events", got-auditBefore)
				}
				if n := len(scopeRows(t, st, tenant)); n != 1 {
					t.Fatalf("%d frontier rows after the replay, want 1", n)
				}
				if after := storedFrontierBody(t, st, tenant); after != body {
					t.Fatalf("the replay rewrote the census document")
				}
				if got := len(countReservations(t, st, tenant)); got != seeded {
					t.Fatalf("the whole sequence changed the ledger: %d reservation rows, want %d", got, seeded)
				}
			})
		}
	})
}

// TestAPendingCensusIsUnchangedByTheEmptyForm is the control positive. The
// correction touches only how a zero-length pending set is spelled, so the tenant
// that HAS a pending group must read back, guard and replay exactly as before —
// with its group still in the document and its digests untouched.
func TestAPendingCensusIsUnchangedByTheEmptyForm(t *testing.T) {
	f := newFrontierFixture(t)
	begun := f.begin(t)

	view, found, err := readScopeThrough(t, f.st, f.t1)
	if err != nil || !found {
		t.Fatalf("reading a non-empty boundary back: found=%v err=%v", found, err)
	}
	if view.PendingGroupCount != 1 || view.FrontierDigest != begun.FrontierDigest {
		t.Fatalf("durable view = %+v, want the one pending group Begin censused", view)
	}
	doc := frontierDocOf(t, f.st, f.t1)
	if len(doc.PendingGroups) != 1 || doc.PendingGroups[0].Handle != f.pending.String() {
		t.Fatalf("pending groups = %+v, want only %s", doc.PendingGroups, f.pending)
	}
	if doc.PendingGroups[0].ChildCount != 2 {
		t.Fatalf("the pending group covers %d children, want both", doc.PendingGroups[0].ChildCount)
	}
	if body := storedFrontierBody(t, f.st, f.t1); strings.Contains(body, `"pending_groups":[]`) {
		t.Fatalf("a populated census was written as empty: %s", body)
	}

	replay := f.begin(t)
	if replay.ID != begun.ID || replay.FrontierDigest != begun.FrontierDigest {
		t.Fatalf("the replay produced a different boundary: %+v vs %+v", replay, begun)
	}
	// T2 never crossed anything: a boundary is per tenant, empty census or not.
	if n := len(scopeRows(t, f.st, f.t2)); n != 0 {
		t.Fatalf("T2 acquired %d frontier rows", n)
	}
	res, gerr := f.m.ReserveBudget(context.Background(), f.t2, SpendDims{}, oneUSD)
	if gerr != nil || !res.Allowed {
		t.Fatalf("T2's ordinary reserve stopped working: allowed=%v err=%v", res.Allowed, gerr)
	}
}

// TestAStoredNullPendingSetIsStillRefused is the half of the contract the
// correction must NOT have bought: the reader was not made permissive.
//
// The document is edited back to the exact shape the defect produced — a JSON null
// where the pending set belongs — and every reader must still refuse it. A frontier
// row written by an older binary therefore stays rejected, which is the adjudicated
// answer; recovering one needs its own contract, not a lenient scanner.
func TestAStoredNullPendingSetIsStillRefused(t *testing.T) {
	m, st, tenant, _ := newLifecycleFin(t)
	req := LifecycleActivationRequest{Evidence: []EvidenceRef{labEvidence("quiescence")}}
	if _, err := m.BeginLifecycleActivation(context.Background(), tenant, req); err != nil {
		t.Fatalf("Begin over an empty census: %v", err)
	}
	if _, _, err := readScopeThrough(t, st, tenant); err != nil {
		t.Fatalf("the boundary must be readable before it is corrupted: %v", err)
	}

	// Nil marshals to `null`. This reproduces the stored bytes, it does not simulate
	// them: the edit goes through the same encoder the producer uses.
	mutateFrontierDocument(t, st, tenant, func(doc *jsonFrontier) { doc.PendingGroups = nil })
	body := storedFrontierBody(t, st, tenant)
	if !strings.Contains(body, `"pending_groups":null`) {
		t.Fatalf("the corruption did not land as a null: %s", body)
	}

	_, _, rerr := readScopeThrough(t, st, tenant)
	if attemptCode(rerr) != errCodeLedgerIndeterminate {
		t.Fatalf("frontier lookup: code = %q, want ledger_indeterminate (err=%v)", attemptCode(rerr), rerr)
	}
	res, gerr := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
	if res.Allowed {
		t.Fatalf("a covered wrapper admitted against an unreadable boundary: %+v", res)
	}
	if attemptCode(gerr) != errCodeLedgerIndeterminate {
		t.Fatalf("guard: code = %q, want ledger_indeterminate (err=%v)", attemptCode(gerr), gerr)
	}
	// The repaired producer does not repair the row: a Begin arriving at a document
	// it cannot read refuses rather than overwriting the boundary.
	if _, err := m.BeginLifecycleActivation(context.Background(), tenant, req); attemptCode(err) != errCodeLedgerIndeterminate {
		t.Fatalf("Begin over a null document: code = %q, want ledger_indeterminate (err=%v)", attemptCode(err), err)
	}
	if after := storedFrontierBody(t, st, tenant); after != body {
		t.Fatalf("a refused Begin rewrote the stored document")
	}
	if n := len(scopeRows(t, st, tenant)); n != 1 {
		t.Fatalf("%d frontier rows, want the one corrupted row and no replacement", n)
	}
}

// TestAnEmptyCensusIsNotAReasonToWrite: the canonical empty array is a
// SERIALIZATION, so none of the refusals that stop a boundary may have become
// weaker for the tenant whose census happens to be empty. Each seam below is the
// one the module already exposes, and each is checked for ZERO writes: no frontier
// row and no begin audit event.
func TestAnEmptyCensusIsNotAReasonToWrite(t *testing.T) {
	req := LifecycleActivationRequest{Evidence: []EvidenceRef{labEvidence("quiescence")}}

	t.Run("no current access to the tenant", func(t *testing.T) {
		m, st, tenant, v := newLifecycleFin(t)
		v.revoke(tenant)
		_, err := m.BeginLifecycleActivation(context.Background(), tenant, req)
		if err == nil {
			t.Fatalf("Begin succeeded for a component with no access to the tenant")
		}
		requireNoBoundaryWritten(t, st, tenant)
	})

	t.Run("authorized to read but not to cross", func(t *testing.T) {
		m, st, tenant, v := newLifecycleFin(t)
		// The lookup check still passes; only the transition is refused. Reaching this
		// refusal proves the census ran and still wrote nothing.
		v.mu.Lock()
		v.failOp = opBeginActivation
		v.mu.Unlock()
		if _, err := m.BeginLifecycleActivation(context.Background(), tenant, req); err == nil {
			t.Fatalf("Begin crossed a boundary the component may not cross")
		}
		ops := map[string]int{}
		for _, c := range v.seen() {
			ops[c.Operation]++
		}
		if ops[opQuery] == 0 || ops[opBeginActivation] == 0 {
			t.Fatalf("checks seen = %v, want both the query and the crossing", ops)
		}
		requireNoBoundaryWritten(t, st, tenant)
	})

	t.Run("an enumeration that never completes", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		forcePager(m, alwaysMorePager(nil))
		if code := attemptCode(mustFailBegin(t, m, tenant, req)); code != errCodeLedgerIncomplete {
			t.Fatalf("code = %q, want ledger_incomplete", code)
		}
		requireNoBoundaryWritten(t, st, tenant)
	})

	t.Run("an enumeration with no usable cursor", func(t *testing.T) {
		m, st, tenant, _ := newLifecycleFin(t)
		forcePager(m, noCursorPager(nil))
		if code := attemptCode(mustFailBegin(t, m, tenant, req)); code != errCodeLedgerIncomplete {
			t.Fatalf("code = %q, want ledger_incomplete", code)
		}
		requireNoBoundaryWritten(t, st, tenant)
	})
}

// -----------------------------------------------------------------------------
// Local helpers
// -----------------------------------------------------------------------------

// storedFrontierBody returns the raw census cell of the tenant's single frontier
// row. The defect was in the stored BYTES, so a case that only inspected the
// decoded struct would have been unable to see it — and, before the correction,
// would not have been able to decode it either.
func storedFrontierBody(t testing.TB, st store.Store, tenant model.TenantID) string {
	t.Helper()
	rows := scopeRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("%d frontier rows for %s, want 1", len(rows), tenant)
	}
	body, ok := textCell(rows[0], colScopeFrontier)
	if !ok {
		t.Fatalf("the frontier row carries no census document")
	}
	return body
}

// requireNoBoundaryWritten asserts a refused Begin left nothing behind at all.
func requireNoBoundaryWritten(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	if n := len(scopeRows(t, st, tenant)); n != 0 {
		t.Fatalf("a refused Begin wrote %d frontier rows", n)
	}
	if n := countAuditAction(t, st, tenant, auditActionBeginActivation); n != 0 {
		t.Fatalf("a refused Begin appended %d begin audit events", n)
	}
}

func mustFailBegin(t *testing.T, m *Module, tenant model.TenantID, req LifecycleActivationRequest) error {
	t.Helper()
	view, err := m.BeginLifecycleActivation(context.Background(), tenant, req)
	if err == nil {
		t.Fatalf("Begin succeeded where it had to refuse: %+v", view)
	}
	return err
}

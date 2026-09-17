// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// managed_stop_lineage_test.go (P2 / W2) — the run's authorization lineage: who
// writes it, who may see it, and how a legacy row acquires one.

// clearRunLineage makes a run look like every row created before W2: lawful in
// every other respect and carrying NO authorization workspace.
func (f *managedStopFixture) clearRunLineage(runRef string) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		rec[colRunAuthzWorkspaceID] = nil
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		f.t.Fatalf("clear the lineage of %s: %v", runRef, err)
	}
}

// runLineage reads the persisted authorization workspace of one run, raw.
func (f *managedStopFixture) runLineage(runRef string) (string, bool) {
	f.t.Helper()
	ctx := context.Background()
	var (
		value string
		null  bool
	)
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		value, null = rec.String(colRunAuthzWorkspaceID), rec.IsNull(colRunAuthzWorkspaceID)
		return nil
	}); err != nil {
		f.t.Fatalf("read the lineage of %s: %v", runRef, err)
	}
	return value, null
}

// claimFacts is the claim row's complete admission stamp plus its version.
type claimFacts struct {
	version  int64
	holder   string
	fence    int64
	state    string
	deadline string
}

func (f *managedStopFixture) claimFacts(sid string) claimFacts {
	f.t.Helper()
	ctx := context.Background()
	var out claimFacts
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		rec, found, err := findClaim(ctx, sc, sid)
		if err != nil {
			return err
		}
		if !found {
			return store.ErrNotFound
		}
		out = claimFacts{
			version: rec.Int(model.ColVersion), holder: rec.String(colHolder),
			fence: rec.Int(colFence), state: rec.String(colClaimState),
			deadline: rec.String(colLeaseExpires),
		}
		return nil
	}); err != nil {
		f.t.Fatalf("read claim %s: %v", sid, err)
	}
	return out
}

// TestRunLineageIsProducedOnlyByTheCreationMutation: the launch stamps it from
// the identity the run is claimed under, and no later lifecycle write reassigns
// it. That is what keeps the column a server fact rather than a moving target.
func TestRunLineageIsProducedOnlyByTheCreationMutation(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			dto, lr := f.launch("thread-lineage-produce")
			stamped, null := f.runLineage(dto.RunRef)
			if null || stamped != f.workspace.String() {
				t.Fatalf("lineage = %q null=%t, want the resolved workspace %s",
					stamped, null, f.workspace)
			}
			ctx := context.Background()

			// The legacy stop is a lifecycle write over the same row.
			if _, err := m2stop(ctx, f, dto.RunRef); err != nil {
				t.Fatalf("legacy stop: %v", err)
			}
			if got, null := f.runLineage(dto.RunRef); null || got != stamped {
				t.Fatalf("the stop reassigned the lineage: %q null=%t", got, null)
			}
			// And so is cleanup.
			if _, err := f.m.cleanupRun(ctx, f.tenant, dto.RunRef, "user:u1", model.ActorUser); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
			if got, null := f.runLineage(dto.RunRef); null || got != stamped {
				t.Fatalf("the cleanup reassigned the lineage: %q null=%t", got, null)
			}
			_ = lr
		})
	}
}

// m2stop runs the legacy operator stop, which W2 must leave behaving exactly as
// it did.
func m2stop(ctx context.Context, f *managedStopFixture, runRef string) (runDTO, error) {
	return f.m.stopRun(ctx, f.tenant, runRef, "user:u1", model.ActorUser)
}

// TestReadRunLaunchConfinesAndNeverRepairs is the confined reader's whole
// contract: a lawful row is presented, a row without lineage is CONCEALED rather
// than repaired, and a foreign workspace answers the same way.
func TestReadRunLaunchConfinesAndNeverRepairs(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			dto, lr := f.launch("thread-readlaunch")
			ctx := context.Background()

			snap, err := f.m.ReadRunLaunch(ctx, f.tenant, f.workspace, dto.RunRef)
			if err != nil {
				t.Fatalf("lawful read: %v", err)
			}
			if snap.RunRef != dto.RunRef || snap.RuntimeLaunchID != lr.launchID ||
				snap.Lifecycle != stateRunning {
				t.Fatalf("snapshot = %+v", snap)
			}

			if _, err := f.m.ReadRunLaunch(ctx, f.tenant, model.NewID(), dto.RunRef); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("foreign workspace = %v, want concealment", err)
			}

			f.clearRunLineage(dto.RunRef)
			if _, err := f.m.ReadRunLaunch(ctx, f.tenant, f.workspace, dto.RunRef); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("unset lineage = %v, want concealment", err)
			}
			// ⛔ AND THE READ REPAIRED NOTHING. A reader that quietly filled the column
			// would turn a concealed row into a visible one as a side effect of being
			// looked at, which is the request-side repair this surface forbids.
			if _, null := f.runLineage(dto.RunRef); !null {
				t.Fatal("ReadRunLaunch wrote a lineage onto a concealed row")
			}
		})
	}
}

// TestManagedStopConcealsARunWithoutLineage: the same concealment, on the Stop
// path. A legacy row is not stoppable through the managed surface until the
// bounded repair gives it a lawful lineage — and it is not repaired on request.
func TestManagedStopConcealsARunWithoutLineage(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("nolineage@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-nolineage")
			f.clearRunLineage(dto.RunRef)

			res, err := f.call(f.request(op, dto, lr.launchID, "nolineage-1"))
			f.refuseWithNoJournalRow(res, err, "nolineage-1", ManagedStopConcealed)
			if _, null := f.runLineage(dto.RunRef); !null {
				t.Fatal("the refused Stop repaired the lineage on its way out")
			}
			if !processRunning(lr.proc.PID()) {
				t.Fatal("a concealed run's child was ended")
			}
		})
	}
}

// TestRepairRunLineageIsBoundedAndExact exercises the W3 seam: one pass repairs
// what it can, reports what it cannot, advances its cursor strictly and reports
// exhaustion from the captured bound rather than from an empty page.
func TestRepairRunLineageIsBoundedAndExact(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			ctx := context.Background()
			var refs []string
			for i := 0; i < 3; i++ {
				dto, _ := f.launch("thread-repair-" + string(rune('a'+i)))
				f.clearRunLineage(dto.RunRef)
				refs = append(refs, dto.RunRef)
			}
			// One of them names no session at all: unresolvable, and it must stay
			// NULL rather than acquire a default.
			f.setRunClaimSID(refs[2], "")

			res, err := f.m.RepairRunLineage(ctx, f.tenant, RunLineageRepairCursor{})
			if err != nil {
				t.Fatalf("repair: %v", err)
			}
			if res.Scanned != 3 {
				t.Fatalf("scanned = %d, want the three candidates", res.Scanned)
			}
			if res.Repaired != 2 || res.Unresolved != 1 || res.Conflicts != 0 {
				t.Fatalf("repaired=%d unresolved=%d conflicts=%d, want 2/1/0",
					res.Repaired, res.Unresolved, res.Conflicts)
			}
			if !res.Exhausted || res.Next != (RunLineageRepairCursor{}) {
				t.Fatalf("a pass that reached the captured bound = %+v, want exhausted with a fresh cursor", res)
			}
			for _, ref := range refs[:2] {
				if got, null := f.runLineage(ref); null || got != f.workspace.String() {
					t.Fatalf("%s lineage = %q null=%t, want the resolved workspace", ref, got, null)
				}
			}
			if _, null := f.runLineage(refs[2]); !null {
				t.Fatal("the unresolvable run acquired a workspace it cannot justify")
			}

			// A second pass finds only the unresolvable row and says so without
			// moving anything. Traversal completed; resolution did not.
			again, err := f.m.RepairRunLineage(ctx, f.tenant, RunLineageRepairCursor{})
			if err != nil {
				t.Fatalf("second pass: %v", err)
			}
			if again.Scanned != 1 || again.Repaired != 0 || again.Unresolved != 1 || !again.Exhausted {
				t.Fatalf("second pass = %+v, want the one unresolvable row re-reported", again)
			}
			if maxRunLineageRepairPage != 256 {
				t.Fatalf("page ceiling = %d, want 256", maxRunLineageRepairPage)
			}
		})
	}
}

// TestRepairRunLineagePagesUnderTheCapturedBound is the cursor contract on its
// own: at most 256 rows a page, a strictly advancing stable id, and an upper bound
// captured ONCE per pass — so a row created while the pass runs belongs to the
// next pass, and a continuation that lost its bound is refused.
func TestRepairRunLineagePagesUnderTheCapturedBound(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			ctx := context.Background()
			first := f.insertLegacyRuns(257)

			page1, err := f.m.RepairRunLineage(ctx, f.tenant, RunLineageRepairCursor{})
			if err != nil {
				t.Fatalf("page 1: %v", err)
			}
			if page1.Scanned != maxRunLineageRepairPage || page1.Exhausted {
				t.Fatalf("page 1 = %+v, want a full, unexhausted page", page1)
			}
			if page1.Unresolved != maxRunLineageRepairPage || page1.Repaired != 0 || page1.Conflicts != 0 {
				t.Fatalf("page 1 counters = %+v, want every sessionless row unresolved", page1)
			}
			upper := page1.Next.UpperBound
			if upper.IsZero() || page1.Next.After.IsZero() || page1.Next.After >= upper {
				t.Fatalf("page 1 cursor = %+v, want an advanced cursor below its captured bound", page1.Next)
			}
			if first[len(first)-1] != upper {
				t.Fatalf("captured bound = %s, want the highest row present at capture %s",
					upper, first[len(first)-1])
			}

			// A row created WHILE the pass runs. UUIDv7 ids order by time, so a short
			// pause guarantees it sorts after the captured bound.
			time.Sleep(5 * time.Millisecond)
			late := f.insertLegacyRuns(1)[0]
			if late <= upper {
				t.Fatalf("late row %s does not sort after the captured bound %s", late, upper)
			}

			page2, err := f.m.RepairRunLineage(ctx, f.tenant, page1.Next)
			if err != nil {
				t.Fatalf("page 2: %v", err)
			}
			if page2.Scanned != 1 || !page2.Exhausted || page2.Next != (RunLineageRepairCursor{}) {
				t.Fatalf("page 2 = %+v, want the one remaining row and exhaustion: the late row "+
					"belongs to the next pass", page2)
			}

			// The next pass captures a new bound, and it is the late row.
			pass2, err := f.m.RepairRunLineage(ctx, f.tenant, RunLineageRepairCursor{})
			if err != nil {
				t.Fatalf("pass 2: %v", err)
			}
			if pass2.Next.UpperBound != late || pass2.Scanned != maxRunLineageRepairPage {
				t.Fatalf("pass 2 = %+v, want a fresh bound at the late row %s", pass2, late)
			}

			// A continuation without its captured bound would silently re-capture one.
			if _, err := f.m.RepairRunLineage(ctx, f.tenant,
				RunLineageRepairCursor{After: page1.Next.After}); !errors.Is(err, errRunLineageRepairCursor) {
				t.Fatalf("bound-less continuation = %v, want refusal", err)
			}
			// A cursor already at its bound is exhausted without reading anything.
			done, err := f.m.RepairRunLineage(ctx, f.tenant,
				RunLineageRepairCursor{After: upper, UpperBound: upper})
			if err != nil || !done.Exhausted || done.Scanned != 0 {
				t.Fatalf("cursor at its bound = %+v, %v", done, err)
			}
		})
	}
}

// TestRepairRunLineageRefusesWithoutATenant is the deny-closed input, and a
// canceled maintenance lifetime writes nothing.
func TestRepairRunLineageRefusesWithoutATenant(t *testing.T) {
	f := newManagedStopFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	if _, err := f.m.RepairRunLineage(context.Background(), "", RunLineageRepairCursor{}); !errors.Is(err, store.ErrNoTenant) {
		t.Fatalf("err = %v, want ErrNoTenant", err)
	}
	dto, _ := f.launch("thread-repair-canceled")
	f.clearRunLineage(dto.RunRef)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := f.m.RepairRunLineage(ctx, f.tenant, RunLineageRepairCursor{})
	if err == nil {
		t.Fatalf("a canceled pass reported %+v", res)
	}
	if res.Repaired != 0 || res.Next != (RunLineageRepairCursor{}) {
		t.Fatalf("a canceled pass reported progress: %+v", res)
	}
	if _, null := f.runLineage(dto.RunRef); !null {
		t.Fatal("a canceled pass wrote a lineage")
	}
}

// setRunClaimSID rewrites one run's admission SID, raw.
func (f *managedStopFixture) setRunClaimSID(runRef, sid string) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		if sid == "" {
			rec[colRunClaimSID] = nil
		} else {
			rec[colRunClaimSID] = sid
		}
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		f.t.Fatalf("rewrite the admission SID of %s: %v", runRef, err)
	}
}

// insertLegacyRuns writes n terminal run rows exactly as a pre-W2 database holds
// them: no admission SID and no authorization lineage. It returns their ids in
// ascending order.
func (f *managedStopFixture) insertLegacyRuns(n int) []model.ID {
	f.t.Helper()
	ctx := context.Background()
	ids := make([]model.ID, 0, n)
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			created, err := repo.Create(ctx, model.Record{
				colRunRef:         "legacy-" + model.NewID().String(),
				colTransport:      string(TransportStreamJSON),
				colPermissionMode: "",
				colIsolation:      string(IsolationNative),
				colState:          stateStopped,
				colLastEventSeq:   int64(0),
			})
			if err != nil {
				return err
			}
			ids = append(ids, model.ID(created.String(model.ColID)))
		}
		return nil
	}); err != nil {
		f.t.Fatalf("insert %d legacy runs: %v", n, err)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			f.t.Fatalf("legacy run ids are not ascending: %s then %s", ids[i-1], ids[i])
		}
	}
	return ids
}

// TestManagedStopAdmissionTouchesTheQualifiedClaimAndNothingElse is the qualified
// TouchClaim (correction 2 §5.4) seen from the module, measured across the
// ADMISSION TRANSACTION ALONE.
//
// ⛔ IT CALLS PHASE D DIRECTLY, and that is the only way to measure this. A whole
// StopManagedRun also releases the claim and finalizes the run, so the claim row
// moves several times for reasons that have nothing to do with the touch — a
// first version of this test asserted +1 across the whole call and measured +3.
// What the contract actually says is about ONE transaction: the qualified claim
// advances by exactly one version and NO other column is assigned.
func TestManagedStopAdmissionTouchesTheQualifiedClaimAndNothingElse(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("touch@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-touch")
			req := f.request(op, dto, lr.launchID, "touch-1")
			before := f.claimFacts(lr.claim.SID)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			ports, err := f.m.managedStopReady()
			if err != nil {
				t.Fatalf("ports: %v", err)
			}
			actx, release, refusal := f.m.managedStopAdmissionContext(ctx, ports)
			if refusal.Outcome != "" {
				t.Fatalf("admission context refused: %+v", refusal)
			}
			defer release()
			authority, res, err := f.m.authorizeManagedStop(actx, ctx, f.tenant, req.Question, req.ManagedStopRequest, ports)
			if err != nil || res.Outcome != "" {
				t.Fatalf("authorize = %+v, %v", res, err)
			}
			semantic := managedStopSemanticDigest(f.tenant, req.Question, req.ManagedStopRequest)
			operationRef := managedStopSurfacePrefix + req.OperationID
			admission, err := f.m.admitManagedStop(actx, f.tenant, req.ManagedStopRequest, authority, lr, operationRef, semantic)
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if admission.result.Outcome != ManagedStopStopped {
				t.Fatalf("admission = %+v", admission.result)
			}
			if admission.epoch != f.leaderEpoch() {
				t.Fatalf("claim epoch = %d, want the elector's own %d",
					admission.epoch, f.leaderEpoch())
			}
			after := f.claimFacts(lr.claim.SID)
			if after.version != before.version+1 {
				t.Fatalf("claim version %d -> %d, want exactly one touch",
					before.version, after.version)
			}
			if after.holder != before.holder || after.fence != before.fence ||
				after.state != before.state || after.deadline != before.deadline {
				t.Fatalf("the touch moved an admission coordinate: %+v -> %+v", before, after)
			}
			// The identity is now CLAIMED and unsettled, which is the shape a crash
			// between admission and effect leaves behind. It is never re-dispatched.
			row, found := f.journal("touch-1")
			if !found || row.State != model.EvidenceOpClaimed {
				t.Fatalf("journal row = %+v found=%t, want a claimed row", row, found)
			}
			if row.OutcomeEvidenceRef != "" {
				t.Fatalf("an unsettled row carries an outcome anchor: %+v", row)
			}
		})
	}
}

// leaderEpoch reads the store elector's own durable fence.
func (f *managedStopFixture) leaderEpoch() uint64 {
	f.t.Helper()
	fencer, ok := f.st.Leader().(store.EpochFencer)
	if !ok {
		f.t.Fatalf("the elector %T carries no durable fence", f.st.Leader())
	}
	epoch, err := fencer.FencedEpoch(context.Background())
	if err != nil {
		f.t.Fatalf("fenced epoch: %v", err)
	}
	return epoch
}

// TestManagedStopRefusesAMovedGenerationInTransaction is D4c: the in-memory
// handle still matches, and the DURABLE row has moved. The re-read inside the
// admission transaction is what catches it, and nothing is written.
func TestManagedStopRefusesAMovedGenerationInTransaction(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("gen@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-generation")
			ctx := context.Background()
			if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				rec, err := findRunRec(ctx, repo, dto.RunRef)
				if err != nil {
					return err
				}
				rec[colRuntimeLaunchID] = model.NewID().String()
				_, err = repo.Update(ctx, rec)
				return err
			}); err != nil {
				t.Fatalf("move the durable generation: %v", err)
			}

			res, err := f.call(f.request(op, dto, lr.launchID, "generation-1"))
			f.refuseWithNoJournalRow(res, err, "generation-1", ManagedStopLaunchSuperseded)
			if !processRunning(lr.proc.PID()) {
				t.Fatal("a superseded generation still ended the child")
			}
		})
	}
}

// TestManagedStopRefusesAMovedClaimInTransaction is D4b: the run's admission
// stamp no longer names the claim this launch was started under.
func TestManagedStopRefusesAMovedClaimInTransaction(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("claimmoved@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-claim-moved")
			ctx := context.Background()
			if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				rec, err := findRunRec(ctx, repo, dto.RunRef)
				if err != nil {
					return err
				}
				rec[colRunClaimSID] = "osn_" + model.NewID().String()
				_, err = repo.Update(ctx, rec)
				return err
			}); err != nil {
				t.Fatalf("move the admission stamp: %v", err)
			}

			res, err := f.call(f.request(op, dto, lr.launchID, "claimmoved-1"))
			f.refuseWithNoJournalRow(res, err, "claimmoved-1", ManagedStopClaimLost)
			if !processRunning(lr.proc.PID()) {
				t.Fatal("a lost claim still ended the child")
			}
		})
	}
}

// TestManagedStopActionsAreDeclared: an action the module never declared is
// deny-closed at the engine, so declaring them IS the capability.
func TestManagedStopActionsAreDeclared(t *testing.T) {
	got := New().Actions()
	want := map[auth.CedarAction]bool{actionRunStop: false, actionLeaseStop: false}
	for _, a := range got {
		if _, ok := want[a]; !ok {
			t.Fatalf("undeclared action %q", a)
		}
		want[a] = true
	}
	for action, seen := range want {
		if !seen {
			t.Fatalf("action %q is not declared", action)
		}
	}
	if managedRunStopMetadata.MinimumAAL != auth.AAL3 ||
		managedStopLeaseMetadata.MinimumAAL != auth.AAL3 {
		t.Fatal("a terminal, irreversible control must require the verified ceremony")
	}
	if managedRunStopMetadata.CedarAction != string(actionRunStop) ||
		managedStopLeaseMetadata.CedarAction != string(actionLeaseStop) {
		t.Fatal("the route metadata names an action this module does not declare")
	}
	// Both floors are the ratified editor; the lease question's barrier is its
	// sessions:lease:admin permission, proven by the bound editor refusal.
	if managedRunStopMetadata.RBACMinimumRole != auth.RoleEditor ||
		managedStopLeaseMetadata.RBACMinimumRole != auth.RoleEditor {
		t.Fatal("a route floor differs from the ratified construction")
	}
}

// TestRepairRunLineageRecordsAStaleVersionAsAConflict proves the repair's write is
// an EXACT version compare-and-swap on a real store. A live-run writer advances the
// row after the pass selected it and before the pass writes: the pass must count a
// conflict, repair nothing, keep the row NULL, and still commit its transaction.
func TestRepairRunLineageRecordsAStaleVersionAsAConflict(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			dto, _ := f.launch("thread-repair-conflict")
			f.clearRunLineage(dto.RunRef)
			ctx := context.Background()
			var out RunLineageRepairResult
			if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				selected, err := findRunRec(ctx, repo, dto.RunRef)
				if err != nil {
					return err
				}
				concurrent := model.Record{}
				for k, v := range selected {
					concurrent[k] = v
				}
				concurrent[colLastActivityAt] = model.NewTimestamp(time.Now()).String()
				if _, err := repo.Update(ctx, concurrent); err != nil {
					return err
				}
				return f.m.repairOneRunLineage(ctx, sc, repo, selected, &out)
			}); err != nil {
				t.Fatalf("the transaction carrying a counted conflict did not commit: %v", err)
			}
			if out.Conflicts != 1 || out.Repaired != 0 || out.Unresolved != 0 {
				t.Fatalf("counters = %+v, want exactly one conflict", out)
			}
			if got, null := f.runLineage(dto.RunRef); !null {
				t.Fatalf("a lost compare-and-swap still wrote lineage %q", got)
			}
		})
	}
}

// TestManagedStopEpochFenceFailureLeavesTheClaimUnsettled is Phase F's settlement
// rule on a real claimed row: an F3 epoch-fence refusal crosses nothing and may
// record nothing, while an F4 refusal after a passing fence is recorded.
func TestManagedStopEpochFenceFailureLeavesTheClaimUnsettled(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newManagedStopFixture(t, be.config(t))
			op := f.operator("fence@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-fence")
			req := f.request(op, dto, lr.launchID, "fence-1")

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			ports, err := f.m.managedStopReady()
			if err != nil {
				t.Fatalf("ports: %v", err)
			}
			actx, release, refusal := f.m.managedStopAdmissionContext(ctx, ports)
			if refusal.Outcome != "" {
				t.Fatalf("admission context refused: %+v", refusal)
			}
			defer release()
			authority, res, err := f.m.authorizeManagedStop(actx, ctx, f.tenant, req.Question, req.ManagedStopRequest, ports)
			if err != nil || res.Outcome != "" {
				t.Fatalf("authorize = %+v, %v", res, err)
			}
			admission, err := f.m.admitManagedStop(actx, f.tenant, req.ManagedStopRequest, authority, lr,
				managedStopSurfacePrefix+req.OperationID,
				managedStopSemanticDigest(f.tenant, req.Question, req.ManagedStopRequest))
			if err != nil || admission.result.Outcome != ManagedStopStopped {
				t.Fatalf("admission = %+v, %v", admission.result, err)
			}

			fenced, settle := f.m.managedStopPreEffect(actx, f.tenant, req.ManagedStopRequest, authority, lr,
				ports, admission.epoch+1)
			if fenced.Outcome != ManagedStopAuthorityUnavailable || settle {
				t.Fatalf("F3 under a foreign epoch = %+v settle=%t, want an unsettled refusal", fenced, settle)
			}
			if row, found := f.journal("fence-1"); !found || row.State != model.EvidenceOpClaimed {
				t.Fatalf("journal after F3 = %+v found=%t, want the row still claimed", row, found)
			}
			if !processRunning(lr.proc.PID()) {
				t.Fatal("an F3 refusal ended the child")
			}

			passing, settle := f.m.managedStopPreEffect(actx, f.tenant, req.ManagedStopRequest, authority, lr,
				ports, admission.epoch)
			if passing.Outcome != "" || settle {
				t.Fatalf("pre-effect under the committed epoch = %+v settle=%t, want a pass with nothing to record",
					passing, settle)
			}
			moved := req.ManagedStopRequest
			moved.ExpectedLaunch = model.NewID()
			f4, settle := f.m.managedStopPreEffect(actx, f.tenant, moved, authority, lr, ports, admission.epoch)
			if f4.Outcome != ManagedStopLaunchSuperseded || !settle {
				t.Fatalf("F4 after a passing fence = %+v settle=%t, want a recorded refusal", f4, settle)
			}
		})
	}
}

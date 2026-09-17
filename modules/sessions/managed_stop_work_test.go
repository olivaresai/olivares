// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// managed_stop_work_test.go (P2 / W2) — the THIRD question and the six work facts.
//
// A work-bound run is not stoppable by run authority alone: ending it ends the
// authority a work lease holds, and that is a different barrier. These cases prove
// the module asks for it, proves the complete binding before the effect, and
// refuses every partial or stale observation without burning an identity.

// managedStopWorkFixture is a composed module with the K1 work ports and a
// process runner that records rather than spawns. The store, the authority, the
// work kernel and the journal are all real; only the child is not, because a
// work launch binds its lease BEFORE it spawns and that ordering — not the
// process — is what these cases measure.
func newManagedStopWorkFixture(t *testing.T, cfg store.Config) (*managedStopFixture, *fakeRunner) {
	t.Helper()
	runner := &fakeRunner{}
	f := newManagedStopFixtureWith(t, cfg,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
		WithProductVersion("test"), WithStopWaitDelay(time.Second),
	)
	return f, runner
}

// boundRun readies a work item, launches a run bound to its lease, and returns
// everything a lawful managed Stop has to present.
func (f *managedStopFixture) boundRun(t *testing.T) (ManagedRunRef, model.ID, model.ID, WorkLease) {
	t.Helper()
	ctx := context.Background()
	itemID, workspace, agentRef := f.readyBoundWorkItem(t)
	managed, err := f.m.LaunchForWork(ctx, f.tenant, workLaunchSpec(itemID, agentRef))
	if err != nil {
		t.Fatalf("LaunchForWork: %v", err)
	}
	var lease WorkLease
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		rec, found, err := findWorkLease(ctx, sc, itemID)
		if err != nil || !found {
			t.Fatalf("work lease unavailable: %v found=%t", err, found)
		}
		lease, err = workLeaseFromRecord(rec, time.Now(), VerdictClean, "")
		return err
	}); err != nil {
		t.Fatalf("read the work lease: %v", err)
	}
	return managed, itemID, workspace, lease
}

// readyBoundWorkItem is readyWorkLaunchItem with a slug this file can call more
// than once in the same millisecond.
//
// ⛔ WHY IT IS NOT THE SHARED HELPER. That helper derives its workspace slug from
// the first eight characters of a UUIDv7, which are a millisecond timestamp: two
// calls inside one test collide on the workspace unique index, and the failure
// reads as "version conflict" rather than "two workspaces wanted the same name".
// Measured here with six subtests in a row.
func (f *managedStopFixture) readyBoundWorkItem(t *testing.T) (model.ID, model.ID, string) {
	t.Helper()
	f.boundSeq++
	slug := "w2-bound-" + strconv.Itoa(f.boundSeq) + "-" + model.NewID().String()[:8]
	ctx := context.Background()
	var workspace, ownerID model.ID
	ownerExternal := "agent:w2-bound-" + model.NewID().String()
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "W2 bound " + slug, Slug: slug, Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		workspace = ws.ID
		identity, err := sc.Identities().Create(ctx, model.Identity{
			Name: "W2 bound executor", Kind: "agent_nhi",
			ExternalID: ownerExternal, Provider: "test",
		})
		if err != nil {
			return err
		}
		ownerID = identity.ID
		_, err = sc.Agents().Create(ctx, model.Agent{
			Name: "W2 bound executor", Kind: "test", ExternalID: ownerExternal,
			Status: model.StatusActive, IdentityID: ownerID, WorkspaceID: workspace,
		})
		return err
	}); err != nil {
		t.Fatalf("create the work workspace and its agent owner: %v", err)
	}
	principal := WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "w2-test-setup",
		Actor: "system:w2-test-setup", Admin: true,
	}
	created, err := f.m.Apply(ctx, f.tenant, principal, WorkCommand{
		Command: "item.create", WorkspaceID: workspace,
		WorkKind: "implementation", Title: "Stop a managed work-bound session",
		BriefMD:     "Run the exact WorkItem generation through the supervised runtime.",
		ContextRefs: []ContextRef{}, Priority: "p1",
		OwnerKind: "agent", OwnerRef: ownerID.String(),
		ProvenanceKind: "workflow", ProvenanceRef: "test:w2-managed-stop",
		Acceptance: []AcceptanceInput{{
			Key: "runtime", Ordinal: 0,
			Statement: "The managed runtime starts under the durable lease.", Required: true,
		}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
		CommandScope: "POST /work-items",
	})
	if err != nil {
		t.Fatalf("create the work item: %v", err)
	}
	ready, err := f.m.Apply(ctx, f.tenant, principal, WorkCommand{
		Command: "item.ready", WorkItemID: created.ResultID,
		ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
		HTTPMethod: http.MethodPost,
	})
	if err != nil || ready.Status != "ready" {
		t.Fatalf("ready the work item = %#v, %v", ready, err)
	}
	return created.ResultID, workspace, ownerExternal
}

// boundRequest is the complete, lawful managed-stop request for a work-bound run.
func (f *managedStopFixture) boundRequest(
	p auth.Principal, managed ManagedRunRef, workspace model.ID, lease WorkLease, opID string,
) managedStopCall {
	lr, ok := f.m.rt.getLive(f.tenant, managed.RunRef)
	if !ok {
		f.t.Fatalf("no live handle for %s", managed.RunRef)
	}
	return managedStopCall{
		Question: f.cockpitQuestion(managed.RunRef, workspace),
		ManagedStopRequest: ManagedStopRequest{
			Principal:      p,
			RunRef:         managed.RunRef,
			ExpectedLaunch: lr.launchID,
			OperationID:    opID,
			Reason:         "operator stop of a work-bound run",
			Work: &ManagedStopWork{
				WorkItemID:     managed.WorkItemID,
				LeaseFence:     managed.WorkLeaseFence,
				OwnerEpoch:     managed.OwnerEpoch,
				LeaseExpiresAt: lease.ExpiresAt,
				HolderSID:      lease.HolderSID,
				HolderRunRef:   lease.HolderRunRef,
			},
		},
	}
}

// TestManagedStopBoundRunAsksTheLeaseQuestion is the lawful bound positive: the
// run's own workspace is the work workspace, all six facts match, and the stop
// records a settlement.
func TestManagedStopBoundRunAsksTheLeaseQuestion(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, _ := newManagedStopWorkFixture(t, be.config(t))
			managed, itemID, workspace, lease := f.boundRun(t)
			// ⛔ THE OPERATOR IS RECONSTRUCTED AFTER THE WORK SETUP, and that order is
			// a measured requirement rather than tidiness. Creating the work
			// workspace, its agent owner and the item MOVES the directory epoch, and
			// the directory authority barrier refuses a bundle pinned to the previous
			// generation — correctly: authority observed before a directory change is
			// stale. Resolving first produced authority_unavailable at D2 with nothing
			// written, which is the right answer to the wrong question.
			op := f.operator("bound@w2.test", auth.RoleAdmin, true, 2*time.Minute)

			// The run's OWN lineage is the work workspace, resolved from the managed
			// SID's identity. That is what makes the two questions answerable against
			// the same boundary without the caller naming either one.
			if got, null := f.runLineage(managed.RunRef); null || got != workspace.String() {
				t.Fatalf("bound run lineage = %q null=%t, want the work workspace %s",
					got, null, workspace)
			}
			req := f.boundRequest(op, managed, workspace, lease, "bound-1")
			res, err := f.call(req)
			if err != nil {
				t.Fatalf("StopManagedRun: %v (%+v)", err, res)
			}
			if res.Outcome != ManagedStopStopped {
				t.Fatalf("outcome = %q (%s), want stopped", res.Outcome, res.Detail)
			}
			if !res.Attempted || res.Settlement != model.EvidenceOpCompleted {
				t.Fatalf("bound stop = %+v", res)
			}
			row, found := f.journal("bound-1")
			if !found || row.State != model.EvidenceOpCompleted {
				t.Fatalf("journal row = %+v found=%t", row, found)
			}
			_ = itemID
		})
	}
}

// TestManagedStopBoundRunRefusals: every partial or stale work observation is a
// distinct refusal, and none of them burns a journal identity.
func TestManagedStopBoundRunRefusals(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, _ := newManagedStopWorkFixture(t, be.config(t))

			t.Run("work_lease_required", func(t *testing.T) {
				managed, _, workspace, lease := f.boundRun(t)
				op := f.operator("bound-required@w2.test", auth.RoleAdmin, true, 2*time.Minute)
				req := f.boundRequest(op, managed, workspace, lease, "bound-required")
				req.Work = nil
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "bound-required", ManagedStopWorkLeaseRequired)
			})

			t.Run("work_lease_stale_item", func(t *testing.T) {
				managed, _, workspace, lease := f.boundRun(t)
				op := f.operator("bound-item@w2.test", auth.RoleAdmin, true, 2*time.Minute)
				req := f.boundRequest(op, managed, workspace, lease, "bound-item")
				req.Work.WorkItemID = model.NewID()
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "bound-item", ManagedStopWorkLeaseStale)
			})

			t.Run("work_lease_stale_fence", func(t *testing.T) {
				managed, _, workspace, lease := f.boundRun(t)
				op := f.operator("bound-fence@w2.test", auth.RoleAdmin, true, 2*time.Minute)
				req := f.boundRequest(op, managed, workspace, lease, "bound-fence")
				req.Work.LeaseFence = managed.WorkLeaseFence + 1
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "bound-fence", ManagedStopWorkLeaseStale)
			})

			t.Run("work_lease_stale_owner_epoch", func(t *testing.T) {
				managed, _, workspace, lease := f.boundRun(t)
				op := f.operator("bound-epoch@w2.test", auth.RoleAdmin, true, 2*time.Minute)
				req := f.boundRequest(op, managed, workspace, lease, "bound-epoch")
				req.Work.OwnerEpoch = managed.OwnerEpoch + 1
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "bound-epoch", ManagedStopWorkLeaseStale)
			})

			t.Run("work_lease_stale_expiry", func(t *testing.T) {
				// A renewal after the caller observed the lease makes the observation
				// stale, which is the whole reason the expiry travels with the request.
				managed, _, workspace, lease := f.boundRun(t)
				op := f.operator("bound-expiry@w2.test", auth.RoleAdmin, true, 2*time.Minute)
				req := f.boundRequest(op, managed, workspace, lease, "bound-expiry")
				req.Work.LeaseExpiresAt = "2026-01-01T00:00:00Z"
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "bound-expiry", ManagedStopWorkLeaseStale)
			})

			t.Run("work_lease_stale_holder", func(t *testing.T) {
				managed, _, workspace, lease := f.boundRun(t)
				op := f.operator("bound-holder@w2.test", auth.RoleAdmin, true, 2*time.Minute)
				req := f.boundRequest(op, managed, workspace, lease, "bound-holder")
				req.Work.HolderSID = "osn_" + model.NewID().String()
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "bound-holder", ManagedStopWorkLeaseStale)
			})

			t.Run("work_lease_stale_holder_run_ref", func(t *testing.T) {
				// The same SID holding the lease for a SIBLING run: the run ref is its own
				// fact and is compared on its own.
				managed, _, workspace, lease := f.boundRun(t)
				op := f.operator("bound-runref@w2.test", auth.RoleAdmin, true, 2*time.Minute)
				req := f.boundRequest(op, managed, workspace, lease, "bound-runref")
				req.Work.HolderRunRef = "run-" + model.NewID().String()
				res, err := f.call(req)
				f.refuseWithNoJournalRow(res, err, "bound-runref", ManagedStopWorkLeaseStale)
			})

			t.Run("incomplete_binding_is_a_caller_error", func(t *testing.T) {
				managed, _, workspace, lease := f.boundRun(t)
				op := f.operator("bound-partial@w2.test", auth.RoleAdmin, true, 2*time.Minute)
				req := f.boundRequest(op, managed, workspace, lease, "bound-partial")
				req.Work.HolderRunRef = ""
				res, err := f.m.StopManagedRun(context.Background(), f.tenant, req.Question, req.ManagedStopRequest)
				if err == nil {
					t.Fatal("a partial work binding was accepted")
				}
				if res.Outcome != ManagedStopConcealed {
					t.Fatalf("outcome = %q", res.Outcome)
				}
				if _, found := f.journal("bound-partial"); found {
					t.Fatal("a malformed request burned a journal identity")
				}
			})
		})
	}
}

// plainRun launches an UNBOUND run through the ordinary runtime path. Its session
// identity is minted without a workspace, which the identity owner reads as the
// tenant default.
func (f *managedStopFixture) plainRun(t *testing.T, agentRef string) (runDTO, *liveRun) {
	t.Helper()
	dto, err := f.m.createRun(context.Background(), f.tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: agentRef, ActorKind: model.ActorAgent, AgentRef: agentRef,
	})
	if err != nil {
		t.Fatalf("launch %s: %v", agentRef, err)
	}
	lr, ok := f.m.rt.getLive(f.tenant, dto.RunRef)
	if !ok {
		t.Fatalf("launch %s produced no live handle", agentRef)
	}
	return dto, lr
}

// createWorkspace creates one more core workspace in the tenant.
func (f *managedStopFixture) createWorkspace(t *testing.T, name string) model.ID {
	t.Helper()
	f.boundSeq++
	slug := name + "-" + strconv.Itoa(f.boundSeq) + "-" + model.NewID().String()[:8]
	var id model.ID
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{
			Name: name, Slug: slug, Status: model.StatusActive,
		})
		id = ws.ID
		return err
	}); err != nil {
		t.Fatalf("create workspace %s: %v", name, err)
	}
	return id
}

// mergeIdentity marks one canonical session identity as merged into another, raw,
// which is the state a legacy row can carry.
func (f *managedStopFixture) mergeIdentity(t *testing.T, sid, into string) {
	t.Helper()
	ctx := context.Background()
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		rec, found, err := findIdentity(ctx, sc, sid)
		if err != nil {
			return err
		}
		if !found {
			return store.ErrNotFound
		}
		repo, err := sc.Ext(identityKind)
		if err != nil {
			return err
		}
		rec[colMergedInto] = into
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("merge identity %s: %v", sid, err)
	}
}

// tryMoveWorkItem attempts to rewrite a work item's stored workspace, raw.
func (f *managedStopFixture) tryMoveWorkItem(itemID, workspace model.ID) error {
	ctx := context.Background()
	return f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, itemID)
		if err != nil {
			return err
		}
		rec[colWorkWorkspaceID] = workspace.String()
		_, err = repo.Update(ctx, rec)
		return err
	})
}

// renewWorkLease renews a bound run's work lease through the real lease.renew
// command, as the run's own purpose-restricted work-session principal: the one
// party the kernel lets renew it. It returns the renewed durable expiry.
func (f *managedStopFixture) renewWorkLease(t *testing.T, lease WorkLease, claimFence int64) string {
	t.Helper()
	ctx := context.Background()
	var itemVersion int64
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := repo.Get(ctx, lease.WorkItemID)
		itemVersion = item.Int(model.ColVersion)
		return err
	}); err != nil {
		t.Fatalf("read the work item version: %v", err)
	}
	holder := WorkPrincipal{
		ActorKind: model.ActorAgent, ActorRef: lease.HolderAgentRef, Actor: lease.HolderAgentRef,
		SessionID: lease.HolderSID, SessionRunRef: lease.HolderRunRef, SessionFence: claimFence,
		PurposeRestricted: true,
	}
	if _, err := f.m.Apply(ctx, f.tenant, holder, WorkCommand{
		Command: "lease.renew", WorkItemID: lease.WorkItemID,
		HolderSID: lease.HolderSID, HolderRunRef: lease.HolderRunRef, HolderAgentRef: lease.HolderAgentRef,
		Fence: lease.Fence, TTLSeconds: 900, ExpectedVersion: itemVersion,
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	}); err != nil {
		t.Fatalf("renew the work lease as its holder: %v", err)
	}
	var renewed string
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		rec, found, err := findWorkLease(ctx, sc, lease.WorkItemID)
		if err != nil || !found {
			return fmt.Errorf("re-read the renewed lease: found=%t: %w", found, err)
		}
		renewed = rec.String(colLeaseExpiresAt)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return renewed
}

// requireConcealedLaunch asserts the confined reader answers not-found.
func (f *managedStopFixture) requireConcealedLaunch(t *testing.T, workspace model.ID, runRef, why string) {
	t.Helper()
	if snap, err := f.m.ReadRunLaunch(context.Background(), f.tenant, workspace, runRef); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("%s: ReadRunLaunch(%s, %s) = %+v, %v; want concealment", why, workspace, runRef, snap, err)
	}
}

// TestRunLineageFollowsTheCreatingIdentity is T-W1: a new run's lineage is the
// workspace its own session identity resolves to, stored as an explicit id in both
// the scoped and the default case; each confined reader sees only its own run; and
// a producer confined elsewhere refuses rather than resolving to something else.
func TestRunLineageFollowsTheCreatingIdentity(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, _ := newManagedStopWorkFixture(t, be.config(t))
			ctx := context.Background()
			bound, _, workspace, _ := f.boundRun(t)
			boundLive, ok := f.m.rt.getLive(f.tenant, bound.RunRef)
			if !ok {
				t.Fatal("the bound run has no live handle")
			}
			plain, plainLive := f.plainRun(t, "agent:w2-plain")
			if workspace == f.workspace {
				t.Fatal("the fixture must place the work in a non-default workspace")
			}

			// The identity facts the producer read.
			boundIdentity, err := f.m.ReadSessionIdentity(ctx, f.tenant, boundLive.claim.SID)
			if err != nil || boundIdentity.WorkspaceID != workspace {
				t.Fatalf("bound identity = %+v, %v; want workspace %s", boundIdentity, err, workspace)
			}
			plainIdentity, err := f.m.ReadSessionIdentity(ctx, f.tenant, plainLive.claim.SID)
			if err != nil || !plainIdentity.WorkspaceID.IsZero() {
				t.Fatalf("plain identity = %+v, %v; want no workspace (the tenant default)", plainIdentity, err)
			}
			// The persisted lineage: explicit in both cases, never NULL-as-default.
			if got, null := f.runLineage(bound.RunRef); null || got != workspace.String() {
				t.Fatalf("bound lineage = %q null=%t, want %s", got, null, workspace)
			}
			if got, null := f.runLineage(plain.RunRef); null || got != f.workspace.String() {
				t.Fatalf("plain lineage = %q null=%t, want the default workspace's own id %s",
					got, null, f.workspace)
			}

			// Each confined reader sees exactly its own run.
			snap, err := f.m.ReadRunLaunch(ctx, f.tenant, workspace, bound.RunRef)
			if err != nil || snap.RuntimeLaunchID != boundLive.launchID || snap.WorkItemID != bound.WorkItemID {
				t.Fatalf("bound launch in its workspace = %+v, %v", snap, err)
			}
			if _, err := f.m.ReadRunLaunch(ctx, f.tenant, f.workspace, plain.RunRef); err != nil {
				t.Fatalf("plain launch in the default workspace: %v", err)
			}
			f.requireConcealedLaunch(t, workspace, plain.RunRef, "a default-lineage run from the work workspace")
			f.requireConcealedLaunch(t, f.workspace, bound.RunRef, "a work-lineage run from the default workspace")

			// A producer confined elsewhere refuses, and assigns nothing.
			if err := f.st.View(ctx, f.tenant, func(raw store.Scope) error {
				inDefault, err := store.ConfineWorkspace(ctx, raw, f.workspace)
				if err != nil {
					return err
				}
				row := model.Record{}
				if err := setRunAuthzWorkspace(ctx, inDefault, row, boundLive.claim.SID); !errors.Is(err, errRunWorkspaceUnresolved) {
					t.Errorf("default-confined producer for a work identity = %v, want unresolved", err)
				}
				if _, assigned := row[colRunAuthzWorkspaceID]; assigned {
					t.Errorf("a refused producer assigned a lineage: %v", row)
				}
				inWork, err := store.ConfineWorkspace(ctx, raw, workspace)
				if err != nil {
					return err
				}
				row = model.Record{}
				if err := setRunAuthzWorkspace(ctx, inWork, row, plainLive.claim.SID); !errors.Is(err, errRunWorkspaceUnresolved) {
					t.Errorf("work-confined producer for a default identity = %v, want unresolved", err)
				}
				row = model.Record{}
				if err := setRunAuthzWorkspace(ctx, inWork, row, boundLive.claim.SID); err != nil ||
					row.String(colRunAuthzWorkspaceID) != workspace.String() {
					t.Errorf("work-confined producer for its own identity = %v / %v", err, row)
				}
				// The creation check for a bound run: the item must live in the lineage.
				if err := assertWorkItemSharesRunLineage(ctx, raw,
					model.Record{colRunAuthzWorkspaceID: f.workspace.String()}, bound.WorkItemID); err == nil ||
					asWorkError(err) == nil || asWorkError(err).verdict != VerdictBroken {
					t.Errorf("an item outside the resolved lineage = %v, want dispatch_conflict", err)
				}
				if err := assertWorkItemSharesRunLineage(ctx, raw,
					model.Record{colRunAuthzWorkspaceID: workspace.String()}, bound.WorkItemID); err != nil {
					t.Errorf("an item inside the resolved lineage = %v", err)
				}
				return nil
			}); err != nil {
				t.Fatalf("confined producer checks: %v", err)
			}
		})
	}
}

// TestRepairRunLineageRestoresLegacyRowsFromIdentityFacts is T-W3 on real runs:
// legacy rows bound to an identity with a workspace, an identity without one, a
// missing identity and a merged identity become W, the default, NULL and NULL.
func TestRepairRunLineageRestoresLegacyRowsFromIdentityFacts(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, _ := newManagedStopWorkFixture(t, be.config(t))
			ctx := context.Background()
			bound, _, workspace, _ := f.boundRun(t)
			fromDefault, _ := f.plainRun(t, "agent:w2-legacy-default")
			missing, _ := f.plainRun(t, "agent:w2-legacy-missing")
			merged, mergedLive := f.plainRun(t, "agent:w2-legacy-merged")
			refs := []string{bound.RunRef, fromDefault.RunRef, missing.RunRef, merged.RunRef}
			for _, ref := range refs {
				f.clearRunLineage(ref)
			}
			f.setRunClaimSID(missing.RunRef, "osn_"+model.NewID().String())
			f.mergeIdentity(t, mergedLive.claim.SID, "osn_"+model.NewID().String())
			for _, ref := range refs {
				f.requireConcealedLaunch(t, workspace, ref, "a legacy row before the pass")
				f.requireConcealedLaunch(t, f.workspace, ref, "a legacy row before the pass")
			}

			res, err := f.m.RepairRunLineage(ctx, f.tenant, RunLineageRepairCursor{})
			if err != nil {
				t.Fatalf("repair: %v", err)
			}
			if res.Scanned != 4 || res.Repaired != 2 || res.Unresolved != 2 || res.Conflicts != 0 || !res.Exhausted {
				t.Fatalf("pass = %+v, want 4 scanned, 2 repaired, 2 unresolved, exhausted", res)
			}
			if got, null := f.runLineage(bound.RunRef); null || got != workspace.String() {
				t.Fatalf("identity W: lineage %q null=%t, want %s", got, null, workspace)
			}
			if got, null := f.runLineage(fromDefault.RunRef); null || got != f.workspace.String() {
				t.Fatalf("identity NULL: lineage %q null=%t, want the default %s", got, null, f.workspace)
			}
			for _, ref := range []string{missing.RunRef, merged.RunRef} {
				if got, null := f.runLineage(ref); !null {
					t.Fatalf("%s acquired lineage %q from identity facts that cannot justify one", ref, got)
				}
			}

			// Confined visibility follows the repaired lineage and nothing else.
			if _, err := f.m.ReadRunLaunch(ctx, f.tenant, workspace, bound.RunRef); err != nil {
				t.Fatalf("repaired W row in W: %v", err)
			}
			if _, err := f.m.ReadRunLaunch(ctx, f.tenant, f.workspace, fromDefault.RunRef); err != nil {
				t.Fatalf("repaired default row in the default: %v", err)
			}
			for _, ref := range []string{missing.RunRef, merged.RunRef} {
				f.requireConcealedLaunch(t, workspace, ref, "an unresolved legacy row")
				f.requireConcealedLaunch(t, f.workspace, ref, "an unresolved legacy row")
			}
			// An unconfined reader still sees every row.
			seen := map[string]bool{}
			if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				rows, err := listAll(ctx, repo)
				for _, rec := range rows {
					seen[rec.String(colRunRef)] = true
				}
				return err
			}); err != nil {
				t.Fatalf("unconfined read: %v", err)
			}
			for _, ref := range refs {
				if !seen[ref] {
					t.Fatalf("the unconfined reader lost %s", ref)
				}
			}
		})
	}
}

// TestManagedStopBoundRunRefusesWithoutLeaseAuthority is T-D4b: a principal who
// may stop the run but not end its work lease, an operator below AAL3, and the
// run's OWN work-session token are all refused before any token or transaction.
func TestManagedStopBoundRunRefusesWithoutLeaseAuthority(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, runner := newManagedStopWorkFixture(t, be.config(t))
			// The run's work-session credential is minted by the REAL authenticator,
			// so the token case below presents a bearer the product actually issued.
			f.m.UseWorkSessionCredentialSource(newAuthWorkCredentialSource(f.authr))

			t.Run("editor_without_lease_admin", func(t *testing.T) {
				managed, _, workspace, lease := f.boundRun(t)
				op := f.operator("bound-editor@w2.test", auth.RoleEditor, true, 2*time.Minute)
				res, err := f.call(f.boundRequest(op, managed, workspace, lease, "bound-editor"))
				f.refuseWithNoJournalRow(res, err, "bound-editor", ManagedStopForbidden)
				if state := f.runState(managed.RunRef); state != stateRunning {
					t.Fatalf("a refused stop changed the run to %q", state)
				}
			})

			t.Run("below_aal3", func(t *testing.T) {
				managed, _, workspace, lease := f.boundRun(t)
				op := f.operator("bound-aal2@w2.test", auth.RoleAdmin, false, 2*time.Minute)
				res, err := f.call(f.boundRequest(op, managed, workspace, lease, "bound-aal2"))
				f.refuseWithNoJournalRow(res, err, "bound-aal2", ManagedStopStepUpRequired)
			})

			t.Run("the_runs_own_work_session_token", func(t *testing.T) {
				managed, _, workspace, lease := f.boundRun(t)
				tokens := envValues(runner.lastSpec().Env, "OLIVARES_WORK_TOKEN")
				if len(tokens) != 1 || tokens[0] == "" {
					t.Fatalf("the launch carried %d work tokens", len(tokens))
				}
				ctx := context.Background()
				// Presented exactly as a route would: authenticated, then reconstructed by
				// the module itself under its admission window.
				principal, err := f.authr.Authenticate(ctx, tokens[0])
				if err != nil {
					t.Fatalf("authenticate the run's own token: %v", err)
				}
				if principal.SessionIdentity == "" {
					t.Fatalf("the token did not authenticate as a work-session principal: %+v", principal)
				}
				res, err := f.call(f.boundRequest(principal, managed, workspace, lease, "bound-token"))
				if err != nil {
					t.Fatalf("unexpected error %v (%+v)", err, res)
				}
				if res.Outcome != ManagedStopForbidden && res.Outcome != ManagedStopStepUpRequired {
					t.Fatalf("outcome = %q (%s), want a refusal before the token", res.Outcome, res.Detail)
				}
				f.refuseWithNoJournalRow(res, err, "bound-token", res.Outcome)
				if state := f.runState(managed.RunRef); state != stateRunning {
					t.Fatalf("the token's refusal changed the run to %q", state)
				}
				t.Logf("the run's own work-session token was refused with %q (%s)", res.Outcome, res.Detail)
			})
		})
	}
}

// TestManagedStopBoundRunMovedAfterAuthorizationIsStale is the T-D4c window a
// request cannot express: durable work state changes AFTER Phase B asked its
// questions and BEFORE D5 re-proves the binding under the item lock.
//
// ⛔ THE ITEM ITSELF CANNOT MOVE, and that is measured rather than assumed: the
// engine refuses a rewrite of a work item's workspace lineage outright. The
// correction-2 case "item moved between Phase B and D5" is therefore excluded by
// the store, and this test pins that exclusion. The durable change that CAN land in
// that window is a lease renewal, and D5 must refuse it as stale with nothing
// written.
func TestManagedStopBoundRunMovedAfterAuthorizationIsStale(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, _ := newManagedStopWorkFixture(t, be.config(t))
			managed, itemID, workspace, lease := f.boundRun(t)
			elsewhere := f.createWorkspace(t, "w2-moved")
			op := f.operator("bound-moved@w2.test", auth.RoleAdmin, true, 2*time.Minute)
			req := f.boundRequest(op, managed, workspace, lease, "bound-moved")

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
				t.Fatalf("Phase B = %+v, %v", res, err)
			}
			if !authority.bound || authority.itemWorkspace != workspace {
				t.Fatalf("Phase B retained %+v, want the bound item in %s", authority, workspace)
			}

			if err := f.tryMoveWorkItem(itemID, elsewhere); err == nil {
				t.Fatal("the store let a work item change its workspace lineage")
			} else {
				t.Logf("%s: the store refuses a work item move: %v", be.name, err)
			}
			lr, ok := f.m.rt.getLive(f.tenant, managed.RunRef)
			if !ok {
				t.Fatal("no live handle")
			}
			renewed := f.renewWorkLease(t, lease, lr.claim.Fence)
			if renewed == lease.ExpiresAt {
				t.Fatal("the renewal did not move the durable expiry")
			}
			admission, err := f.m.admitManagedStop(actx, f.tenant, req.ManagedStopRequest, authority, lr,
				managedStopSurfacePrefix+req.OperationID,
				managedStopSemanticDigest(f.tenant, req.Question, req.ManagedStopRequest))
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if admission.result.Outcome != ManagedStopWorkLeaseStale {
				t.Fatalf("D5 after the renewal = %q (%s), want work_lease_stale",
					admission.result.Outcome, admission.result.Detail)
			}
			if _, found := f.journal("bound-moved"); found {
				t.Fatal("a stale binding burned a journal identity")
			}
			if state := f.runState(managed.RunRef); state != stateRunning {
				t.Fatalf("a stale binding changed the run to %q", state)
			}
		})
	}
}

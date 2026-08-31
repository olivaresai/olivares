// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type inspectingWorkLaunchRunner struct {
	inner  *fakeRunner
	before func() error
}

func (r *inspectingWorkLaunchRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if r.before != nil {
		if err := r.before(); err != nil {
			return nil, err
		}
	}
	return r.inner.Launch(ctx, spec)
}

// durableWorkLaunchIdentity exercises the same canonical owner namespace split
// as production: WorkItem.owner_ref is Identity.ID, while sessions_run.agent_ref
// is the identity's ExternalID. Agent authority locking is inherited from the
// ordinary deterministic test seam; participant and SID attribution are read
// from real store rows.
type durableWorkLaunchIdentity struct {
	allowWorkIdentity
	m  *Module
	st store.Store
}

func (r durableWorkLaunchIdentity) ResolveParticipant(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	kind, ref string,
) (Participant, error) {
	if kind == "session" {
		return r.m.SessionWorkParticipant(ctx, tenant, workspace, ref)
	}
	out := Participant{Kind: kind, CanonicalRef: ref}
	if kind != "agent" {
		return out, nil
	}
	ownerID, err := model.ParseID(ref)
	if err != nil {
		return out, err
	}
	err = r.st.View(ctx, tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Get(ctx, ownerID)
		if err != nil {
			return err
		}
		if identity.ExternalID == "" {
			return nil
		}
		agents, page, err := sc.Agents().List(ctx, model.Query{Filters: []model.Filter{{
			Column: "identity_id", Op: model.OpEq, Value: ownerID.String(),
		}}, Limit: 100})
		if err != nil {
			return err
		}
		if page.HasMore {
			return errors.New("agent identity enumeration is truncated")
		}
		for _, agent := range agents {
			agentWorkspace := agent.WorkspaceID
			if agentWorkspace.IsZero() {
				defaultWorkspace, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				agentWorkspace = defaultWorkspace.ID
			}
			if agent.IdentityID == ownerID && agentWorkspace == workspace &&
				agent.Status == model.StatusActive {
				out.Active, out.WorkspaceEligible = true, true
			}
		}
		return nil
	})
	return out, err
}

func (r durableWorkLaunchIdentity) SessionActsForAgent(
	ctx context.Context,
	tenant model.TenantID,
	sid, canonicalAgentRef string,
) (bool, error) {
	ownerID, err := model.ParseID(canonicalAgentRef)
	if err != nil {
		return false, err
	}
	var externalRef string
	if err := r.st.View(ctx, tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Get(ctx, ownerID)
		if err == nil {
			externalRef = identity.ExternalID
		}
		return err
	}); err != nil {
		return false, err
	}
	return r.m.SessionActsForAgent(ctx, tenant, sid, externalRef)
}

func readyWorkLaunchItem(
	t *testing.T,
	m *Module,
	st store.Store,
	tenant model.TenantID,
) (model.ID, model.ID, string) {
	t.Helper()
	ctx := context.Background()
	var workspace, ownerID model.ID
	ownerExternal := "agent:k4-launch-" + model.NewID().String()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "K4 work launch", Slug: "k4-work-launch-" + model.NewID().String()[:8],
			Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		workspace = ws.ID
		identity, err := sc.Identities().Create(ctx, model.Identity{
			Name: "K4 managed executor", Kind: "agent_nhi",
			ExternalID: ownerExternal, Provider: "test",
		})
		if err != nil {
			return err
		}
		ownerID = identity.ID
		_, err = sc.Agents().Create(ctx, model.Agent{
			Name: "K4 managed executor", Kind: "test", ExternalID: ownerExternal,
			Status: model.StatusActive, IdentityID: ownerID, WorkspaceID: workspace,
		})
		return err
	}); err != nil {
		t.Fatalf("create core workspace and agent owner: %v", err)
	}
	principal := WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "k4-test-setup",
		Actor: "system:k4-test-setup", Admin: true,
	}
	created, err := m.Apply(ctx, tenant, principal, WorkCommand{
		Command: "item.create", WorkspaceID: workspace,
		WorkKind: "implementation", Title: "Launch a managed K4 session",
		BriefMD:     "Run the exact WorkItem generation through the supervised runtime.",
		ContextRefs: []ContextRef{}, Priority: "p1",
		OwnerKind: "agent", OwnerRef: ownerID.String(),
		ProvenanceKind: "workflow", ProvenanceRef: "test:k4-runtime-control",
		Acceptance: []AcceptanceInput{{
			Key: "runtime", Ordinal: 0,
			Statement: "The managed runtime starts under the durable lease.", Required: true,
		}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
		CommandScope: "POST /work-items",
	})
	if err != nil {
		t.Fatalf("create WorkItem: %v", err)
	}
	ready, err := m.Apply(ctx, tenant, principal, WorkCommand{
		Command: "item.ready", WorkItemID: created.ResultID,
		ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
		HTTPMethod: http.MethodPost,
	})
	if err != nil || ready.Status != "ready" {
		t.Fatalf("ready WorkItem = %#v, %v", ready, err)
	}
	return created.ResultID, workspace, ownerExternal
}

func workLaunchSpec(itemID model.ID, agentRef string) WorkLaunchSpec {
	return WorkLaunchSpec{
		WorkItemID: itemID, AuditActorRef: agentRef,
		Runtime: CreateRunParams{
			Name: "k4-managed", Transport: TransportStreamJSON,
			Isolation: IsolationNative, Actor: agentRef,
			ActorKind: model.ActorAgent, AgentRef: agentRef,
		},
	}
}

func TestLaunchForWorkReservesAndBindsBeforeSpawnWithCanonicalWorkspace(t *testing.T) {
	t.Parallel()

	inner := &fakeRunner{}
	runner := &inspectingWorkLaunchRunner{inner: inner}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	itemID, workspace, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(itemID, agentRef)
	expectedKey := WorkLaunchDispatchKey(itemID, 1, 1, WorkLaunchAttemptLeaseBind)
	inspected := false
	runner.before = func() error {
		return st.View(context.Background(), tenant, func(sc store.Scope) error {
			runs, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rows, _, err := runs.List(context.Background(), model.Query{Filters: []model.Filter{{
				Column: colRunWorkDispatchKey, Op: model.OpEq, Value: expectedKey[:],
			}}, Limit: 2})
			if err != nil || len(rows) != 1 {
				return fmt.Errorf("pending dispatch rows = %d: %w", len(rows), err)
			}
			run := rows[0]
			if run.String(colState) != statePending || run.String(colRunWorkItemID) != itemID.String() ||
				run.Int(colRunWorkLeaseFence) != 1 || run.Int(colRunWorkOwnerEpoch) != 1 ||
				len(run.Bytes(colRunWorkLaunchSpecHash)) != 32 {
				return fmt.Errorf("spawn observed incomplete reservation: %#v", run)
			}
			lease, found, err := findWorkLease(context.Background(), sc, itemID)
			if err != nil || !found {
				return fmt.Errorf("work lease unavailable before spawn: %w", err)
			}
			if lease.String(colLeaseState) != workLeaseActive || lease.Int(colLeaseFence) != 1 ||
				lease.String(colLeaseHolderRunRef) != run.String(colRunRef) {
				return fmt.Errorf("spawn preceded lease bind: %#v", lease)
			}
			inspected = true
			return nil
		})
	}

	var control RuntimeControl = m
	managed, err := control.LaunchForWork(context.Background(), tenant, spec)
	if err != nil {
		t.Fatalf("LaunchForWork: %v", err)
	}
	if !inspected || managed.Replayed || managed.State != stateRunning ||
		managed.WorkItemID != itemID || managed.WorkspaceID != workspace ||
		managed.WorkLeaseFence != 1 || managed.OwnerEpoch != 1 ||
		managed.DispatchKey != hex.EncodeToString(expectedKey[:]) || !validCanonicalSID(managed.SessionID) {
		t.Fatalf("managed run = %#v, inspected=%v", managed, inspected)
	}
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		identity, found, err := findIdentity(context.Background(), sc, managed.SessionID)
		if err != nil || !found {
			return errors.New("managed SID identity is absent")
		}
		if identity.String(colIDWorkspaceID) != workspace.String() {
			return fmt.Errorf("identity workspace = %q, want %s",
				identity.String(colIDWorkspaceID), workspace)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := control.InputForWork(
		context.Background(), tenant, managed.RunRef, managed.WorkLeaseFence, []byte(`{"type":"user"}`),
	); err != nil {
		t.Fatalf("RuntimeControl.InputForWork: %v", err)
	}
	if got := inner.lastProc().sentCount(); got != 1 {
		t.Fatalf("RuntimeControl.InputForWork sent %d frames, want 1", got)
	}
	if err := control.StopForWork(
		context.Background(), tenant, managed.RunRef, managed.WorkLeaseFence, "test complete",
	); err != nil {
		t.Fatalf("RuntimeControl.StopForWork: %v", err)
	}
}

func TestLaunchForWorkUserAuditUsesDurableAgentExecutorAndReplays(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	itemID, workspace, ownerExternal := readyWorkLaunchItem(t, m, st, tenant)
	m.UseWorkIdentityResolver(durableWorkLaunchIdentity{m: m, st: st})
	snapshot, err := m.Get(ctx, tenant, WorkPrincipal{}, itemID)
	if err != nil {
		t.Fatalf("load agent-owned WorkItem: %v", err)
	}
	ownerCanonical := snapshot.Item.OwnerRef
	userID := model.NewID()
	spec := workLaunchSpec(itemID, ownerExternal)
	spec.AuditActorRef = userID.String()
	spec.Runtime.Actor = "token:workflow-admin"
	spec.Runtime.ActorKind = model.ActorUser
	// AgentRef is a server-owned output. A value left by the caller must never
	// become the executor or Claim holder.
	spec.Runtime.AgentRef = "agent:caller-value-must-be-ignored"

	first, err := m.LaunchForWork(ctx, tenant, spec)
	if err != nil {
		t.Fatalf("user-admin LaunchForWork: %v", err)
	}
	replayed, err := m.LaunchForWork(ctx, tenant, spec)
	if err != nil {
		t.Fatalf("user-admin LaunchForWork replay: %v", err)
	}
	if !replayed.Replayed || replayed.RunRef != first.RunRef ||
		replayed.SessionID != first.SessionID || replayed.DispatchKey != first.DispatchKey {
		t.Fatalf("user-admin replay diverged: first=%#v replayed=%#v", first, replayed)
	}
	lease, err := m.GetLease(ctx, tenant, WorkPrincipal{}, itemID)
	if err != nil {
		t.Fatalf("load bound WorkLease: %v", err)
	}
	if !lease.Live || lease.WorkspaceID != workspace || lease.HolderSID != first.SessionID ||
		lease.HolderRunRef != first.RunRef || lease.HolderAgentRef != ownerCanonical {
		t.Fatalf("derived WorkLease holder = %#v", lease)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		run, err := findRunRec(ctx, runs, first.RunRef)
		if err != nil {
			return err
		}
		if run.String(colRunAgentRef) != ownerExternal {
			return fmt.Errorf("runtime agent_ref = %q, want durable %q",
				run.String(colRunAgentRef), ownerExternal)
		}
		claim, found, err := findClaim(ctx, sc, first.SessionID)
		if err != nil || !found {
			return fmt.Errorf("runtime Claim missing: %w", err)
		}
		if claim.String(colHolder) != ownerExternal {
			return fmt.Errorf("claim holder = %q, want durable executor %q",
				claim.String(colHolder), ownerExternal)
		}
		runtimeEvents, err := sc.Ext(runEventKind)
		if err != nil {
			return err
		}
		rows, _, err := runtimeEvents.List(ctx, model.Query{Filters: []model.Filter{
			eq(colEvRunRef, first.RunRef), eq(colEvEvent, "created"),
		}, Limit: 2})
		if err != nil || len(rows) != 1 {
			return fmt.Errorf("runtime created events = %d: %w", len(rows), err)
		}
		if rows[0].String(colEvActor) != spec.Runtime.Actor ||
			rows[0].String(colEvActorKind) != model.ActorUser {
			return fmt.Errorf("runtime audit actor changed: %#v", rows[0])
		}
		workEvents, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		rows, _, err = workEvents.List(ctx, model.Query{Filters: []model.Filter{
			eq(colEventAggregateID, itemID.String()), eq(colEventType, "work.lease.acquired"),
		}, Limit: 2})
		if err != nil || len(rows) != 1 {
			return fmt.Errorf("lease acquired events = %d: %w", len(rows), err)
		}
		if rows[0].String(colEventActorKind) != model.ActorUser ||
			rows[0].String(colEventActorRef) != userID.String() {
			return fmt.Errorf("work audit actor changed: %#v", rows[0])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	launches := len(runner.specs)
	runner.mu.Unlock()
	if launches != 1 {
		t.Fatalf("user-admin exact replay spawned %d processes, want 1", launches)
	}
	finishWorkRuntimeRun(t, m, tenant, first.RunRef, runner.lastProc())
}

func TestLaunchForWorkRejectsSessionOwnerBeforeReservation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	var workspace model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "K4 session-owned work", Slug: "k4-session-owner-" + model.NewID().String()[:8],
			Status: model.StatusActive,
		})
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatalf("create core workspace: %v", err)
	}
	ownerSID, err := m.ResolveSession(ctx, tenant, SessionBinding{
		Provider: "test", ExternalID: "existing-owner-session", Origin: OriginOperated,
		WorkspaceID: workspace,
	})
	if err != nil {
		t.Fatalf("resolve existing owner session: %v", err)
	}
	ownerClaim, err := m.Claim(ctx, tenant, ownerSID, "agent:existing-owner", 0)
	if err != nil {
		t.Fatalf("claim existing owner session: %v", err)
	}
	t.Cleanup(func() { _ = m.Release(context.Background(), tenant, ownerSID, ownerClaim.Holder, ownerClaim.Fence) })
	setup := WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "k4-test-setup",
		Actor: "system:k4-test-setup", Admin: true,
	}
	created, err := m.Apply(ctx, tenant, setup, WorkCommand{
		Command: "item.create", WorkspaceID: workspace,
		WorkKind: "implementation", Title: "Already delegated session work",
		BriefMD:     "This WorkItem already belongs to an existing canonical session.",
		ContextRefs: []ContextRef{}, Priority: "p1",
		OwnerKind: "session", OwnerRef: ownerSID,
		ProvenanceKind: "workflow", ProvenanceRef: "test:k4-session-owner",
		Acceptance: []AcceptanceInput{{
			Key: "done", Ordinal: 0, Statement: "The existing session completes it.", Required: true,
		}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
		CommandScope: "POST /work-items",
	})
	if err != nil {
		t.Fatalf("create session-owned WorkItem: %v", err)
	}
	ready, err := m.Apply(ctx, tenant, setup, WorkCommand{
		Command: "item.ready", WorkItemID: created.ResultID,
		ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
		HTTPMethod: http.MethodPost,
	})
	if err != nil || ready.Status != "ready" {
		t.Fatalf("ready session-owned WorkItem = %#v, %v", ready, err)
	}
	userID := model.NewID()
	_, err = m.LaunchForWork(ctx, tenant, WorkLaunchSpec{
		WorkItemID: created.ResultID, AuditActorRef: userID.String(),
		Runtime: CreateRunParams{
			Name: "must-not-launch", Actor: "token:workflow-admin", ActorKind: model.ActorUser,
		},
	})
	if we := asWorkError(err); we == nil || we.code != "owner_ineligible" {
		t.Fatalf("session-owned LaunchForWork = %v, want owner_ineligible", err)
	}
	runner.mu.Lock()
	launches := len(runner.specs)
	runner.mu.Unlock()
	if launches != 0 {
		t.Fatalf("session-owned LaunchForWork spawned %d processes", launches)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rows, _, err := runs.List(ctx, model.Query{Filters: []model.Filter{{
			Column: colRunWorkItemID, Op: model.OpEq, Value: created.ResultID.String(),
		}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			return fmt.Errorf("session-owned launch persisted %d reservations", len(rows))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchForWorkRejectsUserOwnerBeforeReservation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	runner := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	var workspace model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "K4 user-owned work", Slug: "k4-user-owner-" + model.NewID().String()[:8],
			Status: model.StatusActive,
		})
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatalf("create core workspace: %v", err)
	}
	ownerID := model.NewID()
	setup := WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "k4-test-setup",
		Actor: "system:k4-test-setup", Admin: true,
	}
	created, err := m.Apply(ctx, tenant, setup, WorkCommand{
		Command: "item.create", WorkspaceID: workspace,
		WorkKind: "implementation", Title: "User-owned workflow work",
		BriefMD:     "This WorkItem remains assigned to a user and cannot create a managed session.",
		ContextRefs: []ContextRef{}, Priority: "p1",
		OwnerKind: "user", OwnerRef: ownerID.String(),
		ProvenanceKind: "workflow", ProvenanceRef: "test:k4-user-owner",
		Acceptance: []AcceptanceInput{{
			Key: "done", Ordinal: 0, Statement: "The assigned user completes it.", Required: true,
		}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
		CommandScope: "POST /work-items",
	})
	if err != nil {
		t.Fatalf("create user-owned WorkItem: %v", err)
	}
	ready, err := m.Apply(ctx, tenant, setup, WorkCommand{
		Command: "item.ready", WorkItemID: created.ResultID,
		ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
		HTTPMethod: http.MethodPost,
	})
	if err != nil || ready.Status != "ready" {
		t.Fatalf("ready user-owned WorkItem = %#v, %v", ready, err)
	}
	auditUserID := model.NewID()
	_, err = m.LaunchForWork(ctx, tenant, WorkLaunchSpec{
		WorkItemID: created.ResultID, AuditActorRef: auditUserID.String(),
		Runtime: CreateRunParams{
			Name: "must-not-launch", Actor: "token:workflow-admin", ActorKind: model.ActorUser,
		},
	})
	if we := asWorkError(err); we == nil || we.code != "owner_ineligible" {
		t.Fatalf("user-owned LaunchForWork = %v, want owner_ineligible", err)
	}
	runner.mu.Lock()
	launches := len(runner.specs)
	runner.mu.Unlock()
	if launches != 0 {
		t.Fatalf("user-owned LaunchForWork spawned %d processes", launches)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rows, _, err := runs.List(ctx, model.Query{Filters: []model.Filter{{
			Column: colRunWorkItemID, Op: model.OpEq, Value: created.ResultID.String(),
		}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			return fmt.Errorf("user-owned launch persisted %d reservations", len(rows))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchForWorkRejectsNonCanonicalUserAuditActor(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	itemID, _, ownerExternal := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(itemID, ownerExternal)
	spec.Runtime.Actor = "token:workflow-admin"
	spec.Runtime.ActorKind = model.ActorUser
	spec.AuditActorRef = "token:not-a-canonical-user-id"
	if _, err := m.LaunchForWork(context.Background(), tenant, spec); err == nil {
		t.Fatal("non-canonical user audit actor was accepted")
	} else if we := asWorkError(err); we == nil || we.code != "invalid_command" {
		t.Fatalf("non-canonical user audit actor = %v, want invalid_command", err)
	}
	runner.mu.Lock()
	launches := len(runner.specs)
	runner.mu.Unlock()
	if launches != 0 {
		t.Fatalf("invalid audit actor spawned %d processes", launches)
	}
}

func TestLaunchForWorkExactDispatchReplayNeverSpawnsTwice(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(itemID, agentRef)
	first, err := m.LaunchForWork(context.Background(), tenant, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.LaunchForWork(context.Background(), tenant, spec)
	if err != nil {
		t.Fatal(err)
	}
	if second.RunRef != first.RunRef || second.SessionID != first.SessionID ||
		second.DispatchKey != first.DispatchKey || !second.Replayed {
		t.Fatalf("dispatch replay changed result: first=%#v second=%#v", first, second)
	}
	runner.mu.Lock()
	launches := len(runner.specs)
	runner.mu.Unlock()
	if launches != 1 {
		t.Fatalf("exact dispatch replay spawned %d processes, want 1", launches)
	}
	finishWorkRuntimeRun(t, m, tenant, first.RunRef, runner.lastProc())
	explicit := spec
	explicit.OwnerEpoch = first.OwnerEpoch
	explicit.WorkLeaseFence = first.WorkLeaseFence
	explicit.AttemptKind = WorkLaunchAttemptLeaseBind
	decoded, err := hex.DecodeString(first.DispatchKey)
	if err != nil || len(decoded) != len(explicit.DispatchKey) {
		t.Fatalf("decode dispatch key: %v", err)
	}
	copy(explicit.DispatchKey[:], decoded)
	afterDeath, err := m.LaunchForWork(context.Background(), tenant, explicit)
	if err != nil {
		t.Fatalf("replay after owner death: %v", err)
	}
	if !afterDeath.Replayed || afterDeath.RunRef != first.RunRef || afterDeath.State != stateStopped {
		t.Fatalf("durable replay after owner death = %#v", afterDeath)
	}
	runner.mu.Lock()
	launches = len(runner.specs)
	runner.mu.Unlock()
	if launches != 1 {
		t.Fatalf("post-terminal replay spawned %d processes, want 1", launches)
	}
}

func TestLaunchForWorkReplaySurvivesStoreReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "runtime-work-launch.db")
	clk := &testClock{now: baseTime}
	runnerBeforeRestart := &fakeRunner{}
	m1 := New(
		WithClock(clk), WithRunner(runnerBeforeRestart), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	st1, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, m1.RegisterSchema)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	var tenant model.TenantID
	if err := st1.System(ctx, func(sys store.SystemScope) error {
		if _, ensureErr := sys.EnsureSystemTenant(ctx); ensureErr != nil {
			return ensureErr
		}
		org, createErr := sys.CreateOrg(ctx, model.Org{
			Name: "acme", Slug: "acme", Status: model.StatusActive,
		})
		tenant = org.TenantID
		return createErr
	}); err != nil {
		_ = st1.Close()
		t.Fatalf("create tenant: %v", err)
	}
	m1.UseData(api.NewModuleData(st1))
	itemID, _, agentRef := readyWorkLaunchItem(t, m1, st1, tenant)
	spec := workLaunchSpec(itemID, agentRef)
	first, err := m1.LaunchForWork(ctx, tenant, spec)
	if err != nil {
		_ = st1.Close()
		t.Fatalf("first LaunchForWork: %v", err)
	}
	finishWorkRuntimeRun(t, m1, tenant, first.RunRef, runnerBeforeRestart.lastProc())
	if err := st1.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	runnerAfterRestart := &fakeRunner{}
	m2 := New(
		WithClock(clk), WithRunner(runnerAfterRestart), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	st2, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, m2.RegisterSchema)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	m2.UseData(api.NewModuleData(st2))

	explicit := spec
	explicit.OwnerEpoch = first.OwnerEpoch
	explicit.WorkLeaseFence = first.WorkLeaseFence
	explicit.AttemptKind = WorkLaunchAttemptLeaseBind
	decoded, err := hex.DecodeString(first.DispatchKey)
	if err != nil || len(decoded) != len(explicit.DispatchKey) {
		t.Fatalf("decode dispatch key: %v", err)
	}
	copy(explicit.DispatchKey[:], decoded)
	replayed, err := m2.LaunchForWork(ctx, tenant, explicit)
	if err != nil {
		t.Fatalf("LaunchForWork after store reopen: %v", err)
	}
	if !replayed.Replayed || replayed.RunRef != first.RunRef ||
		replayed.SessionID != first.SessionID || replayed.DispatchKey != first.DispatchKey ||
		replayed.OwnerEpoch != first.OwnerEpoch || replayed.WorkLeaseFence != first.WorkLeaseFence ||
		replayed.State != stateStopped {
		t.Fatalf("restart replay changed managed reference: first=%#v replayed=%#v", first, replayed)
	}
	runnerAfterRestart.mu.Lock()
	launchesAfterRestart := len(runnerAfterRestart.specs)
	runnerAfterRestart.mu.Unlock()
	if launchesAfterRestart != 0 {
		t.Fatalf("restart replay spawned %d processes, want 0", launchesAfterRestart)
	}
}

func TestLaunchForWorkSameDispatchWithDifferentRuntimeConflicts(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(itemID, agentRef)
	first, err := m.LaunchForWork(context.Background(), tenant, spec)
	if err != nil {
		t.Fatal(err)
	}
	changed := spec
	changed.Runtime.Model = "different-runtime-profile"
	if _, err := m.LaunchForWork(context.Background(), tenant, changed); err == nil {
		t.Fatal("same dispatch key with a different runtime spec was accepted")
	} else if we := asWorkError(err); we == nil || we.code != "dispatch_conflict" {
		t.Fatalf("changed dispatch spec = %v, want dispatch_conflict", err)
	}
	changedAudit := spec
	changedAudit.AuditActorRef = model.NewID().String()
	if _, err := m.LaunchForWork(context.Background(), tenant, changedAudit); err == nil {
		t.Fatal("same dispatch key with a different audit actor was accepted")
	} else if we := asWorkError(err); we == nil || we.code != "dispatch_conflict" {
		t.Fatalf("changed audit actor = %v, want dispatch_conflict", err)
	}
	runner.mu.Lock()
	launches := len(runner.specs)
	runner.mu.Unlock()
	if launches != 1 {
		t.Fatalf("conflicting dispatch spawned %d processes, want 1", launches)
	}
	finishWorkRuntimeRun(t, m, tenant, first.RunRef, runner.lastProc())
}

func TestLaunchForWorkConcurrentDispatchHasOneProcess(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}),
	)
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	spec := workLaunchSpec(itemID, agentRef)
	type outcome struct {
		managed ManagedRunRef
		err     error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			managed, err := m.LaunchForWork(context.Background(), tenant, spec)
			outcomes <- outcome{managed: managed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)
	var got []ManagedRunRef
	for result := range outcomes {
		if result.err != nil {
			t.Fatalf("concurrent LaunchForWork: %v", result.err)
		}
		got = append(got, result.managed)
	}
	if len(got) != 2 || got[0].RunRef == "" || got[0].RunRef != got[1].RunRef ||
		got[0].SessionID != got[1].SessionID || got[0].DispatchKey != got[1].DispatchKey {
		t.Fatalf("concurrent dispatch results diverged: %#v", got)
	}
	runner.mu.Lock()
	launches := len(runner.specs)
	runner.mu.Unlock()
	if launches != 1 {
		t.Fatalf("concurrent dispatch spawned %d processes, want 1", launches)
	}
	finishWorkRuntimeRun(t, m, tenant, got[0].RunRef, runner.lastProc())
}

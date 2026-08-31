// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

type workAgentLifecycleStub struct {
	eligible bool
	err      error
	wantRef  string
	calls    int
	inScope  int
	facts    []store.AuthorizationFactRef
}

func (s *workAgentLifecycleStub) AgentEligibleForWork(_ context.Context, _ model.TenantID, identityRef string) (bool, error) {
	s.calls++
	if s.wantRef != "" && identityRef != s.wantRef {
		return false, errors.New("lifecycle received a non-canonical external reference")
	}
	return s.eligible, s.err
}

func (s *workAgentLifecycleStub) AgentWorkAuthorityFactsInScope(
	_ context.Context,
	_ store.Scope,
	identityRef string,
) (bool, []store.AuthorizationFactRef, error) {
	s.inScope++
	if s.wantRef != "" && identityRef != s.wantRef {
		return false, nil, errors.New("in-scope lifecycle received a non-canonical external reference")
	}
	if len(s.facts) == 0 {
		s.facts = []store.AuthorizationFactRef{
			{Kind: "core.identity", ID: model.NewID(), Version: 1},
			{Kind: "governance.nhi_lifecycle", ID: model.NewID(), Version: 1},
		}
	}
	return s.eligible, append([]store.AuthorizationFactRef(nil), s.facts...), s.err
}

func TestWiredResolverRequiresAgentLifecycleEligibility(t *testing.T) {
	resolver, _, st, tenant, workspace := newSessionResolverFixture(t)
	identityID, externalRef := seedCanonicalWorkAgent(t, st, tenant, "nhi:agent:work-eligible")
	stub := &workAgentLifecycleStub{eligible: true, wantRef: externalRef}
	resolver.agentLifecycle = stub

	got, err := resolver.ResolveParticipant(
		context.Background(), tenant, workspace, "agent", identityID.String(),
	)
	if err != nil {
		t.Fatalf("resolve eligible agent: %v", err)
	}
	if !got.Active || !got.WorkspaceEligible || got.CanonicalRef != identityID.String() {
		t.Fatalf("eligible lifecycle did not converge with core agent: %#v", got)
	}
	if stub.calls != 1 {
		t.Fatalf("lifecycle calls = %d, want 1", stub.calls)
	}

	// A lifecycle refusal keeps the canonical participant visible but inactive.
	// This is an authoritative negative, not an infrastructure error.
	stub.eligible = false
	got, err = resolver.ResolveParticipant(
		context.Background(), tenant, workspace, "agent", identityID.String(),
	)
	if err != nil || got.Active {
		t.Fatalf("ineligible lifecycle = %#v, %v; want inactive participant", got, err)
	}

	// NO-FIRE: lifecycle eligibility cannot revive a disabled core agent.
	stub.eligible = true
	setWorkAgentStatus(t, st, tenant, identityID, model.StatusInactive)
	got, err = resolver.ResolveParticipant(
		context.Background(), tenant, workspace, "agent", identityID.String(),
	)
	if err != nil || got.Active {
		t.Fatalf("disabled core agent = %#v, %v; want inactive participant", got, err)
	}
}

func TestWiredResolverLocksAndRevalidatesCanonicalAgentInScope(t *testing.T) {
	resolver, _, st, tenant, workspace := newSessionResolverFixture(t)
	identityID, externalRef := seedCanonicalWorkAgent(t, st, tenant, "nhi:agent:work-locked")
	stub := &workAgentLifecycleStub{eligible: true, wantRef: externalRef}
	resolver.agentLifecycle = stub

	snapshot, err := resolver.ObserveAgentWorkAuthority(
		context.Background(), tenant, workspace, identityID.String(), externalRef,
	)
	if err != nil || !snapshot.Eligible || snapshot.Digest == "" || snapshot.Token == nil {
		t.Fatalf("eligible authority snapshot = %#v, %v", snapshot, err)
	}
	token, ok := snapshot.Token.(workAgentAuthorityToken)
	if !ok || len(token.facts) < 4 {
		t.Fatalf("snapshot token facts = %#v, want identity+agent+sponsor+lifecycle", snapshot.Token)
	}
	if stub.inScope != 1 {
		t.Fatalf("in-scope lifecycle calls = %d, want 1", stub.inScope)
	}
	stub.inScope = 0
	forged, err := resolver.ObserveAgentWorkAuthority(
		context.Background(), tenant, workspace, identityID.String(), "agent:forged",
	)
	if err != nil || forged.Eligible || stub.inScope != 0 {
		t.Fatalf("forged ExternalID snapshot = %#v, %v, lifecycle calls=%d",
			forged, err, stub.inScope)
	}

	// NO-FIRE: a fresh observation refuses an inactive core agent before
	// consulting lifecycle.
	setWorkAgentStatus(t, st, tenant, identityID, model.StatusInactive)
	stub.inScope = 0
	fresh, err := resolver.ObserveAgentWorkAuthority(
		context.Background(), tenant, workspace, identityID.String(), externalRef,
	)
	if err != nil || fresh.Eligible {
		t.Fatalf("fresh inactive core agent snapshot = %#v, %v", fresh, err)
	}
	if stub.inScope != 0 {
		t.Fatalf("lifecycle consulted after core agent refusal: %d calls", stub.inScope)
	}
}

func TestWiredResolverFailsClosedWhenAgentLifecycleCannotBeRead(t *testing.T) {
	resolver, _, st, tenant, workspace := newSessionResolverFixture(t)
	identityID, externalRef := seedCanonicalWorkAgent(t, st, tenant, "nhi:agent:work-unavailable")

	if got, err := resolver.ResolveParticipant(
		context.Background(), tenant, workspace, "agent", identityID.String(),
	); err == nil || got.Active {
		t.Fatalf("unwired lifecycle = %#v, %v; want error", got, err)
	}

	resolver.agentLifecycle = &workAgentLifecycleStub{
		eligible: true, err: errors.New("lifecycle store unavailable"), wantRef: externalRef,
	}
	if got, err := resolver.ResolveParticipant(
		context.Background(), tenant, workspace, "agent", identityID.String(),
	); err == nil || got.Active {
		t.Fatalf("failed lifecycle read = %#v, %v; want error", got, err)
	}
}

func TestWiredResolverRealLifecycleSnapshotSurvivesWorkspaceConfinement(t *testing.T) {
	ctx := context.Background()
	gov := governance.New()
	cfg := store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true,
	}
	st, err := coreengine.Open(ctx, cfg, gov.RegisterSchema)
	if err != nil {
		t.Fatalf("open governance authority store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gov.UseData(api.NewModuleData(st))

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "authority confinement", Slug: "authority-confinement", Status: model.StatusActive,
		})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision authority tenant: %v", err)
	}

	var workspace, ownerID model.ID
	const (
		ownerExternal   = "agent:authority-confined"
		sponsorExternal = "human:authority-sponsor"
	)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		workspace = ws.ID
		owner, err := sc.Identities().Create(ctx, model.Identity{
			Name: "authority owner", Kind: "agent_nhi", ExternalID: ownerExternal, Provider: "test",
		})
		if err != nil {
			return err
		}
		ownerID = owner.ID
		if _, err := sc.Agents().Create(ctx, model.Agent{
			Name: "authority agent", Kind: "test", ExternalID: ownerExternal,
			Status: model.StatusActive, IdentityID: owner.ID, WorkspaceID: workspace,
		}); err != nil {
			return err
		}
		if _, err := sc.Identities().Create(ctx, model.Identity{
			Name: "authority sponsor", Kind: "user", ExternalID: sponsorExternal,
			Provider: "test", Metadata: map[string]any{
				"principal_type": "human", "disabled": false,
			},
		}); err != nil {
			return err
		}
		repo, err := sc.Ext("governance.nhi_lifecycle")
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			"identity_ref": ownerExternal, "source": "test", "criticality": "high",
			"sponsor_ref": sponsorExternal, "max_age_seconds": int64(0),
			"staleness_status": "unknown", "enforcement": "monitor",
			"orphaned": false, "offboard_state": "none", "kind": "agent",
		})
		return err
	}); err != nil {
		t.Fatalf("seed real authority facts: %v", err)
	}

	resolver := workIdentityResolver{st: st, agentLifecycle: gov}
	snapshot, err := resolver.ObserveAgentWorkAuthority(
		ctx, tenant, workspace, ownerID.String(), ownerExternal,
	)
	if err != nil || !snapshot.Eligible || snapshot.Digest == "" {
		t.Fatalf("observe real governance lifecycle = %#v, %v", snapshot, err)
	}
	tampered := snapshot
	tampered.Digest = "forged"
	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, workspace)
		if err != nil {
			return err
		}
		return resolver.LockAgentWorkAuthority(ctx, confined, tampered)
	}); !errors.Is(err, store.ErrRowLockUnavailable) {
		t.Fatalf("tampered authority digest err = %v, want deny-closed", err)
	}
	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, workspace)
		if err != nil {
			return err
		}
		return resolver.LockAgentWorkAuthority(ctx, confined, snapshot)
	}); err != nil {
		t.Fatalf("real lifecycle snapshot through confined transaction: %v", err)
	}

	// ActorRef is authenticated in the ExternalID namespace. Rotating the
	// canonical owner's ExternalID after Observe must invalidate the exact owner
	// Identity fact before an owner-only write can use that old authentication.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		owner, err := sc.Identities().Get(ctx, ownerID)
		if err != nil {
			return err
		}
		owner.ExternalID = "agent:authority-rotated"
		_, err = sc.Identities().Update(ctx, owner)
		return err
	}); err != nil {
		t.Fatalf("rotate authority owner ExternalID: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, workspace)
		if err != nil {
			return err
		}
		return resolver.LockAgentWorkAuthority(ctx, confined, snapshot)
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale authenticated owner mapping err = %v, want ErrConflict", err)
	}
	rotated, err := resolver.ObserveAgentWorkAuthority(
		ctx, tenant, workspace, ownerID.String(), ownerExternal,
	)
	if err != nil || rotated.Eligible {
		t.Fatalf("old authenticated ActorRef after owner rotation = %#v, %v", rotated, err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		owner, err := sc.Identities().Get(ctx, ownerID)
		if err != nil {
			return err
		}
		owner.ExternalID = ownerExternal
		_, err = sc.Identities().Update(ctx, owner)
		return err
	}); err != nil {
		t.Fatalf("restore authority owner ExternalID: %v", err)
	}
	snapshot, err = resolver.ObserveAgentWorkAuthority(
		ctx, tenant, workspace, ownerID.String(), ownerExternal,
	)
	if err != nil || !snapshot.Eligible {
		t.Fatalf("refresh restored owner snapshot = %#v, %v", snapshot, err)
	}

	// The lifecycle names a sponsor by ExternalID. A duplicate inserted after
	// Observe changes that predicate without changing the selected sponsor row's
	// version, so Lock must re-count it after acquiring the identity-table fence.
	var duplicateSponsorID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		duplicate, err := sc.Identities().Create(ctx, model.Identity{
			Name: "ambiguous authority sponsor", Kind: "user", ExternalID: sponsorExternal,
			Provider: "test", Metadata: map[string]any{
				"principal_type": "human", "disabled": false,
			},
		})
		duplicateSponsorID = duplicate.ID
		return err
	}); err != nil {
		t.Fatalf("insert ambiguous authority sponsor: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, workspace)
		if err != nil {
			return err
		}
		return resolver.LockAgentWorkAuthority(ctx, confined, snapshot)
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("ambiguous sponsor after Observe err = %v, want ErrConflict", err)
	}
	if result, err := coreengine.ActivateDirectoryWriter(
		ctx, st, cfg, coreengine.DirectoryWriterActivationRequest{
			ExpectedGeneration: 1,
			WritersUpgraded:    true,
			WritersDrained:     true,
			Actor:              "test:workkernel-agent-lifecycle",
			Reason:             "exercise definitive retirement fixture",
		},
	); err != nil || !result.Changed ||
		result.After.ControlMode != store.DirectoryControlEnforced {
		t.Fatalf("activate directory writer for retirement fixture = %+v, %v", result, err)
	}
	if _, err := coreengine.RetireDirectoryPrincipal(
		ctx, st, coreengine.RetireDirectoryPrincipalRequest{
			TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
			SourceID: duplicateSponsorID, ExpectedVersion: 1,
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
		},
	); err != nil {
		t.Fatalf("remove ambiguous authority sponsor fixture: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, workspace)
		if err != nil {
			return err
		}
		return resolver.LockAgentWorkAuthority(ctx, confined, snapshot)
	}); err != nil {
		t.Fatalf("unique sponsor no-fire after duplicate removal: %v", err)
	}

	// FIRE: changing the real governance row after observation invalidates the
	// opaque version witness even though the WorkItem transaction sees only its
	// confined repositories.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.nhi_lifecycle")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{
			Column: "identity_ref", Op: model.OpEq, Value: ownerExternal,
		}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return errors.New("real lifecycle row not found")
		}
		rows[0]["enforcement"] = "blocked"
		_, err = repo.Update(ctx, rows[0])
		return err
	}); err != nil {
		t.Fatalf("block real lifecycle: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, workspace)
		if err != nil {
			return err
		}
		return resolver.LockAgentWorkAuthority(ctx, confined, snapshot)
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale real lifecycle snapshot err = %v, want ErrConflict", err)
	}
	fresh, err := resolver.ObserveAgentWorkAuthority(
		ctx, tenant, workspace, ownerID.String(), ownerExternal,
	)
	if err != nil || fresh.Eligible {
		t.Fatalf("fresh blocked lifecycle snapshot = %#v, %v", fresh, err)
	}
}

func setWorkAgentStatus(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	identityID model.ID,
	status model.LifecycleStatus,
) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		agents, _, err := sc.Agents().List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: "identity_id", Op: model.OpEq, Value: identityID.String(),
		}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(agents) != 1 {
			return errors.New("work agent not found")
		}
		agent := agents[0]
		agent.Status = status
		_, err = sc.Agents().Update(context.Background(), agent)
		return err
	}); err != nil {
		t.Fatalf("set work agent status: %v", err)
	}
}

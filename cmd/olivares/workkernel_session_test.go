// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// This file exists because every other test of the K2 session identity seam
// substitutes the resolver. The module suite injects a test WorkIdentityResolver
// and the HTTP acceptance test does too, so all of them prove the module's half
// and NONE of them can see the half wired here — which is exactly how the
// composition root came to resolve a canonical sid against a core model.Session
// primary key, a lookup that can never succeed, leaving owner_kind="session"
// answering not-found before any authorization ran.
//
// It is the same lesson as the M63-M91 matrix one level down: a subject that is
// not the one that runs in production. The subject here IS the one boot wires
// (boot.go, UseWorkIdentityResolver).

func newSessionResolverFixture(t *testing.T) (workIdentityResolver, *sessions.Module, store.Store, model.TenantID, model.ID) {
	t.Helper()
	ctx := context.Background()
	mod := sessions.New()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, mod.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mod.UseData(api.NewModuleData(st))

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "k2", Slug: "k2", Status: model.StatusActive})
		if err != nil {
			return err
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	var workspace model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err == nil {
			workspace = ws.ID
		}
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	return workIdentityResolver{st: st, sessions: mod}, mod, st, tenant, workspace
}

// TestWiredResolverResolvesACanonicalSID is the direct regression test for the
// defect. Before the fix, a sid minted by the identity plane resolved to
// not-found here, because the resolver looked it up as a core model.Session
// primary key and nothing ever creates one with that id.
func TestWiredResolverResolvesACanonicalSID(t *testing.T) {
	resolver, mod, _, tenant, workspace := newSessionResolverFixture(t)
	ctx := context.Background()

	sid, err := mod.ResolveSession(ctx, tenant, sessions.SessionBinding{
		Provider: "claude", ExternalID: "wired-resolver-1",
		Origin: sessions.OriginObserved, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve canonical sid: %v", err)
	}

	got, err := resolver.ResolveParticipant(ctx, tenant, workspace, "session", sid)
	if err != nil {
		t.Fatalf("resolve session participant = %v, want it to resolve at all", err)
	}
	if got.Kind != "session" || got.CanonicalRef != sid {
		t.Fatalf("wired resolver did not recognize the canonical sid: %#v", got)
	}
	// Unscoped identity reads as the tenant's DEFAULT workspace, the same
	// soft-isolation model.Session and model.Agent already use.
	if !got.WorkspaceEligible {
		t.Fatalf("unscoped session is not eligible in the default workspace: %#v", got)
	}

	// NO-FIRE: a sid nobody ever resolved is not eligible, and says so as a
	// participant rather than as an error, so checkParticipant can answer
	// owner_ineligible instead of a store failure.
	unknown, err := resolver.ResolveParticipant(ctx, tenant, workspace, "session", "osn_"+model.NewID().String())
	if err != nil {
		t.Fatalf("unknown sid = %v, want a not-eligible participant", err)
	}
	if unknown.Active || unknown.WorkspaceEligible || unknown.CanonicalRef != "" {
		t.Fatalf("unknown sid resolved to something: %#v", unknown)
	}

	// NO-FIRE: a bare uuid is NOT a canonical sid. Accepting one is what let the
	// two id spaces be confused in the first place.
	bare, err := resolver.ResolveParticipant(ctx, tenant, workspace, "session", model.NewID().String())
	if err != nil {
		t.Fatalf("bare uuid = %v, want a not-eligible participant", err)
	}
	if bare.WorkspaceEligible || bare.CanonicalRef != "" {
		t.Fatalf("a bare uuid was accepted as a canonical sid: %#v", bare)
	}
}

// TestWiredResolverScopesASessionToItsOwnWorkspace proves the plane OWNS the
// workspace dimension the hub chose (option ii) rather than borrowing it.
func TestWiredResolverScopesASessionToItsOwnWorkspace(t *testing.T) {
	resolver, mod, st, tenant, defaultWS := newSessionResolverFixture(t)
	ctx := context.Background()

	var other model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "OTHER", Slug: "other", Status: model.StatusActive,
		})
		if err == nil {
			other = ws.ID
		}
		return err
	}); err != nil {
		t.Fatalf("create second workspace: %v", err)
	}

	sid, err := mod.ResolveSession(ctx, tenant, sessions.SessionBinding{
		Provider: "claude", ExternalID: "wired-resolver-scoped",
		Origin: sessions.OriginObserved, At: time.Now().UTC(), WorkspaceID: other,
	})
	if err != nil {
		t.Fatalf("resolve scoped sid: %v", err)
	}

	inOther, err := resolver.ResolveParticipant(ctx, tenant, other, "session", sid)
	if err != nil || !inOther.WorkspaceEligible {
		t.Fatalf("scoped session in its own workspace = %#v, %v", inOther, err)
	}
	// NO-FIRE: the scope is a boundary, not a label.
	inDefault, err := resolver.ResolveParticipant(ctx, tenant, defaultWS, "session", sid)
	if err != nil {
		t.Fatalf("scoped session elsewhere: %v", err)
	}
	if inDefault.WorkspaceEligible {
		t.Fatalf("a session scoped to another workspace was eligible here: %#v", inDefault)
	}
}

// TestWiredResolverAnswersSessionActsForAgentFromItsOwnAlias proves the second
// question resolves through facts this plane wrote — sid -> operated alias ->
// run_ref -> sessions_run.agent_ref — instead of guessing a core Session key.
func TestWiredResolverAnswersSessionActsForAgentFromItsOwnAlias(t *testing.T) {
	resolver, mod, st, tenant, _ := newSessionResolverFixture(t)
	ctx := context.Background()
	// owner_ref is the canonical core Identity.ID, while sessions_run.agent_ref
	// carries Principal.AgentIdentity: the identity's external_id. Keep the two
	// namespaces deliberately different so this composition test cannot pass by
	// accidentally comparing the same UUID on both sides.
	agentIdentity, agentRef := seedCanonicalWorkAgent(t, st, tenant, "nhi:agent:wired-owner")
	runRef := model.NewID().String()

	sid, err := mod.ResolveSession(ctx, tenant, sessions.SessionBinding{
		Provider: sessions.ProviderOperated, ExternalID: runRef,
		Origin: sessions.OriginOperated, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve operated sid: %v", err)
	}
	seedRunWithAgent(t, st, tenant, runRef, agentRef)

	acts, err := resolver.SessionActsForAgent(ctx, tenant, sid, agentIdentity.String())
	if err != nil {
		t.Fatalf("SessionActsForAgent = %v, want it to resolve", err)
	}
	if !acts {
		t.Fatal("the agent that drives this operated session was not recognized")
	}
	if matches, err := resolver.AuthenticatedAgentMatches(
		ctx, tenant, agentIdentity.String(), agentRef,
	); err != nil || !matches {
		t.Fatalf("canonical owner vs authenticated external ref = %v, %v; want true",
			matches, err)
	}

	// NO-FIRE, three ways: a sibling canonical identity, an unknown sid, and a
	// session with no operated alias all answer false without erroring.
	siblingIdentity, _ := seedCanonicalWorkAgent(t, st, tenant, "nhi:agent:wired-sibling")
	if matches, err := resolver.AuthenticatedAgentMatches(
		ctx, tenant, siblingIdentity.String(), agentRef,
	); err != nil || matches {
		t.Fatalf("sibling canonical owner matched authenticated external ref: %v, %v",
			matches, err)
	}
	for name, tc := range map[string]struct{ sid, agent string }{
		"another agent":  {sid, siblingIdentity.String()},
		"unknown sid":    {"osn_" + model.NewID().String(), agentIdentity.String()},
		"empty agentRef": {sid, ""},
	} {
		got, err := resolver.SessionActsForAgent(ctx, tenant, tc.sid, tc.agent)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got {
			t.Fatalf("%s: reported as acting for the agent", name)
		}
	}

	observed, err := mod.ResolveSession(ctx, tenant, sessions.SessionBinding{
		Provider: "claude", ExternalID: "not-operated", At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve observed sid: %v", err)
	}
	if acts, err := resolver.SessionActsForAgent(ctx, tenant, observed, agentIdentity.String()); err != nil || acts {
		t.Fatalf("an observed session vouched for an agent: acts=%v err=%v", acts, err)
	}

	// An external ref is not a canonical owner_ref and must never become an
	// alternate authorization spelling. An unknown canonical identity similarly
	// cannot vouch for the run (its lookup error is honest missing evidence).
	if acts, _ := resolver.SessionActsForAgent(ctx, tenant, sid, agentRef); acts {
		t.Fatal("the raw identity external_id was accepted as canonical owner_ref")
	}
	if acts, err := resolver.SessionActsForAgent(ctx, tenant, sid, model.NewID().String()); err == nil || acts {
		t.Fatalf("unknown canonical identity: acts=%v err=%v, want false plus lookup error", acts, err)
	}
}

// seedCanonicalWorkAgent creates the two linked roster facts used by the
// composition resolver: WorkItem owner_ref names Identity.ID, while an
// authenticated runtime records Identity.ExternalID in sessions_run.agent_ref.
func seedCanonicalWorkAgent(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	externalRef string,
) (model.ID, string) {
	t.Helper()
	var identityID model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Create(context.Background(), model.Identity{
			Name: externalRef, Kind: "agent_nhi", ExternalID: externalRef, Provider: "test",
		})
		if err != nil {
			return err
		}
		identityID = identity.ID
		_, err = sc.Agents().Create(context.Background(), model.Agent{
			Name: externalRef, Kind: "test", ExternalID: externalRef,
			Status: model.StatusActive, IdentityID: identityID,
		})
		return err
	}); err != nil {
		t.Fatalf("seed canonical work agent: %v", err)
	}
	return identityID, externalRef
}

// seedRunWithAgent writes the sessions_run row the operated alias points at,
// with the agent attribution setRunGovFacts records at launch.
func seedRunWithAgent(t *testing.T, st store.Store, tenant model.TenantID, runRef, agentRef string) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"run_ref": runRef, "transport": "stream-json", "permission_mode": "default",
			"isolation": "native", "state": "running", "last_event_seq": int64(0),
			"agent_ref": agentRef,
		})
		return err
	}); err != nil {
		t.Fatalf("seed run row: %v", err)
	}
}

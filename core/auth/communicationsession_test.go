// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func communicationSessionSystemActor(t *testing.T) auth.Principal {
	t.Helper()
	actor, err := auth.NewSystemOperator("test:sessions-runtime", "exercise the dedicated communication-session issuer")
	if err != nil {
		t.Fatalf("system actor: %v", err)
	}
	return actor
}

func communicationSessionWorkspace(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
) model.ID {
	t.Helper()
	var workspace model.ID
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(context.Background())
		if err == nil {
			workspace = ws.ID
		}
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	return workspace
}

func communicationSessionSpec(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
) auth.CommunicationSessionCredentialSpec {
	t.Helper()
	return auth.CommunicationSessionCredentialSpec{
		Tenant: tenant, WorkspaceID: communicationSessionWorkspace(t, st, tenant),
		SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(),
		AgentRef: "agent:" + model.NewID().String(), ClaimFence: 1,
	}
}

func TestCommunicationSessionCredentialHasExactHardCeilingAndConfinement(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "communication-session-ceiling")
	foreign := provisionTenant(t, st, "communication-session-foreign")
	a := auth.NewAuthenticator(st, nil)
	spec := communicationSessionSpec(t, st, tenant)

	issued, err := a.IssueCommunicationSessionCredential(
		ctx, communicationSessionSystemActor(t), spec,
	)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	p, err := a.Authenticate(ctx, issued.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if p.SessionIdentity != spec.SessionRef || p.SessionWorkspaceID != spec.WorkspaceID ||
		p.SessionRunRef != spec.RunRef || p.SessionFence != spec.ClaimFence ||
		p.AgentIdentity != spec.AgentRef || !p.IsCommunicationSessionCredential() ||
		!p.IsPurposeRestricted() {
		t.Fatalf("principal binding = sid %q workspace %s run %q fence %d agent %q communication=%v restricted=%v",
			p.SessionIdentity, p.SessionWorkspaceID, p.SessionRunRef, p.SessionFence,
			p.AgentIdentity, p.IsCommunicationSessionCredential(), p.IsPurposeRestricted())
	}
	if issued.Tenant != spec.Tenant || issued.WorkspaceID != spec.WorkspaceID ||
		issued.SessionRef != spec.SessionRef || issued.RunRef != spec.RunRef ||
		issued.AgentRef != spec.AgentRef || issued.ClaimFence != spec.ClaimFence {
		t.Fatalf("issuer returned incorrect binding: %#v", issued)
	}
	if ws, ok := p.ConfinedWorkspaceIn(tenant); !ok || ws != spec.WorkspaceID {
		t.Fatalf("workspace confinement = %s,%v; want %s,true", ws, ok, spec.WorkspaceID)
	}
	if _, ok := p.ConfinedWorkspaceIn(foreign); ok {
		t.Fatal("credential carried its workspace confinement into another tenant")
	}

	want := []auth.Permission{
		auth.CommunicationSessionDeliveryRead,
		auth.CommunicationSessionDeliveryWrite,
		auth.CommunicationSessionHandoffResponseWrite,
		auth.CommunicationSessionMessageSendWrite,
	}
	got, restricted := p.PurposePermissionsIn(tenant)
	if !restricted || len(got) != len(want) {
		t.Fatalf("purpose permissions = %v,%v; want exact four", got, restricted)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("purpose permissions = %v, want %v", got, want)
		}
	}

	// Even a deliberately permissive scoped engine cannot add a fifth bit.
	az := auth.NewAuthorizer(nil, auth.WithScopedGrants(grantingScopedAuthorizer{}))
	for _, permission := range want {
		if !az.Allowed(ctx, p, permission, tenant) {
			t.Errorf("exact permission %q denied", permission)
		}
	}
	for _, permission := range []auth.Permission{
		"sessions:channel:read", "sessions:work:write", "sessions:lease:write",
		"sessions:run:write", "sessions:decision-response:write", "token:write",
	} {
		if az.Allowed(ctx, p, permission, tenant) {
			t.Errorf("hard ceiling widened to %q", permission)
		}
	}
	if az.Allowed(ctx, p, auth.CommunicationSessionDeliveryRead, foreign) {
		t.Error("credential crossed its tenant binding")
	}

	simulated, found, err := a.PrincipalForToken(ctx, issued.ID)
	if err != nil || !found || simulated.SessionIdentity != spec.SessionRef ||
		simulated.SessionWorkspaceID != spec.WorkspaceID ||
		simulated.SessionRunRef != spec.RunRef || simulated.SessionFence != spec.ClaimFence ||
		simulated.AgentIdentity != spec.AgentRef || !simulated.IsCommunicationSessionCredential() {
		t.Fatalf("PrincipalForToken = found=%v principal=%#v err=%v", found, simulated, err)
	}
	if ws, ok := simulated.ConfinedWorkspaceIn(tenant); !ok || ws != spec.WorkspaceID {
		t.Fatalf("simulated confinement = %s,%v; want %s,true", ws, ok, spec.WorkspaceID)
	}
	population, err := a.TenantPrincipals(ctx, tenant, auth.AAL3)
	if err != nil {
		t.Fatalf("TenantPrincipals: %v", err)
	}
	found = false
	for _, candidate := range population {
		if candidate.CredID != issued.ID {
			continue
		}
		found = candidate.IsCommunicationSessionCredential() &&
			candidate.SessionWorkspaceID == spec.WorkspaceID
		if ws, ok := candidate.ConfinedWorkspaceIn(tenant); !ok || ws != spec.WorkspaceID {
			t.Fatalf("population confinement = %s,%v; want %s,true", ws, ok, spec.WorkspaceID)
		}
	}
	if !found {
		t.Fatal("TenantPrincipals omitted or widened the communication-session principal")
	}
}

func TestCommunicationSessionCredentialIssuerAndStoredShapeDenyClosed(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "communication-session-shape")
	a := auth.NewAuthenticator(st, nil)
	actor := communicationSessionSystemActor(t)
	valid := communicationSessionSpec(t, st, tenant)

	nonSystem := auth.ScopedPrincipal(model.NewID(), "tenant admin", tenant, auth.RoleAdmin)
	if _, err := a.IssueCommunicationSessionCredential(ctx, nonSystem, valid); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Fatalf("non-system issue = %v, want role ceiling", err)
	}
	invalid := []struct {
		name   string
		mutate func(*auth.CommunicationSessionCredentialSpec)
	}{
		{"system tenant", func(s *auth.CommunicationSessionCredentialSpec) { s.Tenant = model.SystemTenantID }},
		{"zero workspace", func(s *auth.CommunicationSessionCredentialSpec) { s.WorkspaceID = "" }},
		{"malformed workspace", func(s *auth.CommunicationSessionCredentialSpec) { s.WorkspaceID = "not-a-uuid" }},
		{"malformed sid", func(s *auth.CommunicationSessionCredentialSpec) { s.SessionRef = "osn_not-a-uuid" }},
		{"malformed run", func(s *auth.CommunicationSessionCredentialSpec) { s.RunRef = "run-not-uuid" }},
		{"zero fence", func(s *auth.CommunicationSessionCredentialSpec) { s.ClaimFence = 0 }},
		{"unsafe agent", func(s *auth.CommunicationSessionCredentialSpec) { s.AgentRef = "agent\nforged" }},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid
			tc.mutate(&spec)
			if _, err := a.IssueCommunicationSessionCredential(ctx, actor, spec); !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("issue = %v, want invalid token", err)
			}
		})
	}

	const (
		uuidV1  = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
		uuidV4  = "550e8400-e29b-41d4-a716-446655440000"
		uuidNil = "00000000-0000-0000-0000-000000000000"
	)
	uuidShapeCases := []struct {
		name        string
		mutateSpec  func(*auth.CommunicationSessionCredentialSpec)
		mutateToken func(*model.APIToken)
	}{
		{"tenant v1", func(s *auth.CommunicationSessionCredentialSpec) { s.Tenant = model.TenantID(uuidV1) }, func(tok *model.APIToken) { tok.BoundTenantID = model.TenantID(uuidV1) }},
		{"tenant v4", func(s *auth.CommunicationSessionCredentialSpec) { s.Tenant = model.TenantID(uuidV4) }, func(tok *model.APIToken) { tok.BoundTenantID = model.TenantID(uuidV4) }},
		{"tenant uppercase", func(s *auth.CommunicationSessionCredentialSpec) {
			s.Tenant = model.TenantID(strings.ToUpper(s.Tenant.String()))
		}, func(tok *model.APIToken) {
			tok.BoundTenantID = model.TenantID(strings.ToUpper(tok.BoundTenantID.String()))
		}},
		{"tenant nil", func(s *auth.CommunicationSessionCredentialSpec) { s.Tenant = model.TenantID(uuidNil) }, func(tok *model.APIToken) { tok.BoundTenantID = model.TenantID(uuidNil) }},
		{"workspace v1", func(s *auth.CommunicationSessionCredentialSpec) { s.WorkspaceID = model.ID(uuidV1) }, func(tok *model.APIToken) { tok.WorkspaceID = model.ID(uuidV1) }},
		{"workspace v4", func(s *auth.CommunicationSessionCredentialSpec) { s.WorkspaceID = model.ID(uuidV4) }, func(tok *model.APIToken) { tok.WorkspaceID = model.ID(uuidV4) }},
		{"workspace uppercase", func(s *auth.CommunicationSessionCredentialSpec) {
			s.WorkspaceID = model.ID(strings.ToUpper(s.WorkspaceID.String()))
		}, func(tok *model.APIToken) { tok.WorkspaceID = model.ID(strings.ToUpper(tok.WorkspaceID.String())) }},
		{"workspace nil", func(s *auth.CommunicationSessionCredentialSpec) { s.WorkspaceID = model.ID(uuidNil) }, func(tok *model.APIToken) { tok.WorkspaceID = model.ID(uuidNil) }},
		{"sid v1", func(s *auth.CommunicationSessionCredentialSpec) { s.SessionRef = "osn_" + uuidV1 }, func(tok *model.APIToken) { tok.SessionRef = "osn_" + uuidV1 }},
		{"sid v4", func(s *auth.CommunicationSessionCredentialSpec) { s.SessionRef = "osn_" + uuidV4 }, func(tok *model.APIToken) { tok.SessionRef = "osn_" + uuidV4 }},
		{"sid uppercase", func(s *auth.CommunicationSessionCredentialSpec) {
			s.SessionRef = "osn_" + strings.ToUpper(strings.TrimPrefix(s.SessionRef, "osn_"))
		}, func(tok *model.APIToken) {
			tok.SessionRef = "osn_" + strings.ToUpper(strings.TrimPrefix(tok.SessionRef, "osn_"))
		}},
		{"sid nil", func(s *auth.CommunicationSessionCredentialSpec) { s.SessionRef = "osn_" + uuidNil }, func(tok *model.APIToken) { tok.SessionRef = "osn_" + uuidNil }},
		{"run v1", func(s *auth.CommunicationSessionCredentialSpec) { s.RunRef = uuidV1 }, func(tok *model.APIToken) { tok.SessionRunRef = uuidV1 }},
		{"run v4", func(s *auth.CommunicationSessionCredentialSpec) { s.RunRef = uuidV4 }, func(tok *model.APIToken) { tok.SessionRunRef = uuidV4 }},
		{"run uppercase", func(s *auth.CommunicationSessionCredentialSpec) { s.RunRef = strings.ToUpper(s.RunRef) }, func(tok *model.APIToken) { tok.SessionRunRef = strings.ToUpper(tok.SessionRunRef) }},
		{"run nil", func(s *auth.CommunicationSessionCredentialSpec) { s.RunRef = uuidNil }, func(tok *model.APIToken) { tok.SessionRunRef = uuidNil }},
	}
	// A non-zero malformed tenant cannot reach the parser through the guarded
	// writer: fencing its directory epoch must reject the write before source DML.
	writerRejects := map[string]bool{
		"tenant v1":        true,
		"tenant v4":        true,
		"tenant uppercase": true,
	}
	for _, tc := range uuidShapeCases {
		t.Run("issuer "+tc.name, func(t *testing.T) {
			spec := valid
			tc.mutateSpec(&spec)
			if _, err := a.IssueCommunicationSessionCredential(ctx, actor, spec); !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("issue = %v, want invalid token", err)
			}
		})
		t.Run("parser "+tc.name, func(t *testing.T) {
			spec := valid
			spec.SessionRef = "osn_" + model.NewID().String()
			spec.RunRef = model.NewID().String()
			issued, err := a.IssueCommunicationSessionCredential(ctx, actor, spec)
			if err != nil {
				t.Fatalf("issue valid control: %v", err)
			}
			err = st.AuthMutate(ctx, func(as store.AuthScope) error {
				row, err := as.Tokens().Get(ctx, issued.ID)
				if err != nil {
					return err
				}
				tc.mutateToken(&row)
				_, err = as.Tokens().Update(ctx, row)
				return err
			})
			if writerRejects[tc.name] {
				if !errors.Is(err, store.ErrDirectoryUnavailable) {
					t.Fatalf("corrupt stored binding = %v, want directory unavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("corrupt stored binding: %v", err)
			}
			if _, err := a.Authenticate(ctx, issued.Token); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("authenticate = %v, want unauthenticated", err)
			}
			if _, found, err := a.PrincipalForToken(ctx, issued.ID); err != nil || found {
				t.Fatalf("PrincipalForToken = found=%v err=%v, want hidden malformed row", found, err)
			}
		})
	}

	// Empty AgentRef is a valid exact server-authored absence, not a fallback to
	// the system actor or a display name.
	emptyAgent := valid
	emptyAgent.SessionRef = "osn_" + model.NewID().String()
	emptyAgent.RunRef = model.NewID().String()
	emptyAgent.AgentRef = ""
	issued, err := a.IssueCommunicationSessionCredential(ctx, actor, emptyAgent)
	if err != nil {
		t.Fatalf("empty agent issue: %v", err)
	}
	p, err := a.Authenticate(ctx, issued.Token)
	if err != nil || p.AgentIdentity != "" || !p.IsCommunicationSessionCredential() {
		t.Fatalf("empty agent principal = agent %q communication=%v err=%v",
			p.AgentIdentity, p.IsCommunicationSessionCredential(), err)
	}
	if strings.Contains(issued.Token, emptyAgent.SessionRef) || strings.Contains(issued.Token, emptyAgent.RunRef) {
		t.Fatal("opaque bearer encoded a runtime binding")
	}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		row, err := as.Tokens().Get(ctx, issued.ID)
		if err != nil {
			return err
		}
		if strings.Contains(row.Name, emptyAgent.SessionRef) || strings.Contains(row.Name, emptyAgent.RunRef) ||
			strings.Contains(row.Name, emptyAgent.WorkspaceID.String()) {
			t.Errorf("Name encoded an authority binding: %q", row.Name)
		}
		if !row.UserID.IsZero() {
			t.Errorf("system-issued credential user_id = %s, want zero", row.UserID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Public IssueToken has no binding inputs and persists all binding columns
	// empty. Directly corrupting any one onto an ordinary token denies both real
	// authentication and simulated lookup.
	super := mustSuperadmin(t, ctx, a)
	for _, tc := range []struct {
		name   string
		mutate func(*model.APIToken)
	}{
		{"session_ref", func(tok *model.APIToken) { tok.SessionRef = valid.SessionRef }},
		{"workspace_id", func(tok *model.APIToken) { tok.WorkspaceID = valid.WorkspaceID }},
		{"session_run_ref", func(tok *model.APIToken) { tok.SessionRunRef = valid.RunRef }},
		{"session_fence", func(tok *model.APIToken) { tok.SessionFence = valid.ClaimFence }},
	} {
		t.Run("ordinary "+tc.name, func(t *testing.T) {
			raw, stored, err := a.IssueToken(ctx, super, auth.TokenSpec{
				Name: "ordinary-" + tc.name, BoundTenant: tenant, Role: auth.RoleEditor,
			})
			if err != nil {
				t.Fatal(err)
			}
			if stored.SessionRef != "" || !stored.WorkspaceID.IsZero() ||
				stored.SessionRunRef != "" || stored.SessionFence != 0 {
				t.Fatalf("public issuer populated private binding columns: %#v", stored)
			}
			if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
				row, err := as.Tokens().Get(ctx, stored.ID)
				if err != nil {
					return err
				}
				tc.mutate(&row)
				_, err = as.Tokens().Update(ctx, row)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Authenticate(ctx, raw); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("ordinary token with %s = %v, want unauthenticated", tc.name, err)
			}
			if _, found, err := a.PrincipalForToken(ctx, stored.ID); err != nil || found {
				t.Fatalf("PrincipalForToken ordinary %s = found=%v err=%v", tc.name, found, err)
			}
		})
	}

	// The newly-added columns cannot be smuggled onto the older work-session
	// shape either: each purpose has its own exact parser and durable contract.
	work, err := a.IssueWorkSessionCredential(ctx, actor, auth.WorkSessionCredentialSpec{
		Tenant: tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), ClaimFence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		row, err := as.Tokens().Get(ctx, work.ID)
		if err != nil {
			return err
		}
		row.WorkspaceID = valid.WorkspaceID
		_, err = as.Tokens().Update(ctx, row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, work.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("work-session accepted communication binding column: %v", err)
	}
	if _, found, err := a.PrincipalForToken(ctx, work.ID); err != nil || found {
		t.Fatalf("work-session lookup accepted communication binding column: found=%v err=%v", found, err)
	}
}

func TestCommunicationSessionCredentialRenewAndRevokeRequireExactBinding(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "communication-session-binding")
	otherTenant := provisionTenant(t, st, "communication-session-binding-other")
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	actor := communicationSessionSystemActor(t)
	spec := communicationSessionSpec(t, st, tenant)
	issued, err := a.IssueCommunicationSessionCredential(ctx, actor, spec)
	if err != nil {
		t.Fatal(err)
	}
	if ttl := issued.ExpiresAt.Sub(clock.Now().Time()); ttl <= 0 ||
		ttl > auth.DefaultCommunicationSessionCredentialTTL {
		t.Fatalf("issued TTL = %s, want (0,%s]", ttl, auth.DefaultCommunicationSessionCredentialTTL)
	}

	mismatches := []struct {
		name   string
		mutate func(*auth.CommunicationSessionCredentialSpec)
	}{
		{"tenant", func(s *auth.CommunicationSessionCredentialSpec) { s.Tenant = otherTenant }},
		{"workspace", func(s *auth.CommunicationSessionCredentialSpec) { s.WorkspaceID = model.NewID() }},
		{"session", func(s *auth.CommunicationSessionCredentialSpec) { s.SessionRef = "osn_" + model.NewID().String() }},
		{"run", func(s *auth.CommunicationSessionCredentialSpec) { s.RunRef = model.NewID().String() }},
		{"agent", func(s *auth.CommunicationSessionCredentialSpec) { s.AgentRef = "agent:" + model.NewID().String() }},
		{"fence", func(s *auth.CommunicationSessionCredentialSpec) { s.ClaimFence++ }},
	}
	for _, tc := range mismatches {
		t.Run(tc.name, func(t *testing.T) {
			crossed := spec
			tc.mutate(&crossed)
			if _, err := a.RenewCommunicationSessionCredential(ctx, actor, issued.ID, crossed); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("crossed renew = %v, want unauthenticated", err)
			}
			if err := a.RevokeCommunicationSessionCredential(ctx, actor, issued.ID, crossed); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("crossed revoke = %v, want unauthenticated", err)
			}
			if _, err := a.Authenticate(ctx, issued.Token); err != nil {
				t.Fatalf("crossed operation harmed exact bearer: %v", err)
			}
		})
	}

	clock.advance(20 * time.Minute)
	if _, err := a.RenewCommunicationSessionCredential(ctx, actor, issued.ID, spec); err != nil {
		t.Fatalf("exact renew: %v", err)
	}
	clock.advance(11 * time.Minute)
	if _, err := a.Authenticate(ctx, issued.Token); err != nil {
		t.Fatalf("renewed bearer: %v", err)
	}
	if err := a.RevokeCommunicationSessionCredential(ctx, actor, model.NewID(), spec); err != nil {
		t.Fatalf("missing handle revoke: %v", err)
	}
	if err := a.RevokeCommunicationSessionCredential(ctx, actor, issued.ID, spec); err != nil {
		t.Fatalf("exact revoke: %v", err)
	}
	if err := a.RevokeCommunicationSessionCredential(ctx, actor, issued.ID, spec); err != nil {
		t.Fatalf("repeat exact revoke: %v", err)
	}
	if _, err := a.RenewCommunicationSessionCredential(ctx, actor, issued.ID, spec); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked credential renewed: %v", err)
	}
	if _, err := a.Authenticate(ctx, issued.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked bearer = %v, want unauthenticated", err)
	}
}

func TestCommunicationSessionCredentialIssueSupersedesPriorRunGeneration(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "communication-session-supersede")
	a := auth.NewAuthenticator(st, nil)
	actor := communicationSessionSystemActor(t)
	oldSpec := communicationSessionSpec(t, st, tenant)
	siblingSID := oldSpec
	siblingSID.SessionRef = "osn_" + model.NewID().String()
	sameSIDOtherRun := oldSpec
	sameSIDOtherRun.RunRef = model.NewID().String()

	old, err := a.IssueCommunicationSessionCredential(ctx, actor, oldSpec)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*auth.CommunicationSessionCredentialSpec){
		"workspace": func(spec *auth.CommunicationSessionCredentialSpec) { spec.WorkspaceID = model.NewID() },
		"agent":     func(spec *auth.CommunicationSessionCredentialSpec) { spec.AgentRef = "agent:" + model.NewID().String() },
	} {
		crossed := oldSpec
		mutate(&crossed)
		if _, err := a.IssueCommunicationSessionCredential(ctx, actor, crossed); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("same-fence crossed %s issue = %v, want unauthenticated", name, err)
		}
		if _, err := a.Authenticate(ctx, old.Token); err != nil {
			t.Fatalf("same-fence crossed %s harmed original bearer: %v", name, err)
		}
	}
	sibling, err := a.IssueCommunicationSessionCredential(ctx, actor, siblingSID)
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := a.IssueCommunicationSessionCredential(ctx, actor, sameSIDOtherRun)
	if err != nil {
		t.Fatal(err)
	}
	successorSpec := oldSpec
	successorSpec.WorkspaceID = model.NewID()
	successorSpec.AgentRef = "agent:" + model.NewID().String()
	successorSpec.ClaimFence++
	successor, err := a.IssueCommunicationSessionCredential(ctx, actor, successorSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, old.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("superseded bearer = %v, want unauthenticated", err)
	}
	if _, err := a.IssueCommunicationSessionCredential(ctx, actor, oldSpec); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("stale fence issue = %v, want unauthenticated", err)
	}
	for name, token := range map[string]string{
		"successor": successor.Token, "sibling SID": sibling.Token, "other run": otherRun.Token,
	} {
		if _, err := a.Authenticate(ctx, token); err != nil {
			t.Errorf("%s bearer: %v", name, err)
		}
	}
}

func TestCommunicationSessionCredentialConcurrentIssueLeavesOneActiveBearer(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "communication-session-concurrent-issue")
	a := auth.NewAuthenticator(st, nil)
	actor := communicationSessionSystemActor(t)
	spec := communicationSessionSpec(t, st, tenant)

	const racers = 8
	issued := make([]auth.CommunicationSessionCredential, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := range racers {
		go func(i int) {
			defer wg.Done()
			<-start
			issued[i], errs[i] = a.IssueCommunicationSessionCredential(ctx, actor, spec)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("issuer %d: %v", i, err)
		}
	}

	authenticable := 0
	for i, credential := range issued {
		if _, err := a.Authenticate(ctx, credential.Token); err == nil {
			authenticable++
		} else if !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("authenticate issuer %d: %v", i, err)
		}
	}
	if authenticable != 1 {
		t.Fatalf("concurrent exact issuers left %d authenticable bearers, want exactly one", authenticable)
	}

	activeRows := 0
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		rows, page, err := as.Tokens().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "bound_tenant_id", Op: model.OpEq, Value: tenant.String()},
			{Column: "purpose", Op: model.OpEq, Value: auth.CommunicationSessionCredentialPurpose},
			{Column: "session_ref", Op: model.OpEq, Value: spec.SessionRef},
			{Column: "session_run_ref", Op: model.OpEq, Value: spec.RunRef},
		}, Limit: racers + 1})
		if err != nil {
			return err
		}
		if page.HasMore {
			t.Errorf("exact issuer rows were truncated")
		}
		if len(rows) != racers {
			t.Errorf("stored rows = %d, want %d successful issuers", len(rows), racers)
		}
		for _, row := range rows {
			if !row.Revoked && row.ExpiresAt != nil {
				activeRows++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if activeRows != 1 {
		t.Fatalf("concurrent exact issuers left %d active rows, want exactly one", activeRows)
	}
}

func TestCommunicationSessionCredentialExpiryBoundaryIsClosed(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "communication-session-expiry")
	clock := newStepClock()
	a := auth.NewAuthenticator(st, clock)
	actor := communicationSessionSystemActor(t)
	spec := communicationSessionSpec(t, st, tenant)
	issued, err := a.IssueCommunicationSessionCredential(ctx, actor, spec)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(auth.DefaultCommunicationSessionCredentialTTL - time.Nanosecond)
	if _, err := a.Authenticate(ctx, issued.Token); err != nil {
		t.Fatalf("one tick before expiry: %v", err)
	}
	if _, found, err := a.PrincipalForToken(ctx, issued.ID); err != nil || !found {
		t.Fatalf("one tick before expiry lookup = found=%v err=%v", found, err)
	}
	clock.advance(time.Nanosecond)
	if _, err := a.Authenticate(ctx, issued.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("exact expiry authentication = %v, want unauthenticated", err)
	}
	if _, found, err := a.PrincipalForToken(ctx, issued.ID); err != nil || found {
		t.Fatalf("exact expiry lookup = found=%v err=%v", found, err)
	}
	if _, err := a.RenewCommunicationSessionCredential(ctx, actor, issued.ID, spec); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("exact expiry renewal = %v, want unauthenticated", err)
	}
}

func TestCommunicationSessionCredentialCannotCrossCredentialDerivationEdges(t *testing.T) {
	ctx := context.Background()
	t.Run("token exchange caller subject and actor", func(t *testing.T) {
		f := newExchangeFixture(t)
		spec := auth.CommunicationSessionCredentialSpec{
			Tenant: f.tenant, WorkspaceID: model.NewID(),
			SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(), ClaimFence: 1,
		}
		issued, err := f.a.IssueCommunicationSessionCredential(ctx, communicationSessionSystemActor(t), spec)
		if err != nil {
			t.Fatal(err)
		}
		restricted, err := f.a.Authenticate(ctx, issued.Token)
		if err != nil {
			t.Fatal(err)
		}
		ordinary, err := f.a.Authenticate(ctx, f.adminTok)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.a.ExchangeToken(ctx, restricted, accessReq(f.editorTok)); !errors.Is(err, auth.ErrInvalidExchange) {
			t.Fatalf("restricted caller = %v, want invalid exchange", err)
		}
		if _, err := f.a.ExchangeToken(ctx, ordinary, accessReq(issued.Token)); !errors.Is(err, auth.ErrInvalidExchange) {
			t.Fatalf("restricted subject = %v, want invalid exchange", err)
		}
		req := accessReq(f.editorTok)
		req.ActorToken = issued.Token
		req.ActorTokenType = auth.TokenTypeAccessToken
		if _, err := f.a.ExchangeToken(ctx, ordinary, req); !errors.Is(err, auth.ErrInvalidExchange) {
			t.Fatalf("restricted actor = %v, want invalid exchange", err)
		}
	})

	t.Run("delegation caller and subject", func(t *testing.T) {
		f := newDelegFixture(t)
		spec := auth.CommunicationSessionCredentialSpec{
			Tenant: f.tenant, WorkspaceID: model.NewID(),
			SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(), ClaimFence: 1,
		}
		issued, err := f.a.IssueCommunicationSessionCredential(f.ctx, communicationSessionSystemActor(t), spec)
		if err != nil {
			t.Fatal(err)
		}
		restricted, err := f.a.Authenticate(f.ctx, issued.Token)
		if err != nil {
			t.Fatal(err)
		}
		req := auth.MintDelegationRequest{
			SubjectToken: f.subjectSession, PEPServiceID: f.serviceA.ID, Operations: []string{"messages"},
		}
		if _, _, err := f.a.MintDelegationHandle(f.ctx, restricted, req); !errors.Is(err, auth.ErrInvalidDelegationRequest) {
			t.Fatalf("restricted caller = %v, want invalid request", err)
		}
		req.SubjectToken = issued.Token
		if _, _, err := f.a.MintDelegationHandle(f.ctx, f.admin, req); !errors.Is(err, auth.ErrInvalidDelegationRequest) {
			t.Fatalf("restricted subject = %v, want invalid request", err)
		}
	})
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestOrchestrationSessionCredentialHasSeparateWorkspaceCeiling(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "orchestration-session")
	foreign := provisionTenant(t, st, "orchestration-foreign")
	workspace := communicationSessionWorkspace(t, st, tenant)
	credential, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	expires := model.NewTimestamp(time.Now().Add(time.Hour))
	if err := st.AuthMutate(ctx, func(sc store.AuthScope) error {
		_, err := sc.Tokens().Create(ctx, model.APIToken{
			Name:     "sessions-runtime-orchestration:ppf_" + model.NewID().String() + ":" + model.NewID().String() + ":decision.read,decision.write,work.assign,work.create,work.read,work.review",
			Selector: credential.Selector, SecretHash: credential.SecretHash,
			BoundTenantID: tenant, WorkspaceID: workspace, Role: "orchestration-session",
			Purpose: "orchestration-session", ExpiresAt: &expires,
			SessionRef: "osn_" + model.NewID().String(), SessionRunRef: model.NewID().String(), SessionFence: 1,
			AgentRef: "agent:" + model.NewID().String(),
			Scope:    "decision.read decision.write work.assign work.create work.read work.review",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a := auth.NewAuthenticator(st, nil)
	p, err := a.Authenticate(ctx, credential.Token)
	if err != nil {
		t.Fatalf("valid orchestration bearer rejected: %v", err)
	}
	if !p.IsPurposeRestricted() || p.IsWorkSessionCredential() || p.IsCommunicationSessionCredential() {
		t.Fatal("orchestration bearer inherited a worker or communication purpose")
	}
	if got, confined := p.ConfinedWorkspaceIn(tenant); !confined || got != workspace {
		t.Fatal("orchestration bearer lost its workspace confinement")
	}
	az := auth.NewAuthorizer(nil, auth.WithScopedGrants(grantingScopedAuthorizer{}))
	for _, permission := range []auth.Permission{"sessions:work:read", "sessions:work:write", "sessions:work:admin", "sessions:decision:read", "sessions:decision:write"} {
		if !az.Allowed(ctx, p, permission, tenant) {
			t.Errorf("declared permission %s denied", permission)
		}
		if az.Allowed(ctx, p, permission, foreign) {
			t.Errorf("permission %s crossed the tenant", permission)
		}
	}
	for _, permission := range []auth.Permission{"sessions:lease:admin", "sessions:lease:write", "sessions:run:write", "token:write", "sessions:profile:write", "sessions:delivery:read"} {
		if az.Allowed(ctx, p, permission, tenant) {
			t.Errorf("orchestration ceiling widened to %s", permission)
		}
	}
}

func TestOrchestrationSessionIssuerDenyCasesLifecycleAndSealedEvidence(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "orchestration-lifecycle")
	a := auth.NewAuthenticator(st, nil)
	actor := communicationSessionSystemActor(t)
	spec := auth.OrchestrationSessionCredentialSpec{Tenant: tenant, WorkspaceID: communicationSessionWorkspace(t, st, tenant), SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(), AgentRef: "agent:" + model.NewID().String(), ClaimFence: 1, ProfileRef: "ppf_" + model.NewID().String(), GrantID: model.NewID(), Capabilities: []string{"work.create", "work.read"}}
	for _, tc := range []struct {
		name   string
		change func(*auth.OrchestrationSessionCredentialSpec)
	}{
		{"missing grant", func(s *auth.OrchestrationSessionCredentialSpec) { s.GrantID = "" }},
		{"no agent", func(s *auth.OrchestrationSessionCredentialSpec) { s.AgentRef = "" }},
		{"unknown capability", func(s *auth.OrchestrationSessionCredentialSpec) { s.Capabilities = []string{"token.write"} }},
		{"duplicate capability", func(s *auth.OrchestrationSessionCredentialSpec) { s.Capabilities = []string{"work.read", "work.read"} }},
		{"noncanonical capability order", func(s *auth.OrchestrationSessionCredentialSpec) {
			s.Capabilities = []string{"work.read", "work.create"}
		}},
		{"no workspace", func(s *auth.OrchestrationSessionCredentialSpec) { s.WorkspaceID = "" }},
		{"no fence", func(s *auth.OrchestrationSessionCredentialSpec) { s.ClaimFence = 0 }},
		{"worker profile override", func(s *auth.OrchestrationSessionCredentialSpec) { s.ProfileRef = "worker" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := spec
			tc.change(&bad)
			if _, err := a.IssueOrchestrationSessionCredential(ctx, actor, bad); err == nil {
				t.Fatal("malformed orchestration grant issued")
			}
		})
	}
	if _, err := a.IssueOrchestrationSessionCredential(ctx, auth.Principal{}, spec); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Fatal("untrusted actor could issue an orchestration credential")
	}
	issued, err := a.IssueOrchestrationSessionCredential(ctx, actor, spec)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := p.Ref()
	if !ok {
		t.Fatal("credential provenance absent")
	}
	deadline, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	sealed, err := a.ResolvePrincipalScope(deadline, ref, tenant)
	if err != nil || !sealed.IsOrchestrationSessionCredential() {
		t.Fatalf("orchestration evidence did not seal: %v", err)
	}
	if _, err := a.RenewOrchestrationSessionCredential(ctx, actor, issued.ID, spec); err != nil {
		t.Fatal(err)
	}
	changed := spec
	changed.GrantID = model.NewID()
	if _, err := a.RenewOrchestrationSessionCredential(ctx, actor, issued.ID, changed); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("renewal accepted a different grant generation")
	}
	if _, err := a.IssueOrchestrationSessionCredential(ctx, actor, changed); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("same-fence grant replacement widened authority")
	}
	spec.ClaimFence++
	successor, err := a.IssueOrchestrationSessionCredential(ctx, actor, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, issued.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("successor left old bearer live")
	}
	stale := spec
	stale.ClaimFence--
	if _, err := a.IssueOrchestrationSessionCredential(ctx, actor, stale); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("stale fence recreated old authority")
	}
	base := auth.WorkSessionCredentialSpec{Tenant: tenant, SessionRef: spec.SessionRef, RunRef: spec.RunRef, AgentRef: spec.AgentRef, ClaimFence: spec.ClaimFence}
	wrong := base
	wrong.SessionRef = "osn_" + model.NewID().String()
	if err := a.RevokeOrchestrationRuntimeCredential(ctx, actor, successor.ID, wrong); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("recovery cleanup revoked a crossed runtime")
	}
	if err := a.RevokeOrchestrationRuntimeCredential(ctx, actor, successor.ID, base); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RenewOrchestrationSessionCredential(ctx, actor, successor.ID, spec); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("renewal revived revoked bearer")
	}
}

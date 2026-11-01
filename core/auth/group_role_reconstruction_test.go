// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func makeGroupDerivedEvidenceToken(t *testing.T, f *principalEvidenceFixture) model.UserGroup {
	t.Helper()
	group := f.addGroupWithMember(f.tenant, "group-admins", RoleAdmin)
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		token, err := as.Tokens().Get(f.ctx, f.token.ID)
		if err != nil {
			return err
		}
		token.Role = RoleAdmin
		token.GroupDerivedRole = true
		f.token, err = as.Tokens().Update(f.ctx, token)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return group
}

func TestSessionCredentialsFollowGroupRemoval(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	group := makeGroupDerivedEvidenceToken(t, f)
	credentials := NewSessionCredentials(f.a, func(context.Context, SessionScope) error { return nil })
	launcher, err := f.a.Authenticate(f.ctx, f.tokRaw)
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := credentials.Mint(f.ctx, launcher, SessionScope{
		TenantID: f.tenant, WorkspaceID: model.NewID(), FolderRef: "folder", SessionRef: "session", RunRef: "run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		return as.Groups().Delete(f.ctx, group.ID)
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := credentials.AuthenticateLauncher(f.ctx, bearer)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok := resolved.RoleIn(f.tenant); !ok || role != RoleViewer {
		t.Fatalf("session role after group removal = %q, %v, want viewer", role, ok)
	}
}

func TestQueuedCredentialFollowsGroupRemoval(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	group := makeGroupDerivedEvidenceToken(t, f)
	principal, err := f.a.Authenticate(f.ctx, f.tokRaw)
	if err != nil {
		t.Fatal(err)
	}
	queued, ok := QueuedCredentialFrom(principal)
	if !ok {
		t.Fatal("token has no queued credential")
	}
	queued, err = f.a.BindQueuedCredential(f.ctx, queued)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		return as.Groups().Delete(f.ctx, group.ID)
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.a.RevalidateQueuedCredential(f.ctx, queued)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok := resumed.RoleIn(f.tenant); !ok || role != RoleViewer {
		t.Fatalf("queued role after group removal = %q, %v, want viewer", role, ok)
	}
}

func TestResolvePrincipalScopeFollowsGroupRemoval(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	group := makeGroupDerivedEvidenceToken(t, f)
	ref := f.tokenRef()
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		return as.Groups().Delete(f.ctx, group.ID)
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok := resolved.RoleIn(f.tenant); !ok || role != RoleViewer {
		t.Fatalf("resolved role after group removal = %q, %v, want viewer", role, ok)
	}
}

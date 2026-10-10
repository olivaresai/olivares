// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestOSAccountBindingCurrentRoleCannotExceedOriginalCeiling(t *testing.T) {
	f := newCredentialBindingFixture(t)
	n := &osAccountNative{uid: 1601, login: "native-ceiling"}
	b := osBindings(f, n)
	admin := osAdminSession(f)
	subject, _, _ := f.session(f.userA, nil)
	c, err := b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Complete(f.deadline(), subject, c.ID, []byte("proof")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Resolve(f.deadline(), n.uid, n.login); err != nil {
		t.Fatal(err)
	}
	if err = f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		memberships, _, e := as.Memberships().List(f.ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: f.userA.ID.String()}, {Column: "target_tenant_id", Op: model.OpEq, Value: f.tenant.String()}}})
		if e != nil {
			return e
		}
		if len(memberships) != 1 {
			t.Fatal("missing original membership")
		}
		membership := memberships[0]
		membership.Role = RoleOwner
		_, e = as.Memberships().Update(f.ctx, membership)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Resolve(f.deadline(), n.uid, n.login); err == nil {
		t.Fatal("same session resolved above original role ceiling")
	}
}

func TestOSAccountBindingInstallationAuthorityCeiling(t *testing.T) {
	for _, scenario := range []string{"tenant_only_owner", "original_superadmin", "scoped_original_superadmin", "promoted_owner", "demoted_superadmin"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCredentialBindingFixture(t)
			setSuperadmin := func(enabled bool) {
				if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
					user, err := as.Users().Get(f.ctx, f.userA.ID)
					if err != nil {
						return err
					}
					user.IsSuperadmin = enabled
					_, err = as.Users().Update(f.ctx, user)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
				members, _, err := as.Memberships().List(f.ctx, model.Query{Filters: []model.Filter{
					{Column: "user_id", Op: model.OpEq, Value: f.userA.ID.String()},
					{Column: "target_tenant_id", Op: model.OpEq, Value: f.tenant.String()},
				}})
				if err != nil || len(members) != 1 {
					t.Fatalf("tenant membership: count=%d err=%v", len(members), err)
				}
				member := members[0]
				member.Role = RoleOwner
				_, err = as.Memberships().Update(f.ctx, member)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			setSuperadmin(scenario == "original_superadmin" || scenario == "scoped_original_superadmin" || scenario == "demoted_superadmin")
			n := &osAccountNative{uid: 1602, login: "native-system-ceiling"}
			b := osBindings(f, n)
			admin := osAdminSession(f)
			subject, token, session := f.session(f.userA, nil)
			if scenario == "scoped_original_superadmin" {
				if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
					session.TenantScope = f.tenant
					_, err := as.Sessions().Update(f.ctx, session)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				var err error
				subject, err = f.a.Authenticate(f.ctx, PrefixScopedSession+strings.TrimPrefix(token, PrefixSession))
				if err != nil {
					t.Fatal(err)
				}
			}
			c, err := b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = b.Complete(f.deadline(), subject, c.ID, []byte("proof")); err != nil {
				t.Fatal(err)
			}
			if scenario == "promoted_owner" || scenario == "demoted_superadmin" {
				setSuperadmin(scenario == "promoted_owner")
			}
			ref, tenant, err := b.ResolveInstallation(f.deadline(), n.uid, n.login)
			if scenario == "promoted_owner" {
				if !errors.Is(err, ErrCredentialBindingCeiling) {
					t.Fatalf("old tenant binding acquired installation authority after promotion: %v", err)
				}
				// A fresh credential and PAM proof cannot widen the permanent ceiling.
				subject, _, _ = f.session(f.userA, nil)
				c, err = b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = b.Complete(f.deadline(), subject, c.ID, []byte("proof")); !errors.Is(err, ErrCredentialBindingCeiling) {
					t.Fatalf("succession widened original installation ceiling: %v", err)
				}
				return
			}
			if scenario == "tenant_only_owner" || scenario == "demoted_superadmin" {
				if err == nil {
					t.Fatal("non-installation owner obtained an installation reference")
				}
				return
			}
			if scenario == "scoped_original_superadmin" {
				if err == nil {
					t.Fatal("original tenant-scoped credential acquired installation authority")
				}
				subject, _, _ = f.session(f.userA, nil)
				c, err = b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = b.Complete(f.deadline(), subject, c.ID, []byte("proof")); !errors.Is(err, ErrCredentialBindingCeiling) {
					t.Fatalf("unscoped succession widened original tenant ceiling: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			current, err := f.a.ResolvePrincipalScope(f.deadline(), ref, tenant)
			if err != nil {
				t.Fatal(err)
			}
			req := Request{Principal: current, Tenant: tenant,
				Permission: PermSystemAdmin, Resource: ResourceAttrs{Kind: "appliance.task", ID: "host-user.create",
					Extra: map[string]string{"target": "host-user:operator", "act": "user", "audience": "appliance-helper:olivares-portal-users", "intent_digest": "native-intent"}}}
			if decision := NewAuthorizer(nil).AuthorizeEvidence(f.deadline(), req); decision.Outcome != EvidenceAllow {
				t.Fatalf("original installation administrator refused: %v", decision.Outcome)
			}
			// Ordinary OS resolution retains the published tenant confinement.
			ordinary, _, err := b.Resolve(f.deadline(), n.uid, n.login)
			if err != nil {
				t.Fatal(err)
			}
			confined, err := f.a.ResolvePrincipalScope(f.deadline(), ordinary, tenant)
			if err != nil || confined.SessionScope() != tenant {
				t.Fatalf("ordinary tenant confinement changed: %v", err)
			}
			req.Principal = confined
			if decision := NewAuthorizer(nil).AuthorizeEvidence(f.deadline(), req); decision.Outcome != EvidenceDeny {
				t.Fatalf("ordinary OS reference acquired installation authority: %v", decision.Outcome)
			}
			// The new profile still uses the current deny overlay and exact seal.
			req.Principal = current
			policy := &principalAuthorityPolicyProducer{decision: principalAuthorityBrokenPolicy(current.evidence.observedAt,
				current.evidence.freshUntil, current.evidence.directoryEpoch)}
			if decision := NewAuthorizer(policy).AuthorizeEvidence(f.deadline(), req); decision.Outcome != EvidenceDeny || policy.typedCalls != 1 {
				t.Fatalf("installation attribution bypassed the current deny overlay: %v", decision.Outcome)
			}
			req.Principal.credentialRef.installation = false
			if decision := NewAuthorizer(nil).AuthorizeEvidence(f.deadline(), req); decision.Outcome == EvidenceAllow {
				t.Fatal("altered installation profile retained a usable seal")
			}
			if err = b.Revoke(f.deadline(), admin, tenant, f.userA.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.a.ResolvePrincipalScope(f.deadline(), ref, tenant); err == nil {
				t.Fatal("revoked OS binding retained installation attribution")
			}
		})
	}
}

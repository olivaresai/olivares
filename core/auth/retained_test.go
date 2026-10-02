// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func TestStepUpRefusalIsRetainedWithoutEvaluatingPolicy(t *testing.T) {
	for _, nativeRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "route", true: "native read"}[nativeRead], func(t *testing.T) {
			f, p := resolvedPrincipalAuthorityEvidence(t)
			var records []AuthorizationRecord
			ctx := WithAuthorizationRecording(f.ctx, func(r AuthorizationRecord) { records = append(records, r) })
			scoped := &principalAuthorityScopedProducer{}
			policy := &principalAuthorityPolicyProducer{}
			az := NewAuthorizer(policy, WithScopedGrants(scoped), WithClock(func() time.Time { return p.evidence.observedAt.Add(time.Second) }))
			req := principalAuthorityEvidenceRequest(p, f.tenant)
			req.Route.MinimumAAL = AAL3
			if nativeRead {
				d, err := az.DecideRouteRead(ctx, req)
				if err != nil || d.Allowed() {
					t.Fatalf("native assurance refusal changed: %v", err)
				}
			} else {
				w, err := az.AuthorizeRoute(ctx, req)
				if !errors.Is(err, ErrStepUpRequired) || w.minted {
					t.Fatalf("route assurance refusal changed: %v", err)
				}
			}
			if scoped.typedCalls+scoped.legacyCalls+policy.typedCalls+policy.legacyCalls != 0 {
				t.Fatal("assurance refusal consulted policy")
			}
			if len(records) != 1 || records[0].Outcome != EvidenceDeny {
				t.Fatalf("assurance refusal records = %d, want one attributable refusal", len(records))
			}
			if records[0].Snapshot.Complete || len(records[0].Snapshot.Policy)+len(records[0].Snapshot.Scoped) != 0 {
				t.Fatal("authentication refusal claimed a reconstructible policy answer")
			}
		})
	}
}

func TestRetainedRBACUsesTheEvaluatedMembershipAndRoute(t *testing.T) {
	tenant := model.NewTenantID()
	p := Principal{Kind: KindUser, UserID: model.NewID(), CredID: model.NewID(), grants: map[model.TenantID]string{tenant: RoleEditor}}
	request := Request{Principal: p, Tenant: tenant, Permission: "agent:write", Resource: ResourceAttrs{Kind: "agent"}}
	var records []AuthorizationRecord
	ctx := WithAuthorizationRecording(t.Context(), func(r AuthorizationRecord) { records = append(records, r) })
	az := NewAuthorizer(nil)
	if !az.Authorize(ctx, request).Allow || len(records) != 1 {
		t.Fatal("write answer was not recorded")
	}
	// The principal's current membership changes after the decision.
	p.grants[tenant] = RoleViewer
	if az.Authorize(t.Context(), request).Allow {
		t.Fatal("current viewer unexpectedly has write authority")
	}
	outcome, err := ReplayRetainedAuthorization(records[0].Snapshot, ScopedDecision{}, Decision{Allow: true})
	if err != nil || outcome != EvidenceAllow {
		t.Fatalf("historical editor: %v %v", outcome, err)
	}
	az.Authorize(ctx, request)
	outcome, err = ReplayRetainedAuthorization(records[1].Snapshot, ScopedDecision{}, Decision{Allow: true})
	if err != nil || outcome != EvidenceDeny {
		t.Fatalf("recorded viewer denial: %v %v", outcome, err)
	}
	p.grants[tenant] = RoleAdmin
	request.Route = RouteMetadata{RequireScopedGrant: true}
	if az.Authorize(ctx, request).Allow {
		t.Fatal("route removed the RBAC term")
	}
	outcome, err = ReplayRetainedAuthorization(records[2].Snapshot, ScopedDecision{}, Decision{Allow: true})
	if err != nil || outcome != EvidenceDeny {
		t.Fatalf("route metadata was lost: %v %v", outcome, err)
	}
}

func TestRetainedTenantOwnerAuthorityPreservesItsBoundary(t *testing.T) {
	tenant, other := model.NewTenantID(), model.NewTenantID()
	for _, tc := range []struct {
		name       string
		superadmin bool
		scope      model.TenantID
		target     model.TenantID
		permission Permission
		want       EvidenceOutcome
	}{
		{"owner implicit grant", false, "", tenant, "directory:admin", EvidenceAllow},
		{"superadmin owner implicit grant", true, tenant, tenant, "directory:admin", EvidenceAllow},
		{"superadmin owner cannot act globally", true, tenant, tenant, PermSystemAdmin, EvidenceDeny},
		{"superadmin owner cannot cross tenant", true, tenant, other, "directory:admin", EvidenceDeny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Principal{Kind: KindUser, UserID: model.NewID(), CredID: model.NewID(),
				Superadmin: tc.superadmin, sessionScope: tc.scope,
				grants: map[model.TenantID]string{tenant: RoleOwner}}
			req := Request{Principal: p, Tenant: tc.target, Permission: tc.permission,
				Resource: ResourceAttrs{Kind: "directory"}, Route: RouteMetadata{RequireScopedGrant: tc.permission != PermSystemAdmin}}
			var records []AuthorizationRecord
			ctx := WithAuthorizationRecording(t.Context(), func(r AuthorizationRecord) { records = append(records, r) })
			live := NewAuthorizer(nil).Authorize(ctx, req)
			if live.Allow != (tc.want == EvidenceAllow) || len(records) != 1 {
				t.Fatalf("live decision = %+v, records = %d", live, len(records))
			}
			// Membership can change after the request, without changing its history.
			delete(p.grants, tenant)
			outcome, err := ReplayRetainedAuthorization(records[0].Snapshot, ScopedDecision{}, Decision{Allow: true})
			if err != nil || outcome != tc.want {
				t.Fatalf("retained owner decision = %v, %v; want %v", outcome, err, tc.want)
			}
		})
	}
}

type unavailableRetainedEvaluator struct{}

func (unavailableRetainedEvaluator) Evaluate(context.Context, Request) (Decision, error) {
	return Decision{Allow: true}, nil
}

func TestRetainedAuthorizationDoesNotClaimUnknownPolicyInputs(t *testing.T) {
	tenant := model.NewTenantID()
	var record AuthorizationRecord
	ctx := WithAuthorizationRecording(t.Context(), func(r AuthorizationRecord) { record = r })
	az := NewAuthorizer(unavailableRetainedEvaluator{})
	req := Request{Tenant: tenant, Permission: "agent:write", Principal: Principal{Kind: KindUser, UserID: model.NewID(), Superadmin: true}}
	if !az.Authorize(ctx, req).Allow {
		t.Fatal("recording changed the live evaluator's answer")
	}
	if record.Snapshot.Complete {
		t.Fatal("unknown policy was claimed reconstructible")
	}
	if _, err := ReplayRetainedAuthorization(record.Snapshot, ScopedDecision{}, Decision{Allow: true}); err == nil {
		t.Fatal("missing policy inputs were accepted for replay")
	}
}

func TestAuthorizationHistorySkipsOrdinaryReadAllows(t *testing.T) {
	var records []AuthorizationRecord
	ctx := WithAuthorizationRecording(t.Context(), func(r AuthorizationRecord) { records = append(records, r) })
	az := NewAuthorizer(nil)
	p := Principal{Kind: KindUser, UserID: model.NewID(), Superadmin: true}
	az.Allowed(ctx, p, "agent:read", model.NewTenantID())
	if len(records) != 0 {
		t.Fatal("ordinary read consumed history retention")
	}
	p.Superadmin = false
	az.Allowed(ctx, p, "agent:read", model.NewTenantID())
	if len(records) != 1 || records[0].Outcome != EvidenceDeny {
		t.Fatal("read denial was not recorded")
	}
}

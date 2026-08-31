// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

const forbidSecret = `forbid(principal, action, resource) when { resource.sensitivity == "secret" };`

func cedarReq(sensitivity string) auth.Request {
	return auth.Request{
		Principal:  auth.Principal{Kind: auth.KindUser, CredID: "cred-1"},
		Permission: "agent:write",
		Tenant:     model.TenantID("t-1"),
		Resource:   auth.ResourceAttrs{Kind: "agent", ID: "a-1", Sensitivity: sensitivity},
	}
}

func TestCedarEmptyPolicyImposesNoRestriction(t *testing.T) {
	// No operator policy => only the implicit base permit => every request is Allow
	// (the RBAC decision stands). This is the restrict-only invariant: an empty Cedar
	// set must NOT deny by Cedar's default-deny.
	ce, err := NewCedarEvaluator("", nil)
	if err != nil {
		t.Fatalf("NewCedarEvaluator: %v", err)
	}
	dec, err := ce.Evaluate(context.Background(), cedarReq("secret"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !dec.Allow {
		t.Errorf("empty policy must impose no restriction, got %+v", dec)
	}
}

func TestCedarForbidOnSensitivity(t *testing.T) {
	ce, err := NewCedarEvaluator(forbidSecret, nil)
	if err != nil {
		t.Fatalf("NewCedarEvaluator: %v", err)
	}
	// A secret resource is forbidden (the PDP restricts).
	if dec, _ := ce.Evaluate(context.Background(), cedarReq("secret")); dec.Allow {
		t.Error("forbid-on-secret must deny a secret resource")
	}
	// A non-secret resource is not restricted (RBAC stands).
	if dec, _ := ce.Evaluate(context.Background(), cedarReq("public")); !dec.Allow {
		t.Error("forbid-on-secret must not restrict a public resource")
	}
	// An UNSET sensitivity must evaluate the forbid to false (sensitivity is always
	// present as "" — never ABSENT, which would make Cedar error and silently SKIP the
	// forbid). So an unset request is allowed cleanly, and the forbid still fires when
	// a caller does set sensitivity="secret".
	if dec, _ := ce.Evaluate(context.Background(), cedarReq("")); !dec.Allow {
		t.Error("forbid-on-secret must not restrict a resource with unset sensitivity")
	}
}

func TestCedarErroredForbidFailsClosed(t *testing.T) {
	// F-06: a forbid that ERRORS during evaluation — it touches an attribute the
	// engine never populates and the policy did not guard with `has` — is a restriction
	// Cedar silently dropped. Treating that as "no restriction" is fail-OPEN on a deny
	// rule, so the PDP must fail CLOSED for that request. (Before this returned
	// Allow — that was the F-06 fail-open, previously enshrined as intended behavior.)
	ce, err := NewCedarEvaluator(`forbid(principal, action, resource) when { resource.owner == "alice" };`, nil)
	if err != nil {
		t.Fatalf("NewCedarEvaluator: %v", err)
	}
	dec, err := ce.Evaluate(context.Background(), cedarReq("public"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dec.Allow {
		t.Error("F-06: an errored forbid must fail closed (deny), not silently drop the restriction")
	}

	// The fail-closed is SELECTIVE: a forbid the author guards with `has` does not error
	// on the absent attribute (short-circuit), so it imposes no restriction and the
	// request is cleanly allowed. This is the escape hatch — one unpopulated attribute
	// reference denies only the requests whose forbid genuinely cannot be resolved.
	guarded, err := NewCedarEvaluator(`forbid(principal, action, resource) when { resource has owner && resource.owner == "alice" };`, nil)
	if err != nil {
		t.Fatalf("NewCedarEvaluator (guarded): %v", err)
	}
	if dec, _ := guarded.Evaluate(context.Background(), cedarReq("public")); !dec.Allow {
		t.Error("a `has`-guarded forbid on an absent attribute must not deny (no eval error)")
	}
}

func TestCedarInvalidPolicyFailsAtLoad(t *testing.T) {
	if _, err := NewCedarEvaluator("this is not cedar @#$", nil); err == nil {
		t.Fatal("an invalid cedar policy must fail at load, not on the hot path")
	}
}

// TestCedarThroughAuthorizerRestrictsNeverWidens proves the deny-only contract end to
// end: wired as the Authorizer's PolicyEvaluator, the Cedar PDP turns an RBAC ALLOW
// into a deny for a sensitive resource, yet can NEVER turn an RBAC DENY into an allow.
func TestCedarThroughAuthorizerRestrictsNeverWidens(t *testing.T) {
	ce, err := NewCedarEvaluator(forbidSecret, nil)
	if err != nil {
		t.Fatalf("NewCedarEvaluator: %v", err)
	}
	az := auth.NewAuthorizer(ce)
	ctx := context.Background()
	tenant := model.TenantID("t-1")

	// Superadmin => RBAC allows. The PDP forbids the secret resource => denied.
	super := auth.Principal{Superadmin: true, Kind: auth.KindUser, CredID: "root"}
	if az.Authorize(ctx, auth.Request{Principal: super, Permission: "agent:write", Tenant: tenant,
		Resource: auth.ResourceAttrs{Kind: "agent", Sensitivity: "secret"}}).Allow {
		t.Error("PDP must restrict a secret resource even for an RBAC-allowed superadmin")
	}
	// Same superadmin on a public resource => the PDP imposes nothing => allowed.
	if !az.Authorize(ctx, auth.Request{Principal: super, Permission: "agent:write", Tenant: tenant,
		Resource: auth.ResourceAttrs{Kind: "agent", Sensitivity: "public"}}).Allow {
		t.Error("PDP must not restrict a public resource")
	}
	// A non-member (RBAC denies) on a PUBLIC resource the PDP would allow => still
	// denied: the PDP can never WIDEN an RBAC denial.
	nonMember := auth.Principal{Kind: auth.KindUser, CredID: "stranger"}
	if az.Authorize(ctx, auth.Request{Principal: nonMember, Permission: "agent:write", Tenant: tenant,
		Resource: auth.ResourceAttrs{Kind: "agent", Sensitivity: "public"}}).Allow {
		t.Error("PDP must never widen an RBAC denial")
	}
}

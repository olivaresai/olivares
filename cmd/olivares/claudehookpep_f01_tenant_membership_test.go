// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// TestHookPEP_F01_DeclaredTenantRequiresMembership is the RED repro for F-01: the governed
// Claude Code hooks PEP resolves the request tenant from the client-supplied hint
// (X-Olivares-Hook-Tenant → in.Identity.Tenant) and, on the hint path, only confirms a
// governed policy EXISTS for that tenant (claudehookpep.go:432-451 resolveTenant). It never
// checks that the authenticated principal is a MEMBER of the declared tenant — unlike the
// sibling resolvers in the inference proxy (inferenceproxy.go:705) and apps gateway
// (appsgateway.go:573), which both gate the tenant by `p.Superadmin || p.IsMember(tid)`.
//
// Consequence: a principal that is a member of tenant A can declare tenant B in the hint and
// have B's hook policy govern the tool-call. Because the PDP overlay is OPTIONAL (eval==nil in
// the AGPL build) and only consulted when wired, tenant resolution is the ONLY authz boundary
// here — so an outsider is governed by (and here, ALLOWED by) another tenant's policy.
//
// The test asserts the SECURE behavior (deny for a non-member's declared tenant), so it is RED
// today and goes GREEN once resolveTenant gates the hint by membership.
func TestHookPEP_F01_DeclaredTenantRequiresMembership(t *testing.T) {
	h := newHarness(t)

	// firmAgentToken grants membership to tenant A ONLY (createApprover: POST /v1/memberships
	// with tenant=h.tenantA). This principal is NOT a member of tenant B.
	tok := h.firmAgentToken(t, "agent-a@f01.test")

	tidA, err := model.ParseTenantID(h.tenantA)
	if err != nil {
		t.Fatalf("parse tenant A: %v", err)
	}
	tidB, err := model.ParseTenantID(h.tenantB)
	if err != nil {
		t.Fatalf("parse tenant B: %v", err)
	}

	// A decider that governs BOTH tenants. Tenant A denies every tool; tenant B allows every
	// tool. No PDP overlay (eval == nil): the default AGPL build, so tenant resolution is the
	// ONLY authorization boundary. requireFirm is left false so the firm gate is not what
	// stops (or fails to stop) the call — this isolates the membership hole in resolveTenant.
	dec := &claudeHookDecider{
		tenants: map[model.TenantID]resolvedTenant{
			tidA: {tenant: tidA, policy: hookPolicyDoc{
				Default: "deny",
				Rules:   []hookPolicyRule{{Tool: "*", Decision: "deny"}},
			}},
			tidB: {tenant: tidB, policy: hookPolicyDoc{
				Default: "allow",
				Rules:   []hookPolicyRule{{Tool: "*", Decision: "allow"}},
			}},
		},
		authr: auth.NewAuthenticator(h.st, nil),
		clock: time.Now,
		log:   discardLog(),
	}

	in := claude.HookDecisionInput{
		Event:        "PreToolUse",
		SessionID:    "s-f01",
		Tool:         "mcp__acme__deploy",
		ResourceKind: "mcp.tool",
		ResourceRef:  "deploy",
		Mode:         "read",
		// The client DECLARES tenant B — a tenant the authenticated principal does not belong to.
		Identity: claude.HookIdentity{Tenant: h.tenantB},
	}

	res, err := dec.Decide(context.Background(), in, tok)
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}

	// SECURE: a principal that is not a member of the DECLARED tenant must never be governed by
	// that tenant's policy. F-01 (live today): the hint is trusted without a membership check,
	// so tenant B's allow-all policy authorizes an outsider's tool-call — cross-tenant escalation.
	if res.Permission == claude.DecisionAllow {
		t.Fatalf("F-01: a tenant-A-only principal declared tenant B and was ALLOWED under B's "+
			"policy (cross-tenant escalation); the declared tenant must be gated by membership. "+
			"got permission=%q reason=%q", res.Permission, res.Reason)
	}
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("expected DENY for a non-member's declared tenant, got permission=%q reason=%q",
			res.Permission, res.Reason)
	}
}

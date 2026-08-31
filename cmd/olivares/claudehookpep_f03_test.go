// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
)

// fakeScopedForbid is a ScopedAuthorizer that FORBIDS a specific mcp_server resource and
// abstains on everything else — modeling a central scoped forbid ("forbid mcp_server
// X"). It proves the hook now consults the SAME scoped algebra REST/model/MCP enforce.
type fakeScopedForbid struct{ server string }

func (f fakeScopedForbid) Scoped(_ context.Context, req auth.Request) (auth.ScopedDecision, error) {
	if req.Resource.Kind == "mcp_server" && req.Resource.ID == f.server {
		return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "scoped forbid: mcp_server " + f.server}, nil
	}
	return auth.ScopedDecision{Effect: auth.EffectAbstain}, nil
}

// erroringScoped fails every scoped evaluation — to exercise the fail-closed posture.
type erroringScoped struct{ err error }

func (e erroringScoped) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	return auth.ScopedDecision{}, e.err
}

// TestF03HookConsultsCentralScopedForbid is the F-03 conformance proof: a policy whose local
// disposition ALLOWS an mcp tool-call is DENIED at the hook when a central scoped forbid
// targets that mcp_server (forbid-overrides-allow — the same disposition the REST, model and
// MCP surfaces enforce via the shared Authorizer/ScopedGrants). The deny anchors to the
// signed, hash-chained ledger like any other terminal decision (the anchoring is
// preserved). A non-MCP tool has no catalog projection, so the scoped overlay cannot target
// it (documented residual).
func TestF03HookConsultsCentralScopedForbid(t *testing.T) {
	f := newHookLedgerFixture(t, hookPolicyDoc{Version: "hook-policy/scoped-v1", Default: claude.DecisionAllow})
	f.dec.scoped = fakeScopedForbid{server: "payments"}

	// Control 1: an mcp tool-call whose server is NOT forbidden stays allowed (the overlay
	// never widens NOR over-denies).
	okIn := hookLedgerInput(f.tenant, "mcp__billing__read", hookResourceKindMCP, "billing/read", "read")
	assertSingleHookLedgerDecision(t, f, okIn, claude.DecisionAllow)

	// The central scoped forbid on the payments mcp_server flips ALLOW → DENY, anchored.
	denyIn := hookLedgerInput(f.tenant, "mcp__payments__charge", hookResourceKindMCP, "payments/charge", "write")
	res := assertSingleHookLedgerDecision(t, f, denyIn, claude.DecisionDeny)
	if !strings.Contains(res.Reason, "scoped") {
		t.Fatalf("deny reason must name the central scoped forbid, got %q", res.Reason)
	}

	// Control 2: a NON-MCP tool-call has no scope-grantable catalog projection, so the mcp
	// scoped forbid cannot target it — the disposition stands (allow).
	fileIn := hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/acme/README.md", "read")
	assertSingleHookLedgerDecision(t, f, fileIn, claude.DecisionAllow)
}

// TestF03HookScopedEngineErrorFailsClosed proves the scoped overlay is deny-closed: a
// scoped-engine error denies the hook tool-call (an unreadable forbid state must never let
// the hook approve what the central algebra would forbid).
func TestF03HookScopedEngineErrorFailsClosed(t *testing.T) {
	f := newHookLedgerFixture(t, hookPolicyDoc{Default: claude.DecisionAllow})
	f.dec.scoped = erroringScoped{err: errors.New("scoped engine down")}
	in := hookLedgerInput(f.tenant, "mcp__payments__charge", hookResourceKindMCP, "payments/charge", "write")
	res := assertSingleHookLedgerDecision(t, f, in, claude.DecisionDeny)
	if !strings.Contains(res.Reason, "deny-closed") && !strings.Contains(res.Reason, "unavailable") {
		t.Fatalf("a scoped-engine error must fail closed, got %q", res.Reason)
	}
}

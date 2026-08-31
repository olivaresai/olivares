// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// The built-in default classifier encodes the decided CRITICAL set:
// production deploy/retire, data deletion, security-policy changes, re-enable
// after kill-switch, key custody/rotation — everything else defaults to HIGH.
func TestDefaultActionRiskTier(t *testing.T) {
	cases := []struct {
		action string
		want   ActionRiskTier
	}{
		// The decided CRITICAL set — exact gate actions that exist today.
		{"deploy.apply", RiskTierCritical},
		{"audit.ledger.recover", RiskTierCritical},
		{"deploy.retire", RiskTierCritical},
		{"security.enforcement.enable", RiskTierCritical},
		{"compliance.content.erase", RiskTierCritical},
		// releasing a legal hold is the gateway to the data-deletion family.
		{"compliance.hold.release", RiskTierCritical},
		// Decided families: kill-switch re-enable, key custody.
		{"security.killswitch.reenable", RiskTierCritical},
		{"secrets.provider.rotate", RiskTierCritical},
		{"keys.custody.transfer", RiskTierCritical},
		{"kms.key.schedule-deletion", RiskTierCritical},
		{"pki.ca.rotate", RiskTierCritical},
		// Data-deletion verbs on any domain.
		{"knowledge.index.purge", RiskTierCritical},
		{"data.store.delete", RiskTierCritical},
		{"inventory.asset.wipe", RiskTierCritical},
		{"tenant.org.destroy", RiskTierCritical},
		// mcp.tool.call only ever gates server-classified DESTRUCTIVE tools → critical.
		{"mcp.tool.call", RiskTierCritical},
		// archiving an Anthropic workspace is irreversible (revokes all keys) → critical
		// (explicit map entry; ".archive" is deliberately NOT a critical suffix). The
		// recoverable admin actions stay HIGH (single approval).
		{"claude.admin.workspace.archive", RiskTierCritical},
		// E2: workspace-admin grant is recoverable but privilege-critical → critical.
		{"claude.admin.workspace.admin_grant", RiskTierCritical},
		{"claude.admin.key.deactivate", RiskTierHigh},
		{"claude.admin.key.archive", RiskTierHigh},
		{"claude.admin.member.deprovision", RiskTierHigh},
		{"claude.admin.invite.revoke", RiskTierHigh},
		{"claude.admin.invite.create", RiskTierHigh},
		{"claude.admin.member.role_update", RiskTierHigh},
		{"claude.admin.workspace.member_add", RiskTierHigh},
		// Everything else that reaches the approval queue defaults to HIGH. A generic
		// agent tool-call (claude.tool.use) is operator-routed to "ask" — one human in
		// the loop — not inherently destructive; an operator raises it via policy.
		{"voice.session.open", RiskTierHigh},
		{"orchestration.schedule.fire", RiskTierHigh},
		{"claude.tool.use", RiskTierHigh},
		{"deploy", RiskTierHigh}, // a legacy plain name is NOT in the critical set
		// Case/whitespace robustness.
		{"  Deploy.Apply ", RiskTierCritical},
	}
	for _, c := range cases {
		if got := defaultActionRiskTier(c.action); got != c.want {
			t.Errorf("defaultActionRiskTier(%q) = %q, want %q", c.action, got, c.want)
		}
	}
}

func TestResolveRiskTierPolicyWord(t *testing.T) {
	// An explicit policy tier is authoritative in BOTH directions (the set is configurable by policy)...
	if got := resolveRiskTier(approvalSpec{RiskTier: "high"}, true, "deploy.apply"); got != RiskTierHigh {
		t.Errorf("explicit downgrade ignored: got %q", got)
	}
	if got := resolveRiskTier(approvalSpec{RiskTier: "critical"}, true, "claude.tool.use"); got != RiskTierCritical {
		t.Errorf("explicit upgrade ignored: got %q", got)
	}
	// ...a matched policy that stays silent defers to the built-in default...
	if got := resolveRiskTier(approvalSpec{}, true, "deploy.apply"); got != RiskTierCritical {
		t.Errorf("silent policy must defer to the default: got %q", got)
	}
	// ...and with no match at all the default classifies.
	if got := resolveRiskTier(approvalSpec{}, false, "voice.session.open"); got != RiskTierHigh {
		t.Errorf("unmatched default: got %q", got)
	}
}

func TestFloorRequiredApprovals(t *testing.T) {
	if got := floorRequiredApprovals(1, RiskTierCritical); got != 2 {
		t.Errorf("critical floor: got %d, want 2", got)
	}
	if got := floorRequiredApprovals(3, RiskTierCritical); got != 3 {
		t.Errorf("the floor must never LOWER a threshold: got %d, want 3", got)
	}
	if got := floorRequiredApprovals(1, RiskTierHigh); got != 1 {
		t.Errorf("high keeps its threshold: got %d, want 1", got)
	}
}

func TestApprovalSystemTokenCannotDecide(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/approvals/approval-1/decisions", bytes.NewBufferString(`{"decision":"approve"}`))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", "approval-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	rec := httptest.NewRecorder()

	(&Module{}).handleDecide(rec, req, api.ModuleContext{Principal: auth.Principal{Kind: auth.KindToken}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("system-token approval status = %d; want 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "system token cannot approve") {
		t.Fatalf("system-token denial must be explicit; body=%s", rec.Body.String())
	}
}

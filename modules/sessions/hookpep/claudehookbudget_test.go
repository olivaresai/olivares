// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestTenantConfigReadsTheDenialBudgetKey(t *testing.T) {
	var cfg Config
	raw := `{"tenants":[{"tenant":"t1","denial_budget":{"consecutive":2,"total":5}}]}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	rt, err := ResolveTenant(model.TenantID("tenant-budget"), cfg.Tenants[0], time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := (sessions.DenialBudget{Consecutive: 2, Total: 5}); rt.denialBudget != got {
		t.Fatalf("resolved budget = %+v, want %+v", rt.denialBudget, got)
	}
}

func TestResolveTenantRefusesANegativeDenialBudget(t *testing.T) {
	for name, budget := range map[string]sessions.DenialBudget{
		"consecutive": {Consecutive: -1},
		"total":       {Total: -1},
	} {
		if _, err := ResolveTenant(model.TenantID("tenant-budget"), TenantConfig{DenialBudget: budget}, time.Now(), nil); err == nil {
			t.Fatalf("a negative %s limit was accepted", name)
		}
	}
	rt, err := ResolveTenant(model.TenantID("tenant-budget"), TenantConfig{DenialBudget: sessions.DenialBudget{Consecutive: 3}}, time.Now(), nil)
	if err != nil || rt.denialBudget.Total != 0 {
		t.Fatalf("a limit left at zero must stay zero (the default applies later): %+v %v", rt.denialBudget, err)
	}
}

type countedDecision struct {
	denied bool
	reason string
	budget sessions.DenialBudget
}

// countSessionDecision reports exactly the governed tool-call decisions of an authenticated
// session, with the tenant's budget, and never a person's refusal.
func TestCountSessionDecisionReportsOnlyGovernedToolCallDecisions(t *testing.T) {
	tenant := model.TenantID("tenant-budget")
	budget := sessions.DenialBudget{Consecutive: 2, Total: 9}
	rt, err := ResolveTenant(tenant, TenantConfig{DenialBudget: budget}, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	session := auth.Principal{SessionIdentity: "osn_session"}
	deny := claude.HookDecisionResult{Permission: claude.DecisionDeny, Reason: "policy says no"}
	allow := claude.HookDecisionResult{Permission: claude.DecisionAllow, Reason: "ok"}
	for name, tc := range map[string]struct {
		event     string
		principal auth.Principal
		authErr   error
		out       claude.HookDecisionResult
		declined  bool
		want      *countedDecision
	}{
		"deny before a tool-call":      {"PreToolUse", session, nil, deny, false, &countedDecision{true, "policy says no", budget}},
		"allow before a tool-call":     {"PreToolUse", session, nil, allow, false, &countedDecision{false, "ok", budget}},
		"deny of a permission request": {"PermissionRequest", session, nil, deny, false, &countedDecision{true, "policy says no", budget}},
		"a person's refusal":           {"PreToolUse", session, nil, deny, true, nil},
		"a review still pending":       {"PreToolUse", session, nil, claude.HookDecisionResult{Permission: claude.DecisionAsk}, false, nil},
		"an output event":              {"PostToolUse", session, nil, deny, false, nil},
		"a lifecycle event":            {"SessionStart", session, nil, allow, false, nil},
		"an unauthenticated caller":    {"PreToolUse", session, errors.New("bad token"), deny, false, nil},
		"a caller with no session":     {"PreToolUse", auth.Principal{}, nil, deny, false, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var got *countedDecision
			d := &Decider{
				Tenants: map[model.TenantID]ResolvedTenant{tenant: rt},
				SessionDecision: func(_ auth.Principal, denied bool, reason string, b sessions.DenialBudget) error {
					got = &countedDecision{denied, reason, b}
					return nil
				},
			}
			d.countSessionDecision(tenant, tc.principal, tc.authErr, claude.HookDecisionInput{Event: tc.event}, tc.out, &humanAnswer{declined: tc.declined})
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("reported %+v, want nothing", *got)
			case tc.want != nil && (got == nil || *got != *tc.want):
				t.Fatalf("reported %+v, want %+v", got, *tc.want)
			}
		})
	}
}

// A budget the module cannot take (the run ended meanwhile) never changes the verdict.
func TestCountSessionDecisionSurvivesAFailingBudget(t *testing.T) {
	d := &Decider{SessionDecision: func(auth.Principal, bool, string, sessions.DenialBudget) error { return context.Canceled }}
	d.countSessionDecision(model.TenantID("tenant-budget"), auth.Principal{SessionIdentity: "osn_session"}, nil, claude.HookDecisionInput{Event: "PreToolUse"},
		claude.HookDecisionResult{Permission: claude.DecisionDeny}, &humanAnswer{})
}

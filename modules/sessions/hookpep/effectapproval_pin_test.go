// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"strings"
	"testing"
)

// pinBridge is an in-memory approval bridge: it answers one status and one spend
// result, and records what the caller asked.
type pinBridge struct {
	ref, status, bound string
	openErr, spendErr  error
	granted, replay    bool

	opened, sessions, scoped, spent []string
}

func (b *pinBridge) GateOnceForSession(_ context.Context, _ model.TenantID, action, subjectKind, subjectRef, planHash, _, _, sessionRef string) (string, string, string, error) {
	b.opened = append(b.opened, action+"|"+subjectKind+"|"+subjectRef+"|"+planHash)
	b.sessions = append(b.sessions, sessionRef)
	if b.openErr != nil {
		return "", "", "", b.openErr
	}
	return b.ref, b.status, b.bound, nil
}

func (b *pinBridge) StatusScoped(_ context.Context, _ model.TenantID, ref, planHash, action, subjectKind, subjectRef string) (string, string, error) {
	b.scoped = append(b.scoped, ref+"|"+planHash+"|"+action+"|"+subjectKind+"|"+subjectRef)
	if b.openErr != nil {
		return "", "", b.openErr
	}
	return b.status, b.bound, nil
}

func (b *pinBridge) ConsumeApproval(_ context.Context, _ model.TenantID, ref, consumerID, policyVersion string) (bool, bool, error) {
	b.spent = append(b.spent, ref+"|"+consumerID+"|"+policyVersion)
	return b.granted, b.replay, b.spendErr
}

// The legacy Claude Code hook (no session identity) asks the bridge on every
// retry: pending asks, approved spends once for the tool_use_id or a fresh nonce,
// break-glass allows without a spend, anything else denies.
func TestPinHookHITLApprovalOutcomes(t *testing.T) {
	broken := errors.New("bridge broke")
	for _, tc := range []struct {
		name       string
		bridge     *pinBridge
		toolUseID  string
		permission string
		reason     string
		spend      string // the recorded spend; "nonce" is a fresh single-use id
	}{
		{"open error", &pinBridge{openErr: broken}, "toolu_1", claude.DecisionDeny, "could not open governed approval (deny-closed)", ""},
		{"pending", &pinBridge{ref: "appr-1", status: governance.GateStatusPending}, "toolu_1", claude.DecisionAsk, "pending human approval (appr-1)", ""},
		{"approved, transport id", &pinBridge{ref: "appr-1", status: governance.GateStatusApproved, granted: true}, "toolu_1", claude.DecisionAllow, "approved by human review, single-use grant spent (appr-1)", "appr-1|toolu_1|v7"},
		{"approved, no transport id", &pinBridge{ref: "appr-1", status: governance.GateStatusApproved, granted: true}, "", claude.DecisionAllow, "approved by human review, single-use grant spent (appr-1)", "nonce|v7"},
		{"replay", &pinBridge{ref: "appr-1", status: governance.GateStatusApproved, replay: true}, "toolu_2", claude.DecisionDeny, "human approval already consumed by another tool-call; replay denied — a new human decision is required (appr-1)", "appr-1|toolu_2|v7"},
		{"not spendable", &pinBridge{ref: "appr-1", status: governance.GateStatusApproved}, "toolu_1", claude.DecisionDeny, "governed approval is no longer valid to spend (deny-closed)", "appr-1|toolu_1|v7"},
		{"spend error", &pinBridge{ref: "appr-1", status: governance.GateStatusApproved, spendErr: broken}, "toolu_1", claude.DecisionDeny, "could not spend governed approval (deny-closed)", "appr-1|toolu_1|v7"},
		{"break-glass", &pinBridge{ref: "breakglass:g1", status: governance.GateStatusBreakGlass}, "toolu_1", claude.DecisionAllow, "authorized by BREAK-GLASS emergency access (breakglass:g1) — audited; post-review required", ""},
		{"rejected", &pinBridge{ref: "appr-1", status: governance.GateStatusRejected}, "toolu_1", claude.DecisionDeny, "human review did not approve (status=rejected)", ""},
		{"expired", &pinBridge{ref: "appr-1", status: governance.GateStatusExpired}, "toolu_1", claude.DecisionDeny, "human review did not approve (status=expired)", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Decider{Bridge: tc.bridge}
			in := claude.HookDecisionInput{Tool: "Bash", ResourceKind: "shell", ResourceRef: "rm -rf build", PlanHash: "plan-1", ToolUseID: tc.toolUseID}
			res := d.gateViaHITL(context.Background(), model.TenantID("t"), in, hookDisposition{}, "agent:a", "firm", "v7")
			if res.Permission != tc.permission || res.Reason != tc.reason {
				t.Fatalf("decision = %s %q, want %s %q", res.Permission, res.Reason, tc.permission, tc.reason)
			}
			if got := strings.Join(tc.bridge.opened, ","); got != ActionCapability+"|claude.tool|rm -rf build|plan-1" {
				t.Fatalf("opened %q", got)
			}
			assertPinSpend(t, tc.bridge.spent, tc.spend)
		})
	}
	res := (&Decider{}).gateViaHITL(context.Background(), model.TenantID("t"), claude.HookDecisionInput{Tool: "Bash", PlanHash: "plan-1"}, hookDisposition{}, "agent:a", "firm", "v7")
	if res.Permission != claude.DecisionDeny || res.Reason != "human approval required but the HITL bridge is not wired (deny-closed)" {
		t.Fatalf("unwired bridge = %s %q", res.Permission, res.Reason)
	}
}

// A privileged launch spends its approval once with a fresh nonce; a launch that
// names its approval reads that approval, scope-checked; bridge errors are outages.
func assertPinSpend(t *testing.T, spent []string, want string) {
	t.Helper()
	if want == "" {
		if len(spent) != 0 {
			t.Fatalf("spent %v, want none", spent)
		}
		return
	}
	if version, ok := strings.CutPrefix(want, "nonce|"); ok {
		parts := strings.Split(strings.Join(spent, ","), "|")
		if len(spent) != 1 || len(parts) != 3 || !strings.HasPrefix(parts[1], singleUseConsumerPrefix) || parts[2] != version {
			t.Fatalf("spent %v, want one spend for a fresh single-use consumer, version %q", spent, version)
		}
		return
	}
	if len(spent) != 1 || spent[0] != want {
		t.Fatalf("spent %v, want %q", spent, want)
	}
}

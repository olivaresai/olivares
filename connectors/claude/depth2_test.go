// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"encoding/json"
	"testing"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/olivaresai/olivares/sdk/model"
)

var d2Time = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

// TestParseNetNewEvents proves the ANT2-09 net-new events parse with their attribution
// (plugin/skill names + query_source/effort) and produce the right findings.
func TestParseNetNewEvents(t *testing.T) {
	res := []*commonpb.KeyValue{kvStr(attrSessionID, "sess_1")}

	// plugin_installed → supply-chain finding carrying the plugin name.
	pev, ok := parseLogRecord(logRecord(evtPluginInstalled, d2Time,
		kvStr(attrPluginName, "acme-plugin"), kvStr(attrQuerySource, "agent_sdk")), res)
	if !ok || pev.pluginName != "acme-plugin" || pev.querySource != "agent_sdk" {
		t.Fatalf("plugin_installed parse = %+v", pev)
	}
	if f, ok := findingFromPluginInstalled(pev); !ok || f.Kind != findingKindSupplyChain || f.Severity != model.SeverityMedium {
		t.Errorf("plugin finding = %+v ok=%v, want supply_chain/medium", f, ok)
	}

	// skill_activated → supply-chain finding carrying the skill name.
	sev, _ := parseLogRecord(logRecord(evtSkillActivated, d2Time, kvStr(attrSkillName, "pdf")), res)
	if sev.skillName != "pdf" {
		t.Errorf("skill name = %q, want pdf", sev.skillName)
	}
	if f, ok := findingFromSkillActivated(sev); !ok || f.Kind != findingKindSupplyChain {
		t.Errorf("skill finding = %+v ok=%v", f, ok)
	}

	// compaction → forensic continuity finding.
	cev, _ := parseLogRecord(logRecord(evtCompaction, d2Time), res)
	if f, ok := findingFromCompaction(cev); !ok || f.Kind != findingKindForensic {
		t.Errorf("compaction finding = %+v ok=%v", f, ok)
	}

	// effort attribution flows onto an api_request event.
	aev, _ := parseLogRecord(logRecord(evtAPIRequest, d2Time, kvStr(attrModel, "claude-opus-4-8"), kvStr(attrEffort, "xhigh")), res)
	if aev.effort != "xhigh" {
		t.Errorf("effort = %q, want xhigh", aev.effort)
	}

	// hook_execution_* is recognized by prefix and carries the hook name.
	hev, _ := parseLogRecord(logRecord("claude_code.hook_execution_completed", d2Time, kvStr(attrHookEventName, "PreToolUse")), res)
	if hev.hookName != "PreToolUse" {
		t.Errorf("hook_execution hook name = %q, want PreToolUse", hev.hookName)
	}
}

// TestMetricsNoDoubleCount proves ANT2-09's reconciliation invariant: the 8 metrics
// are recognized, cost.usage/token.usage are flagged as cost (so they are never summed
// as cost here — Owns it), and a metrics batch produces NO CostSample (only
// liveness via onSignal).
func TestMetricsNoDoubleCount(t *testing.T) {
	for _, m := range []string{
		metricSessionCount, metricLinesOfCode, metricPullRequest, metricCommit,
		metricCostUsage, metricTokenUsage, metricCodeEditDecision, metricActiveTime,
	} {
		if !IsClaudeCodeMetric(m) {
			t.Errorf("metric %q not recognized", m)
		}
	}
	if IsClaudeCodeMetric("claude_code.not_a_metric") {
		t.Error("a bogus metric must not be recognized")
	}
	if !isCostMetric(metricCostUsage) || !isCostMetric(metricTokenUsage) {
		t.Error("cost.usage/token.usage must be flagged as cost metrics")
	}
	if isCostMetric(metricSessionCount) {
		t.Error("session.count must not be flagged as a cost metric")
	}

	// A metrics batch carrying cost.usage feeds liveness ONLY (one onSignal per
	// resource batch); the receiver has NO observation sink on the metrics path, so a
	// metric VALUE can never become a CostSample (reconciliation, not summation — the
	// "one source of cost per session" invariant). The dispatch sink is left nil here:
	// were ingestMetrics ever to try to emit, it would nil-panic the test — that is the
	// structural guarantee, not a vacuous loop over an empty slice.
	var liveness int
	rcv := &receiver{
		onSignal: func(claudeIdentity, time.Time) { liveness++ },
		now:      func() time.Time { return d2Time },
	}
	rcv.ingestMetrics(&colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr(attrSessionID, "sess_1")}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{Name: metricCostUsage}},
			}},
		}},
	})
	if liveness != 1 {
		t.Errorf("liveness signals = %d, want 1 (and no CostSample path exists)", liveness)
	}
}

// TestKnownHookLifecycleSet proves the recognized hook set spans the FULL verified Claude
// Code lifecycle (code.claude.com/docs/en/hooks, 2026-06-19) — the gating, context
// and observe events — so the governed PEP classifies every one (an UNKNOWN event still
// deny-closes). The set is derived from hookSpecs so the count and the classification never
// drift.
func TestKnownHookLifecycleSet(t *testing.T) {
	for _, h := range []string{
		hookConfigChange, hookInstructionsLoaded, hookPostToolBatch, hookSubagentStart,
		hookPostCompact, hookElicitation, hookPermissionRequest, hookMessageDisplay,
		hookUserPromptSubmit, hookPreCompact, hookTaskCreated, hookTaskCompleted,
		hookUserPromptExpansion, hookTeammateIdle, hookWorktreeCreate, hookStopFailure,
	} {
		if !IsKnownHook(h) {
			t.Errorf("hook %q not in the recognized lifecycle set", h)
		}
	}
	if IsKnownHook("NotARealHook") {
		t.Error("a bogus hook must not be recognized")
	}
	// The recognized set IS the hookSpecs taxonomy (gating + context + observe).
	if got := len(KnownHookEvents()); got != len(hookSpecs) {
		t.Errorf("recognized hook events = %d, want len(hookSpecs)=%d", got, len(hookSpecs))
	}
}

// TestHookReturnSchema proves the per-event return schema: PreToolUse uses
// permissionDecision (and accepts "defer"); PermissionRequest uses the nested
// decision.behavior shape (VERIFIED 2026-06-19 — NOT permissionDecision, which Claude Code
// would silently ignore); "defer" is invalid on PermissionRequest; an invalid value → "{}".
func TestHookReturnSchema(t *testing.T) {
	// PreToolUse defer → permissionDecision=defer.
	out := parseHSO(t, hookDecision{event: hookPreToolUse, permission: permDefer, reason: "x"}.json())
	if out["permissionDecision"] != "defer" {
		t.Errorf("PreToolUse permissionDecision = %v, want defer", out["permissionDecision"])
	}

	// PermissionRequest deny → hookSpecificOutput.decision.behavior=deny, NO permissionDecision.
	out = parseHSO(t, hookDecision{event: hookPermissionRequest, permission: permDeny, reason: "blocked"}.json())
	if _, has := out["permissionDecision"]; has {
		t.Error("PermissionRequest must NOT emit permissionDecision (the verified schema is decision.behavior)")
	}
	if dec, _ := out["decision"].(map[string]any); dec == nil || dec["behavior"] != "deny" {
		t.Errorf("PermissionRequest deny output = %v, want decision.behavior=deny", out)
	}
	// PermissionRequest allow with a governed rewrite → behavior=allow + updatedInput.
	outAllow := parseHSO(t, hookDecision{event: hookPermissionRequest, permission: permAllow, updatedInput: map[string]any{"command": "ls"}}.json())
	dec, _ := outAllow["decision"].(map[string]any)
	if dec == nil || dec["behavior"] != "allow" || dec["updatedInput"] == nil {
		t.Errorf("PermissionRequest allow output = %v, want decision.behavior=allow + updatedInput", outAllow)
	}

	// defer is invalid on PermissionRequest → neutral "{}".
	if got := (hookDecision{event: hookPermissionRequest, permission: permDefer}).json(); string(got) != "{}" {
		t.Errorf("defer on PermissionRequest = %s, want {}", got)
	}
	// An unknown permission value → neutral "{}".
	if got := (hookDecision{event: hookPreToolUse, permission: "yolo"}).json(); string(got) != "{}" {
		t.Errorf("unknown permission = %s, want {}", got)
	}
	// permissionValueValid pins the per-event rules.
	if !permissionValueValid(hookPreToolUse, permDefer) || permissionValueValid(hookPermissionRequest, permDefer) {
		t.Error("defer must be valid only on PreToolUse")
	}
}

// TestHookDecisionOffByDefault proves the decision path stays OFF by default: a
// freshly-built connector wires no decider (cooperative-only, observe-never-gate).
func TestHookDecisionOffByDefault(t *testing.T) {
	s := New()
	if s.hookDecider(func(model.Observation) {}) != nil {
		t.Error("enforcement must be OFF by default (hookDecider should be nil)")
	}
}

// parseHSO decodes a hook decision payload's hookSpecificOutput object.
func parseHSO(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out struct {
		HSO map[string]any `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode hook decision %s: %v", body, err)
	}
	return out.HSO
}

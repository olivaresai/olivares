// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// findingKindEnforcement marks a governed Claude Code hook decision the connector
// returned at the edge (an allow/deny/ask gate it actually imposed). It is the
// audit/HITL counterpart of the synchronous decision: every enforced gate lands on
// the tamper-evident ledger and the SIEM export so the central control plane sees
// what the edge decided (CLA-01, wired to the governance/HITL trail).
const findingKindEnforcement = "enforcement"

// hookRule is one governed enforcement rule. It matches a PreToolUse /
// PermissionRequest hook when every specified condition matches; an unspecified
// condition is a wildcard. tool supports "" / "*" (any) and a trailing-"*" prefix
// glob (e.g. "mcp__*"); resource_kind and mode match the resource the connector
// derives from the tool input.
type hookRule struct {
	Tool         string `json:"tool,omitempty"`
	ResourceKind string `json:"resource_kind,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Decision     string `json:"decision"` // "deny" | "ask"
	Reason       string `json:"reason,omitempty"`
}

// hookEnforcementPolicy is the connector's LOCAL, opt-in permission policy
// (CLA-01). It is evaluated in-process on every permission-gating hook — there is
// NO network call into the engine on the agent's hot path: read-first means a
// slow or unreachable control plane must never wedge a developer's tool call
// (docs/SECURITY-HARDENING.md). The policy is the same artifact a governance / managed-settings
// authoring surface renders (CLA-05), so "what the org permits" is enforced at the
// edge exactly as Claude Code's own managed-settings permissions are. Default
// (no rules) = disabled: hooks are observed, never gated (cooperative-by-default).
type hookEnforcementPolicy struct {
	rules []hookRule
}

// enabled reports whether any enforcement rule is configured.
func (p hookEnforcementPolicy) enabled() bool { return len(p.rules) > 0 }

// parseEnforcement builds the policy from the connector's enforcement config (a
// JSON object {"rules":[…]}). Empty config yields the disabled policy. A malformed
// document or an invalid decision is a hard error so a typo fails LOUD at Open
// rather than silently leaving the fleet ungoverned (a security hole, not a
// degradation — unlike the other, degradable settings).
func parseEnforcement(raw string) (hookEnforcementPolicy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return hookEnforcementPolicy{}, nil
	}
	var doc struct {
		Rules []hookRule `json:"rules"`
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return hookEnforcementPolicy{}, fmt.Errorf("invalid enforcement policy: %w", err)
	}
	for i, r := range doc.Rules {
		switch r.Decision {
		case "deny", "ask":
		default:
			return hookEnforcementPolicy{}, fmt.Errorf("enforcement rule %d: decision must be \"deny\" or \"ask\", got %q", i, r.Decision)
		}
	}
	return hookEnforcementPolicy{rules: doc.Rules}, nil
}

// decide returns the governed decision for a permission-gating hook, or an empty
// decision when the policy is disabled, the hook is not enforcing, or no rule
// matches. It is pure (no I/O, no emission) so the handler can call it on the hot
// path; the finding is emitted by the caller (hookDecider).
func (p hookEnforcementPolicy) decide(ev hookEvent) hookDecision {
	if !p.enabled() || !isEnforcingHook(ev.event) {
		return hookDecision{}
	}
	kind, _, mode := resourceFromTool(ev.toolName, ev.input)
	for _, r := range p.rules {
		if r.matches(ev.toolName, kind, mode) {
			reason := r.Reason
			if reason == "" {
				reason = "blocked by Olivares governance policy"
			}
			return hookDecision{event: ev.event, permission: r.Decision, reason: reason}
		}
	}
	return hookDecision{}
}

// matches reports whether the rule's conditions all match the hook's tool, derived
// resource kind and inferred mode.
func (r hookRule) matches(tool, kind string, mode model.AccessMode) bool {
	if !toolGlobMatch(r.Tool, tool) {
		return false
	}
	if r.ResourceKind != "" && r.ResourceKind != kind {
		return false
	}
	if r.Mode != "" && r.Mode != string(mode) {
		return false
	}
	return true
}

// toolGlobMatch matches a rule pattern against a tool name: "" or "*" matches any;
// a trailing "*" is a prefix glob; otherwise it is an exact match.
func toolGlobMatch(pattern, s string) bool {
	switch {
	case pattern == "" || pattern == "*":
		return true
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(s, strings.TrimSuffix(pattern, "*"))
	default:
		return pattern == s
	}
}

// findingFromEnforcement records an imposed hook gate as a governance finding. A
// deny is medium severity (the connector blocked a tool call); an ask is low (it
// deferred to a human prompt). The resource reference is derived and redacted; the
// raw tool input is never carried.
func findingFromEnforcement(ev hookEvent, d hookDecision) model.FindingReport {
	sev := model.SeverityLow
	if d.permission == "deny" {
		sev = model.SeverityMedium
	}
	tool := ev.toolName
	if tool == "" {
		tool = "unknown"
	}
	return model.FindingReport{
		Kind:        findingKindEnforcement,
		Severity:    sev,
		SubjectKind: originSession,
		SubjectRef:  firstNonEmpty(ev.sessionID, "claude-hook"),
		Title:       "governed " + d.permission + " (" + ev.event + "): " + tool,
		DetailHash:  redact.Hash(ev.sessionID + "|" + ev.event + "|" + tool + "|" + d.permission),
		OccurredAt:  ev.at,
	}
}

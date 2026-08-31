// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// observe.go is the bridge from the ACTUATE side (a governed delegation) back to
// the OBSERVE graph (module IV). Audits every delegation to the ledger/OTel, but a
// governed delegation that actually leaves this plane is also a COMMUNICATION fact —
// "agent X delegated skill S to agent Y, with objective O" — and the operator must see it
// in the same agent↔agent graph the read-only Source already feeds (G14: the
// communication made visible). These pure mappers turn a delegation into the SDK wire
// shapes; the composition root (cmd, AGPL) publishes them onto the bus (a connector holds
// no Host). They emit the SAME edge shape the Source emits for an observed interaction
// (origin agent → resource a2a.agent), so module IV derives one `a2a` delegation relation
// and increments its delegation_count whether the edge came from observation or actuation.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): an edge/finding carries agent references, the skill, and a
// non-sensitive objective LABEL only — never the task text, a credential, or a payload.
// References are scrubbed for secret shapes (redact.Clean) and finding detail is hashed.

// delegationFindingKind is the finding kind for an audited governed delegation (the
// "who delegated what to whom, with what objective" record in the findings feed).
const delegationFindingKind = "a2a_delegation"

// AgentEdge builds an A2A agent→agent communication edge: originRef (the calling agent)
// delegated to targetAgent, exercising skill. Confidence is `attributed` because an A2A
// delegation only leaves this plane after the remote card's identity was verified
// (emit_task.go) — the edge records an established, governed relationship. It is the low-
// level builder both the Delegator-decision mapper and the scheduled-fire route (cmd) use.
// An edge with a blank origin or target is returned as-is; the caller/engine drops a
// non-attributable edge (module IV ignores an edge with an empty OriginRef).
func AgentEdge(originRef, targetAgent, skill string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originAgent,
		OriginRef:    redact.Clean(originRef),
		ResourceKind: resourceAgent,
		ResourceRef:  redact.Clean(targetAgent),
		Mode:         model.ModeUnknown,
		Source:       model.SignalA2A,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      redact.Clean(skill),
		ObservedAt:   at,
	}
}

// DelegationEdge maps an ALLOWED governed delegation decision to a communication edge for
// module IV. originRef is the calling/local agent reference the composition root supplies
// (e.g. the control plane's own agent identity, or the immediate caller in the chain) —
// the "who" the connector does not itself know. It returns ok=false for a decision that
// was NOT allowed (a denied delegation never created a communication edge) or that names
// no target/origin, so the caller can skip emission without inspecting the decision.
func DelegationEdge(dec DelegationDecision, originRef string, at time.Time) (model.EdgeObservation, bool) {
	if !dec.Allowed {
		return model.EdgeObservation{}, false
	}
	if strings.TrimSpace(originRef) == "" || strings.TrimSpace(dec.AgentName) == "" {
		return model.EdgeObservation{}, false
	}
	return AgentEdge(originRef, dec.AgentName, dec.Skill, at), true
}

// DelegationFinding maps a governed delegation decision to a finding for the SOC/audit
// feed — the "with what objective" record. An ALLOWED delegation is Info (a normal,
// governed communication); a DENIED one is Low (a refused delegation is worth surfacing,
// the gap is a signal). The title is non-sensitive (agent + skill + objective LABEL); the
// detail (which carries the plan/approval/chain refs) is HASHED. The objective is a label,
// never the task text.
func DelegationFinding(dec DelegationDecision, at time.Time) model.FindingReport {
	sev := model.SeverityInfo
	verb := "delegated"
	if !dec.Allowed {
		sev = model.SeverityLow
		verb = "delegation denied"
	}
	title := "A2A " + verb + ": skill " + skillLabel(dec.Skill) + " → agent " + agentLabel(dec.AgentName)
	// The objective is contractually a non-sensitive label, but the title is "safe to
	// display" (sdk/model FindingReport.Title) and the scrubber must never under-redact —
	// so scrub it like every sibling title field, defense-in-depth against a caller that
	// ever violates the label contract. The raw objective stays only in the hashed detail.
	if obj := strings.TrimSpace(redact.Clean(dec.Objective)); obj != "" {
		title += " (objective: " + obj + ")"
	}
	detail := "a2a-delegation" +
		" agent=" + dec.AgentName + " skill=" + dec.Skill + " scope=" + dec.Scope +
		" objective=" + dec.Objective + " allowed=" + boolStr(dec.Allowed) +
		" reason=" + dec.Reason + " plan=" + dec.PlanHash + " approval=" + dec.ApprovalRef +
		" chain_depth=" + strconv.Itoa(dec.ChainDepth) + " chain_root=" + dec.ChainRoot +
		" principal=" + dec.Principal +
		" state=" + string(dec.State)
	return model.FindingReport{
		Kind:        delegationFindingKind,
		Severity:    sev,
		SubjectKind: subjectAgent,
		SubjectRef:  redact.Clean(dec.AgentName),
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OWASPASI:    []string{"ASI03"}, // Identity & Privilege Abuse — governed inter-agent delegation
		OccurredAt:  at,
	}
}

// skillLabel / agentLabel return a non-sensitive, scrubbed label for a title (a blank
// skill renders as "(none)" so a skill-less delegation reads honestly).
func skillLabel(skill string) string {
	if s := strings.TrimSpace(skill); s != "" {
		return redact.Clean(s)
	}
	return "(none)"
}

func agentLabel(agent string) string {
	if a := strings.TrimSpace(agent); a != "" {
		return redact.Clean(a)
	}
	return "(unknown)"
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// pep.go is the per-delegation Policy-Enforcement-Point: the deny-by-default
// authorization every A2A delegation passes through BEFORE a Task leaves for a remote
// agent (docs/SECURITY-HARDENING.md — actuation is the exception to read-first, and every actuation
// goes through a deny-closed gate). It is two independent, composable controls:
//
//  1. Allowlist (local, deterministic, no human): is the (remote agent, skill, scope)
//     tuple explicitly permitted for this caller? Deny-by-default least-privilege —
//     an unlisted agent/skill/scope is refused with no network call. This is the PEP
//     that always applies, mirroring the MCP tools/call mcp_toolset map (server-owned
//     policy, never trusting the peer).
//  2. ApprovalGate (the human-in-the-loop seam): the delegation is bound to a
//     PlanHash (a normalized agent+skill+scope+params tuple a human saw, anti-TOCTOU)
//     and authorized by the gate. The connector defines the seam and a deny-closed
//     default; the composition root (cmd, AGPL) wires the real bridge — a
//     connector may not import /core (LICENSING.md).
//
// Both fail closed: an unlisted tuple, a gate that does not return Allowed(), or any
// gate error all DENY. The token/credentials never appear here (they are out-of-band
// HTTP headers, emit_task.go); the PEP reasons only over references + scopes (minimal
// data, docs/SECURITY-HARDENING.md).

// GateStatus is the effective decision the delegation gate reports. Every value
// except StatusApproved is a DENY (it mirrors the orchestration/voice gate vocabulary
// so the cmd bridge maps 1:1, but is defined here because a connector is Apache-2.0
// and cannot import an AGPL module).
type GateStatus string

const (
	// StatusApproved is the only status that authorizes a delegation.
	StatusApproved GateStatus = "approved"
	// StatusPending: a HITL request is open but undecided — deny, keep waiting.
	StatusPending GateStatus = "pending"
	// StatusRejected: a human rejected the delegation — deny.
	StatusRejected GateStatus = "rejected"
	// StatusExpired: the approval lapsed before the delegation — deny.
	StatusExpired GateStatus = "expired"
	// StatusNoGate: no gate is wired — deny (deny-by-default).
	StatusNoGate GateStatus = "no_gate"
)

// DelegationRequest is the minimal-data description of a prospective delegation the
// gate authorizes. PlanHash binds the request to the exact agent+skill+scope+params
// tuple (anti-TOCTOU); RequestedBy is the audit actor of the asking principal. It
// carries NO message text, NO credentials, NO payload (docs/SECURITY-HARDENING.md).
type DelegationRequest struct {
	Tenant      string // opaque tenant id (the cmd bridge resolves it; "" for single-tenant)
	AgentName   string // logical name of the remote agent
	Skill       string // skill id being delegated (may be "")
	Scope       string // least-privilege scope being exercised (may be "")
	PlanHash    string // canonical bound plan (PlanHash())
	RequestedBy string // audit actor of the requesting principal (provenance)
}

// GateDecision is the gate's answer for one delegation. Allowed() is the ONLY
// authorization; every other status (including the zero value, StatusNoGate via the
// empty string defaulting to deny) is a deny.
type GateDecision struct {
	ApprovalRef string
	Status      GateStatus
	PlanHash    string // the plan the approval was bound to, echoed for confirmation
}

// Allowed reports whether this decision authorizes the delegation — true ONLY for an
// explicit approval bound to the matching plan. The empty/zero value is a deny.
func (d GateDecision) Allowed() bool { return d.Status == StatusApproved }

// DelegationGate is the governance HITL seam for a delegation. The real adapter
// (cmd/olivares) bridges to the ApprovalGate (POST /v1/m/governance/approvals
// bound to the PlanHash). The connector never decides — it asks and consumes.
type DelegationGate interface {
	Authorize(ctx context.Context, req DelegationRequest) (GateDecision, error)
}

// denyDelegationGate is the deny-closed default: with no gate wired, every delegation
// is denied. It is NOT a silent no-op — it returns an explicit no_gate decision so a
// Delegator with no gate cannot actuate (mirrors the module denyGate pattern). The
// Delegator warns once when it falls back to this default.
type denyDelegationGate struct{}

func (denyDelegationGate) Authorize(_ context.Context, req DelegationRequest) (GateDecision, error) {
	return GateDecision{ApprovalRef: "no-gate:" + req.PlanHash, Status: StatusNoGate, PlanHash: req.PlanHash}, nil
}

// AllowRule grants a caller the right to delegate to one remote Agent for one Skill,
// restricted to the listed Scopes. Agent is matched EXACTLY (no wildcard — you must
// name the agent you delegate to: that is the core of least-privilege). Skill matches
// exactly or via the "*" wildcard. Scope is matched against Scopes (see
// (Allowlist).Allowed). It is operator-owned policy (server side), never derived from
// the peer's AgentCard or the caller's request.
type AllowRule struct {
	Agent  string   `json:"agent"`
	Skill  string   `json:"skill"`
	Scopes []string `json:"scopes"`
}

// Allowlist is the deny-by-default set of permitted (agent, skill, scope) tuples. An
// empty allowlist denies everything (deny-by-default) — there is no "allow all" mode.
type Allowlist struct {
	rules []AllowRule
}

// NewAllowlist builds an allowlist from operator rules. A copy is taken so the caller
// cannot mutate the policy after construction.
func NewAllowlist(rules []AllowRule) *Allowlist {
	cp := make([]AllowRule, len(rules))
	copy(cp, rules)
	return &Allowlist{rules: cp}
}

// Allowed reports whether delegating to agent for skill at scope is permitted. It is
// deny-by-default: it returns true only when SOME rule matches the agent exactly, the
// skill (exactly or via "*"), AND the scope. A nil allowlist denies everything (the
// safe default for an un-provisioned Delegator).
func (a *Allowlist) Allowed(agent, skill, scope string) bool {
	if a == nil {
		return false
	}
	agent = strings.TrimSpace(agent)
	skill = strings.TrimSpace(skill)
	scope = strings.TrimSpace(scope)
	for _, r := range a.rules {
		if strings.TrimSpace(r.Agent) != agent || agent == "" {
			continue
		}
		if rs := strings.TrimSpace(r.Skill); rs != "*" && rs != skill {
			continue
		}
		if scopeAllowed(r.Scopes, scope) {
			return true
		}
	}
	return false
}

// AllowedAgent reports whether the allowlist names this agent in ANY rule — the
// coarser "is this agent reachable at all" check used for non-delegating operations
// (resume/cancel/reconcile) where no specific skill/scope is being exercised. It is
// still deny-by-default: an agent absent from every rule is refused.
func (a *Allowlist) AllowedAgent(agent string) bool {
	if a == nil {
		return false
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return false
	}
	for _, r := range a.rules {
		if strings.TrimSpace(r.Agent) == agent {
			return true
		}
	}
	return false
}

// scopeAllowed reports whether a requested scope is permitted by a rule's scope set.
// "*" grants any scope. An EMPTY rule scope-set grants ONLY a scopeless delegation
// (scope==""), so a rule that lists no scopes can never authorize a privileged scope —
// deny-by-default down to the scope dimension.
func scopeAllowed(ruleScopes []string, scope string) bool {
	if scope == "" {
		if len(ruleScopes) == 0 {
			return true
		}
		return containsScope(ruleScopes, "") || containsScope(ruleScopes, "*")
	}
	return containsScope(ruleScopes, "*") || containsScope(ruleScopes, scope)
}

func containsScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}

// planHashVersion namespaces the canonical plan-hash so a future change to the tuple
// shape cannot collide with an existing bound approval.
const planHashVersion = "a2a-delegation-v1"

// PlanHash computes the canonical, anti-TOCTOU binding for a delegation: a stable
// SHA-256 over the normalized (agent, skill, scope, paramsHash) tuple. The same tuple
// always yields the same hash; any re-target (different agent), re-skill, re-scope, or
// changed params changes the hash and voids a stale approval. paramsHash is a caller-
// computed digest of the (minimal-data) task parameters — never the raw text. The
// fields are length-prefixed so no separator collision can forge a matching plan.
func PlanHash(agent, skill, scope, paramsHash string) string {
	h := sha256.New()
	for _, part := range []string{
		planHashVersion,
		strings.TrimSpace(agent),
		strings.TrimSpace(skill),
		strings.TrimSpace(scope),
		strings.TrimSpace(paramsHash),
	} {
		var lenbuf [8]byte
		n := len(part)
		for i := 0; i < 8; i++ {
			lenbuf[i] = byte(n >> (8 * (7 - i)))
		}
		_, _ = h.Write(lenbuf[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashParams computes a stable, order-independent digest of the minimal-data task
// parameters (skill metadata keys + text length class — never the raw text), so the
// PlanHash binds the SHAPE of the task a human approved without persisting its
// content. It is deterministic across runs.
func hashParams(skill, contextID string, textLen int) string {
	parts := []string{
		"skill=" + strings.TrimSpace(skill),
		"context=" + strings.TrimSpace(contextID),
		"textlen=" + lenClass(textLen),
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

// lenClass buckets a text length so the params digest is stable against trivial edits
// while still changing for a materially different instruction size (minimal data: the
// exact length is not persisted, only its bucket).
func lenClass(n int) string {
	switch {
	case n == 0:
		return "0"
	case n <= 256:
		return "s"
	case n <= 4096:
		return "m"
	case n <= 65536:
		return "l"
	default:
		return "xl"
	}
}

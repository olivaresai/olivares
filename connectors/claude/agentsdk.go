// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file models the Claude Agent SDK / Managed Agents governance surface (CLA-10):
// the SDK config knobs a control plane must be able to OBSERVE and CONSTRAIN, and the
// drift check that flags when an observed agent runs more permissively than policy
// allows. It is MODELING/OBSERVATION — it never builds or launches an agent.
//
// The connector already observes permissionMode transitions over Claude Code OTEL
// (evtPermissionMode → ev.toMode). This adds the POLICY side: a declared
// AgentSDKPolicy (the maximum posture an operator permits) and verifyAgentSDKMode,
// which emits a drift finding when the observed mode exceeds the policy — and crosses
// it against managed-settings (CLA-05 /) through a fail-closed seam, so the cap is
// the STRICTER of the operator policy and the enterprise-managed setting.
package claude

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// PermissionMode is a Claude Agent SDK permission mode. The full set is verified
// against the Agent SDK reference (jun-2026): exactly these six values.
type PermissionMode string

const (
	PMDefault     PermissionMode = "default"           // standard permission behavior
	PMPlan        PermissionMode = "plan"              // planning mode — read-only tools only
	PMAcceptEdits PermissionMode = "acceptEdits"       // auto-accept file edits
	PMAuto        PermissionMode = "auto"              // a model classifier approves/denies each call
	PMDontAsk     PermissionMode = "dontAsk"           // never prompt; deny if not pre-approved
	PMBypass      PermissionMode = "bypassPermissions" // bypass all permission checks
)

// permissionModeRank orders the modes from least to most permissive (lower = stricter).
// plan is the safest (read-only exploration); dontAsk is next — it runs ONLY pre-approved
// tools and HARD-DENIES everything else (canUseTool is never called), so its autonomous
// grant set is a strict SUBSET of default/acceptEdits/auto. default routes unmatched calls
// to canUseTool (which can still approve); acceptEdits auto-approves file/write ops; auto's
// classifier can approve arbitrary calls; bypassPermissions approves everything. An unknown
// mode is absent (fails closed in the comparisons below).
//
// VERIFIED 2026-06-19 (code.claude.com/docs/en/{permission-modes,agent-sdk/permissions}):
// dontAsk is one of the MOST restrictive modes, NOT a permissive one — ranking it above
// default/acceptEdits/auto would mis-flag a TIGHTENED fleet as drift and, worse, make
// stricter() discard a tighter managed dontAsk cap as if it were looser (a real gate
// weakening). This ranking is consistent with ResolveSDKDecision, where dontAsk denies the
// canUseTool fall-through every looser mode can still approve.
var permissionModeRank = map[PermissionMode]int{
	PMPlan: 0, PMDontAsk: 1, PMDefault: 2, PMAcceptEdits: 3, PMAuto: 4, PMBypass: 5,
}

// Valid reports whether m is one of the six known modes.
func (m PermissionMode) Valid() bool { _, ok := permissionModeRank[m]; return ok }

// MorePermissiveThan reports whether m grants strictly more than limit. An unknown m
// is treated as MORE permissive than any known limit (fail closed: an unrecognized
// mode must not silently pass a policy gate); an unknown limit is treated as the
// strictest (everything exceeds it) so a garbage policy never blesses an escalation.
func (m PermissionMode) MorePermissiveThan(limit PermissionMode) bool {
	mr, mok := permissionModeRank[m]
	lr, lok := permissionModeRank[limit]
	if !mok {
		return true
	}
	if !lok {
		return true
	}
	return mr > lr
}

// stricter returns the stricter (less permissive) of two caps. An UNKNOWN cap is
// treated as the strictest (it dominates) — fail closed: an un-rankable managed
// setting must not be silently discarded and weaken the gate; it surfaces as a
// violation the operator can see and fix.
func stricter(a, b PermissionMode) PermissionMode {
	ar, aok := permissionModeRank[a]
	br, bok := permissionModeRank[b]
	switch {
	case !aok:
		return a
	case !bok:
		return b
	case ar <= br:
		return a
	default:
		return b
	}
}

// AgentSDKConfig is the observed/declared SDK configuration the control plane models
// (CLA-10 /). It is structural metadata only — never a prompt, a tool argument or
// a secret. canUseTool is modeled as a PRESENCE flag (whether a programmatic gate is
// wired), not the callback code; the dangerous knobs below are likewise presence/scalars,
// never their contents.
//
// The dangerous-knob fields are VERIFIED against the Agent SDK reference
// (code.claude.com/docs/en/agent-sdk/typescript, 2026-06-19): every one is a real
// ClaudeAgentOptions field, not a fabricated name.
type AgentSDKConfig struct {
	PermissionMode  PermissionMode `json:"permission_mode"`
	AllowedTools    []string       `json:"allowed_tools,omitempty"`
	DisallowedTools []string       `json:"disallowed_tools,omitempty"`
	SettingSources  []string       `json:"setting_sources,omitempty"` // user|project|local
	Agents          []string       `json:"agents,omitempty"`          // subagent names
	CanUseTool      bool           `json:"can_use_tool,omitempty"`
	SessionResume   bool           `json:"session_resume,omitempty"`
	ForkSession     bool           `json:"fork_session,omitempty"`

	// --- dangerous knobs (presence/scalars; never the contents) ---

	// SessionStore reports whether `sessionStore` is wired — it "mirrors session
	// transcripts to an external backend so any host can resume them": an off-box
	// transcript-exfiltration surface the control plane must SEE.
	SessionStore bool `json:"session_store,omitempty"`
	// MaxBudgetUsd is the `maxBudgetUsd` value (0 = unset). It is a CLIENT-SIDE cost
	// ESTIMATE cap (compared against the same estimate as total_cost_usd), NOT a hard,
	// server-enforced spend limit — modeled so governance is honest about its weakness.
	MaxBudgetUsd float64 `json:"max_budget_usd,omitempty"`
	// AllowDangerouslySkipPermissions is `allowDangerouslySkipPermissions`: "Enable
	// bypassing permissions. Required when using permissionMode: 'bypassPermissions'."
	AllowDangerouslySkipPermissions bool `json:"allow_dangerously_skip_permissions,omitempty"`
	// Plugins are the `plugins` entries ("Load custom plugins from local paths"): an
	// executable supply-chain surface. Modeled as refs (names/paths), folded into a hash.
	Plugins []string `json:"plugins,omitempty"`
	// PermissionPromptToolName is `permissionPromptToolName` ("MCP tool name for
	// permission prompts"): the MCP-delegate of canUseTool. Delegating permission
	// decisions to a tool the control plane does not sanction routes them OUTSIDE
	// governance — see the pep.go permissionPromptToolName route, which is the governed
	// destination an operator should point it at.
	PermissionPromptToolName string `json:"permission_prompt_tool_name,omitempty"`
}

// AgentSDKPolicy is the operator-declared MAXIMUM posture. An observed config that
// exceeds it is drift. Empty fields are "unconstrained". The per-knob authorizations
// let an operator EXPLICITLY permit a dangerous knob, degrading its finding from
// HIGH to an Info observation (2026-06-19: uniform HIGH→Info across every knob);
// absent = not authorized (the knob, when present, is HIGH).
type AgentSDKPolicy struct {
	// MaxPermissionMode is the most permissive mode allowed (observed must not exceed).
	MaxPermissionMode PermissionMode `json:"max_permission_mode"`

	AllowSessionStore    bool `json:"allow_session_store,omitempty"`
	AllowSkipPermissions bool `json:"allow_skip_permissions,omitempty"`
	AllowPlugins         bool `json:"allow_plugins,omitempty"`
	AllowMaxBudget       bool `json:"allow_max_budget,omitempty"`
	// PermissionPromptTool is the operator-SANCTIONED permission-prompt MCP tool (the
	// governed route). An observed permissionPromptToolName that MATCHES it is the good,
	// governed path (Info); one that differs — or any value when no sanctioned tool is
	// declared — delegates permission decisions OUTSIDE governance (HIGH).
	PermissionPromptTool string `json:"permission_prompt_tool,omitempty"`
}

// IsSet reports whether a policy was declared (a max permission mode is named).
func (p AgentSDKPolicy) IsSet() bool { return p.MaxPermissionMode != "" }

// ManagedSettingsConstraint is the fail-closed cross-reference seam to enterprise
// managed-settings (CLA-05). The composition root injects the real
// reader; until then it is nil and the cross-ref is skipped (the operator policy
// still applies). MaxPermissionMode returns the managed cap and ok=true when a
// managed setting pins permissionMode.
type ManagedSettingsConstraint interface {
	MaxPermissionMode() (PermissionMode, bool)
}

// parseAgentSDKPolicy parses the connector's agent_sdk_policy config (JSON). Empty =
// no policy (nil, ok=false). A malformed policy is a hard error (governance integrity
// — same posture as the enforcement policy).
func parseAgentSDKPolicy(s string) (AgentSDKPolicy, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return AgentSDKPolicy{}, false, nil
	}
	var p AgentSDKPolicy
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return AgentSDKPolicy{}, false, fmt.Errorf("claude: agent_sdk_policy is not valid JSON: %w", err)
	}
	if p.MaxPermissionMode != "" && !p.MaxPermissionMode.Valid() {
		return AgentSDKPolicy{}, false, fmt.Errorf("claude: agent_sdk_policy.max_permission_mode %q is not one of default|plan|acceptEdits|auto|dontAsk|bypassPermissions", p.MaxPermissionMode)
	}
	return p, p.IsSet(), nil
}

// verifyAgentSDKMode is the live drift check (CLA-10): given an OBSERVED permission
// mode (from evtPermissionMode), it emits a policy-violation finding when the mode
// exceeds the effective cap — the STRICTER of the declared policy and any managed
// setting (the CLA-05 cross-ref). It returns ok=false when there is no policy, no
// observed mode, or the observed mode is within policy. Minimal-data: the finding
// carries a non-sensitive title and a hashed detail, never raw config.
func verifyAgentSDKMode(observed, sessionID string, at time.Time, policy AgentSDKPolicy, managed ManagedSettingsConstraint) (model.FindingReport, bool) {
	if !policy.IsSet() || observed == "" {
		return model.FindingReport{}, false
	}
	limit := policy.MaxPermissionMode
	managedNote := "policy"
	if managed != nil {
		if mc, ok := managed.MaxPermissionMode(); ok {
			if stricter(limit, mc) == mc && mc != limit {
				managedNote = "managed-settings"
			}
			limit = stricter(limit, mc)
		}
	}
	obs := PermissionMode(observed)
	if !obs.MorePermissiveThan(limit) {
		return model.FindingReport{}, false
	}
	sev := model.SeverityMedium
	if obs == PMBypass || !obs.Valid() {
		sev = model.SeverityHigh
	}
	subject := sessionID
	if subject == "" {
		subject = "unknown"
	}
	return model.FindingReport{
		Kind:        findingKindPolicyChange,
		Severity:    sev,
		SubjectKind: originSession,
		SubjectRef:  subject,
		Title:       "Agent SDK permission mode " + observed + " exceeds " + managedNote + " cap " + string(limit),
		DetailHash:  redact.Hash(subject + "|" + observed + "|" + string(limit) + "|" + managedNote),
		OccurredAt:  at,
	}, true
}

// ---- real Agent SDK permission-evaluation precedence model ------------------
//
// The Agent SDK evaluates a tool request through a FIXED, ordered set of steps (VERIFIED
// 2026-06-19, code.claude.com/docs/en/agent-sdk/permissions). The whole point of modeling
// it is that the EARLY steps bind even when a LATER step (the permission mode) would
// auto-approve: a hook can deny outright, and a scoped deny / explicit ask rule holds even
// in bypassPermissions. This is a PURE model the control plane reasons against; it does
// NOT enforce — the SDK does.

// SDKEvalStep identifies one step of the evaluation, in order (lower runs earlier).
type SDKEvalStep int

const (
	StepHooks          SDKEvalStep = iota // hooks run FIRST; a hook may deny outright
	StepDenyRules                         // scoped deny rules — block even in bypassPermissions
	StepAskRules                          // ask rules — prompt even in bypass; denied in dontAsk
	StepPermissionMode                    // bypass approves; acceptEdits approves writes; plan routes writes to canUseTool
	StepAllowRules                        // allow rules auto-approve
	StepCanUseTool                        // canUseTool / permissionPromptToolName catch-all; skipped (deny) in dontAsk
)

// String names the step for findings/preview (non-sensitive).
func (s SDKEvalStep) String() string {
	switch s {
	case StepHooks:
		return "hooks"
	case StepDenyRules:
		return "deny-rules"
	case StepAskRules:
		return "ask-rules"
	case StepPermissionMode:
		return "permission-mode"
	case StepAllowRules:
		return "allow-rules"
	case StepCanUseTool:
		return "canUseTool"
	default:
		return "unknown"
	}
}

// SDKEvalRule pairs a step with its verified one-line semantics — the reference the
// console surfaces so an operator understands why a managed deny holds under bypass.
type SDKEvalRule struct {
	Step SDKEvalStep `json:"step"`
	Rule string      `json:"rule"`
}

// SDKEvaluationOrder returns the verified evaluation order (reference data, not
// fabricated). It is the exposed contract dependents read.
func SDKEvaluationOrder() []SDKEvalRule {
	return []SDKEvalRule{
		{StepHooks, "hooks run FIRST; a hook can deny the call outright or pass it on (a hook 'allow' does NOT skip the deny/ask rules below)"},
		{StepDenyRules, "a matching scoped deny rule (disallowedTools, e.g. Bash(rm *)) blocks the tool EVEN IN bypassPermissions"},
		{StepAskRules, "a matching ask rule falls through to canUseTool EVEN IN bypassPermissions; in dontAsk a matching ask is DENIED instead"},
		{StepPermissionMode, "bypassPermissions approves everything reaching here; acceptEdits approves file/write ops; plan routes writes to canUseTool; other modes fall through"},
		{StepAllowRules, "a matching allow rule (allowedTools) auto-approves"},
		{StepCanUseTool, "unresolved calls hit canUseTool / permissionPromptToolName; in dontAsk this step is skipped and the tool is denied"},
	}
}

// SDKToolRequest carries the SIGNALS the documented flow branches on for one tool-call —
// the RESULT of each step's match (a hook verdict, whether a scoped deny/ask/allow rule
// matches), never raw tool arguments (minimal-data). Writes marks a tool that edits files
// or writes via shell (the class acceptEdits auto-approves and plan routes to canUseTool).
type SDKToolRequest struct {
	Mode         PermissionMode
	HookDenies   bool // a hook returned deny
	DenyMatches  bool // a scoped deny rule matches
	AskMatches   bool // an ask rule matches
	AllowMatches bool // an allow rule matches
	HasResolver  bool // a canUseTool callback OR a permissionPromptToolName is wired
	Writes       bool // the tool edits files / writes via shell
}

// SDKDecision is the verdict ResolveSDKDecision computes and the step that decided it.
type SDKDecision struct {
	Outcome   string // permAllow | permDeny | permAsk
	DecidedBy SDKEvalStep
}

// ResolveSDKDecision models the Agent SDK permission evaluation EXACTLY as documented
// (verified 2026-06-19). It encodes the precedence the control plane reasons about; it
// does NOT enforce. Honesty: the `auto` classifier is a SEPARATE second gate
// (auto-mode-config) that runs AFTER this flow, so auto is treated here as "falls through"
// like default — matching the permissions page's "other modes fall through".
func ResolveSDKDecision(r SDKToolRequest) SDKDecision {
	// 1. Hooks first — a hook can deny outright. (A hook 'allow' does not skip the deny/
	//    ask rules, so only a hook DENY short-circuits here.)
	if r.HookDenies {
		return SDKDecision{permDeny, StepHooks}
	}
	// 2. Deny rules — a matching scoped deny blocks the tool even in bypassPermissions.
	if r.DenyMatches {
		return SDKDecision{permDeny, StepDenyRules}
	}
	// 3. Ask rules — a match prompts via canUseTool even in bypassPermissions; in dontAsk
	//    a matching ask is DENIED (that mode never prompts).
	if r.AskMatches {
		if r.Mode == PMDontAsk {
			return SDKDecision{permDeny, StepAskRules}
		}
		return SDKDecision{permAsk, StepAskRules}
	}
	// 4. Permission mode.
	switch r.Mode {
	case PMBypass:
		return SDKDecision{permAllow, StepPermissionMode} // approves everything reaching here
	case PMAcceptEdits:
		if r.Writes {
			return SDKDecision{permAllow, StepPermissionMode} // file/write ops auto-approved
		}
		// non-write tools fall through to allow rules / canUseTool
	case PMPlan:
		if r.Writes {
			return SDKDecision{permAsk, StepPermissionMode} // writes never auto-approved → canUseTool
		}
		// read-only tools behave as default → fall through
	}
	// 5. Allow rules.
	if r.AllowMatches {
		return SDKDecision{permAllow, StepAllowRules}
	}
	// 6. canUseTool / permissionPromptToolName catch-all. In dontAsk this step is skipped
	//    and the tool is denied. Otherwise a wired resolver prompts (ask); with NO resolver
	//    the call cannot be resolved — we model that as deny-closed (the SDK host would
	//    prompt or error; a control plane treats an unresolvable call as blocked).
	if r.Mode == PMDontAsk {
		return SDKDecision{permDeny, StepCanUseTool}
	}
	if r.HasResolver {
		return SDKDecision{permAsk, StepCanUseTool}
	}
	return SDKDecision{permDeny, StepCanUseTool}
}

// ---- subagent inheritance (the dominant multi-agent risk) -------------------

// inheritingModes are the parent permission modes that PROPAGATE to every subagent and
// CANNOT be overridden per subagent (verified 2026-06-19): bypassPermissions, acceptEdits,
// auto. An explicit ask rule still forces a prompt even for an inheriting subagent.
var inheritingModes = map[PermissionMode]bool{PMBypass: true, PMAcceptEdits: true, PMAuto: true}

// InheritsToSubagents reports whether a parent in mode forces that mode onto its
// subagents non-overridably.
func InheritsToSubagents(mode PermissionMode) bool { return inheritingModes[mode] }

// subagentInheritanceSeverity maps an inheriting parent mode to the finding severity
// (2026-06-19): bypassPermissions = HIGH (subagents get full, autonomous system
// access, non-overridable), acceptEdits/auto = MEDIUM.
var subagentInheritanceSeverity = map[PermissionMode]model.Severity{
	PMBypass:      model.SeverityHigh,
	PMAcceptEdits: model.SeverityMedium,
	PMAuto:        model.SeverityMedium,
}

// subagentInheritanceFinding emits the dominant multi-agent risk: a parent in an
// inheriting mode with declared subagents propagates that mode to EVERY subagent, and it
// cannot be overridden per subagent. Returns false when the mode does not inherit or no
// subagents are declared.
func (c AgentSDKConfig) subagentInheritanceFinding(subject string, at time.Time) (model.FindingReport, bool) {
	if len(c.Agents) == 0 || !InheritsToSubagents(c.PermissionMode) {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingKindPolicyChange,
		Severity:    subagentInheritanceSeverity[c.PermissionMode],
		SubjectKind: subjectAgentSDK,
		SubjectRef:  "subagent_inheritance",
		Title:       fmt.Sprintf("Agent SDK parent in %s propagates to %d subagent(s); mode cannot be overridden per subagent", c.PermissionMode, len(c.Agents)),
		DetailHash:  redact.Hash(subject + "|subagent-inheritance|" + string(c.PermissionMode) + "|" + strings.Join(c.Agents, ",")),
		OccurredAt:  at,
	}, true
}

// ---- dangerous-knob findings (the surface that makes AgentSDKConfig LIVE) ---

// subjectAgentSDK is the FindingReport SubjectKind for the Agent SDK program posture.
const subjectAgentSDK = "claude.agentsdk"

// knobFinding builds a dangerous-knob finding. A knob the operator has NOT authorized in
// policy is HIGH; one EXPLICITLY authorized degrades to an Info observation (2026-06-19). Minimal-data: the title is non-sensitive, the specifics ride a hash.
func knobFinding(subject, knob, title, detail string, authorized bool, at time.Time) model.FindingReport {
	sev := model.SeverityHigh
	if authorized {
		sev = model.SeverityInfo
		title += " — explicitly authorized by policy"
	}
	return model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectAgentSDK,
		SubjectRef:  knob,
		Title:       title,
		DetailHash:  redact.Hash(subject + "|" + knob + "|" + detail),
		OccurredAt:  at,
	}
}

// PostureFindings turns an OBSERVED/DECLARED Agent SDK program config into governance
// findings — the surface that makes AgentSDKConfig LIVE instead of dead code. It
// emits a finding per dangerous knob (HIGH, or Info when policy authorizes it), the
// dominant subagent bypass-inheritance finding, and the effective-cap drift (declared
// permissionMode vs the STRICTER of operator policy and managed-settings, reusing
// verifyAgentSDKMode — no second precedence engine). subject names the program/fleet the
// config belongs to (non-secret).
func (c AgentSDKConfig) PostureFindings(subject string, at time.Time, policy AgentSDKPolicy, managed ManagedSettingsConstraint) []model.FindingReport {
	subject = firstNonEmpty(subject, "agent-sdk-program")
	var out []model.FindingReport

	if c.SessionStore {
		out = append(out, knobFinding(subject, "sessionStore",
			"Agent SDK sessionStore mirrors session transcripts to an external backend (off-box exfiltration surface)",
			"sessionStore=on", policy.AllowSessionStore, at))
	}
	if c.AllowDangerouslySkipPermissions {
		out = append(out, knobFinding(subject, "allowDangerouslySkipPermissions",
			"Agent SDK allowDangerouslySkipPermissions is enabled (unlocks bypassPermissions — tools run with no permission prompts)",
			"allowDangerouslySkipPermissions=true", policy.AllowSkipPermissions, at))
	}
	if len(c.Plugins) > 0 {
		out = append(out, knobFinding(subject, "plugins",
			fmt.Sprintf("Agent SDK loads %d custom plugin(s) from local paths (executable supply-chain surface)", len(c.Plugins)),
			"plugins|"+strings.Join(c.Plugins, ","), policy.AllowPlugins, at))
	}
	if c.MaxBudgetUsd > 0 {
		out = append(out, knobFinding(subject, "maxBudgetUsd",
			"Agent SDK maxBudgetUsd sets a CLIENT-SIDE cost-estimate cap (not a hard, server-enforced spend limit)",
			fmt.Sprintf("maxBudgetUsd=%g", c.MaxBudgetUsd), policy.AllowMaxBudget, at))
	}
	if ppt := strings.TrimSpace(c.PermissionPromptToolName); ppt != "" {
		governed := policy.PermissionPromptTool != "" && policy.PermissionPromptTool == ppt
		title := "Agent SDK permissionPromptToolName delegates permission prompts to an MCP tool not sanctioned for governance"
		if governed {
			title = "Agent SDK permissionPromptToolName routes permission prompts to the governance-sanctioned MCP tool"
		}
		out = append(out, knobFinding(subject, "permissionPromptToolName", title, "permissionPromptToolName|"+ppt, governed, at))
	}

	if f, ok := c.subagentInheritanceFinding(subject, at); ok {
		out = append(out, f)
	}
	if f, ok := verifyAgentSDKMode(string(c.PermissionMode), subject, at, policy, managed); ok {
		// verifyAgentSDKMode labels the drift as a SESSION finding (correct for the live
		// OTEL path, where the subject IS a session). Here the subject is the declared
		// PROGRAM, so relabel it to the agent-SDK posture kind — keeping every finding from
		// this program under one SubjectKind (a console groups posture by SubjectKind).
		f.SubjectKind = subjectAgentSDK
		out = append(out, f)
	}
	return out
}

// parseAgentSDKConfig parses the operator-DECLARED Agent SDK program configuration (JSON)
// the connector models for governance. Empty = none (zero, ok=false). A malformed
// config is a hard error (governance integrity — same posture as the policy): a control
// plane that cannot parse the declared fleet config must not silently skip its findings.
func parseAgentSDKConfig(s string) (AgentSDKConfig, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return AgentSDKConfig{}, false, nil
	}
	var c AgentSDKConfig
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return AgentSDKConfig{}, false, fmt.Errorf("claude: agent_sdk_config is not valid JSON: %w", err)
	}
	if c.PermissionMode != "" && !c.PermissionMode.Valid() {
		return AgentSDKConfig{}, false, fmt.Errorf("claude: agent_sdk_config.permission_mode %q is not one of default|plan|acceptEdits|auto|dontAsk|bypassPermissions", c.PermissionMode)
	}
	return c, true, nil
}

// gatherAgentSDK emits the declared Agent SDK program's governance posture once at start
//: the dangerous-knob findings, the subagent bypass-inheritance finding and the
// effective-cap drift. A no-op when no agent_sdk_config is declared. Read-only modeling —
// it never launches or configures an agent.
func (s *Source) gatherAgentSDK(at time.Time, dispatch func(model.Observation)) {
	if !s.cfg.agentSDKConfigSet {
		return
	}
	for _, f := range s.cfg.agentSDKConfig.PostureFindings("agent-sdk-program", at, s.cfg.agentSDKPolicy, s.managedConstraint) {
		dispatch(f)
	}
}

// ---- Managed Agents catalog target (XIV) -----------------------------------------

// ManagedAgentsSurface enumerates the hosted Claude Managed Agents REST surface as a
// catalog (XIV) / deployment target (CLA-10) — MODELING/inventory, not orchestration.
// The provider-auth matrix (Bedrock/Vertex/Foundry env vars) is NOT duplicated here:
// it is the SCP-08 deploy env-contract (see cmd/olivares gatewayFromEnv and the
// deploy env-contract doc). The REST resource list is declared, AsOf-stamped, and
// verified against the Agent SDK / Managed Agents docs — never fabricated.
type ManagedAgentsSurface struct {
	// CatalogKind is the catalog (XIV) entry kind this target maps to.
	CatalogKind string
	// RESTResources are the hosted Managed Agents REST resource paths (deployment
	// targets the control plane inventories, not endpoints it calls to orchestrate).
	RESTResources []string
	// AuthMatrixRef points at where the provider-auth env matrix lives (SCP-08), so it
	// is referenced, not re-modeled.
	AuthMatrixRef string
	// AsOf stamps when the surface was recorded.
	AsOf string
}

// ManagedAgents returns the declared Managed Agents catalog target.
func ManagedAgents() ManagedAgentsSurface {
	return ManagedAgentsSurface{
		CatalogKind:   "agent",
		RESTResources: []string{"/v1/agents", "/v1/agents/{id}", "/v1/agents/{id}/runs"},
		AuthMatrixRef: "SCP-08 deploy env-contract (ANTHROPIC_*_BASE_URL / CLAUDE_CODE_USE_* / *_SKIP_*_AUTH)",
		AsOf:          "2026-06-05",
	}
}
